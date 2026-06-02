package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/getlantern/systray"
	"github.com/robertpelloni/litellm_control_panel/internal/config"
	"github.com/robertpelloni/litellm_control_panel/internal/db"
	"github.com/robertpelloni/litellm_control_panel/internal/engine"
	"github.com/robertpelloni/litellm_control_panel/internal/proxy"
	"github.com/robertpelloni/litellm_control_panel/internal/ui"
)

func main() {
	// Single instance enforcement
	lockFile := filepath.Join(os.TempDir(), "litellm_control_panel_go.lock")
	if _, err := os.Stat(lockFile); err == nil {
		// Basic check, in real app we'd check if PID is alive
		log.Println("Another instance may be running. Cleaning up lock...")
		os.Remove(lockFile)
	}
	os.WriteFile(lockFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(lockFile)

	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("LiteLLM Control Panel")
	systray.SetTooltip("LiteLLM Control Panel (Go)")

	mOpen := systray.AddMenuItem("Open LLM Interface", "Open the interface in browser")
	mSettings := systray.AddMenuItem("Settings", "Change settings")
	systray.AddSeparator()
	mRefresh := systray.AddMenuItem("Refresh Now", "Run benchmarks immediately")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit the application")

	// Initialize Configuration
	cfgPath := "litellm-config.yaml"
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Printf("Warning: litellm-config.yaml not found, using defaults: %v", err)
		cfg = &config.Config{Port: 4000}
	}

	// Hot-Reloading
	config.WatchConfig(cfgPath, func(newCfg *config.Config) {
		log.Println("Applying new configuration...")
		cfg = newCfg
		// In real app, update proxyPort etc if needed
	})

	// Initialize Database
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	// Initialize Engine & Logger
	eventLogger := engine.NewEventLogger(100)
	apiKeys := map[string]string{
		"openrouter": os.Getenv("OPENROUTER_API_KEY"),
		"groq":       os.Getenv("GROQ_API_KEY"),
		"github":     os.Getenv("GITHUB_TOKEN"),
	}
	benchmarker := engine.NewBenchmarker(apiKeys, 100, eventLogger)

	// Initialize Proxy Gateway
	proxyPort := cfg.Port
	if proxyPort == 0 { proxyPort = 4000 }
	gateway := proxy.NewGateway(10, database) // Max 10 active requests
	gateway.RestoreQueue()
	go func() {
		addr := fmt.Sprintf(":%d", proxyPort)
		log.Printf("Starting LiteLLM Proxy on %s", addr)
		if err := http.ListenAndServe(addr, gateway); err != nil {
			log.Printf("Proxy failed: %v", err)
		}
	}()

	// Initialize Web Dashboard
	uiServer := ui.NewUIServer(database, eventLogger, gateway)
	go func() {
		log.Println("Starting Web Dashboard on :8080")
		if err := uiServer.Start(":8080"); err != nil {
			log.Printf("UI Server failed: %v", err)
		}
	}()

	// Background worker for benchmarking
	refreshTrigger := make(chan bool, 1)
	go func() {
		for {
			log.Println("Continuous Model Discovery & Benchmarking...")
			ctx := context.Background()

			candidates := benchmarker.FetchModels(ctx)
			log.Printf("Discovered %d model candidates", len(candidates))

			ranked := benchmarker.RunBenchmark(ctx, candidates)
			gateway.UpdateModels(ranked)
			uiServer.UpdateModels(ranked)

			// 3. Update Pricing
			for _, m := range ranked {
				db.UpdateModelPricing(database, m.ID, m.Provider, m.PromptPrice, m.CompletionPrice)
			}

			topModel := "none"
			if len(ranked) > 0 {
				topModel = ranked[0].ID
			}

			db.LogActivity(database, "Sync Complete", topModel, fmt.Sprintf("Ranked %d models", len(ranked)))

			select {
			case <-refreshTrigger:
			case <-time.After(1 * time.Hour):
			}
		}
	}()

	// Operational Stability Ticker
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			var qpm int
			var totalTokens int
			oneMinAgo := time.Now().Add(-1 * time.Minute)
			err := database.QueryRow("SELECT COUNT(*), SUM(prompt_tokens + completion_tokens) FROM usage WHERE timestamp > ?", oneMinAgo).Scan(&qpm, &totalTokens)
			if err == nil {
				tps := float64(totalTokens) / 60.0
				db.LogStabilityMetric(database, float64(qpm), tps)
			}
		}
	}()

	// Proactive Health Monitor
	go func() {
		failCount := 0
		for {
			time.Sleep(1 * time.Minute)
			models := gateway.GetModels()
			if len(models) == 0 {
				continue
			}

			top := models[0]
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := benchmarker.MeasureLatency(ctx, top.ID, top.Provider)
			cancel()

			if err != nil {
				failCount++
				log.Printf("Health check failed for %s (%d/3): %v", top.ID, failCount, err)
				db.LogActivity(database, "Health Check Failure", top.ID, fmt.Sprintf("Attempt %d/3 failed", failCount))
			} else {
				failCount = 0
			}

			if failCount >= 3 {
				log.Println("Proactive health threshold reached. Triggering refresh...")
				db.LogActivity(database, "Fallback Triggered", top.ID, "Triggering refresh due to consecutive health failures")
				select {
				case refreshTrigger <- true:
				default:
				}
				failCount = 0
			}
		}
	}()

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				log.Println("Opening LLM Interface...")
				// open.Run("http://localhost:4000")
			case <-mSettings.ClickedCh:
				log.Println("Opening Settings...")
			case <-mRefresh.ClickedCh:
				log.Println("Refreshing...")
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	// Cleanup
}
