package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gen2brain/beeep"
	"github.com/getlantern/systray"
	"github.com/robertpelloni/freellm/internal/config"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
	"github.com/robertpelloni/freellm/internal/proxy"
	"github.com/robertpelloni/freellm/internal/ui"
	"github.com/skratchdot/open-golang/open"
)

func notify(title, message string) {
	beeep.Notify(title, message, "")
}

func main() {
	// Single instance enforcement
	lockFile := filepath.Join(os.TempDir(), "freellm.lock")
	if _, err := os.Stat(lockFile); err == nil {
		log.Println("Another instance may be running. Cleaning up lock...")
		os.Remove(lockFile)
	}
	os.WriteFile(lockFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(lockFile)

	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("FreeLLM")
	systray.SetTooltip("FreeLLM - Free AI Model Router")

	mOpen := systray.AddMenuItem("Open Chat", "Open the LLM chat interface")
	mDashboard := systray.AddMenuItem("Open Dashboard", "Open monitoring dashboard")
	mSettings := systray.AddMenuItem("Settings", "Change settings")
	systray.AddSeparator()
	mRefresh := systray.AddMenuItem("Refresh Models", "Re-discover and benchmark models")
	mStatus := systray.AddMenuItem("Status: Starting...", "Current status")
	mStatus.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit FreeLLM")

	// Initialize Database
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	// Initialize Engine & Logger
	eventLogger := engine.NewEventLogger(100, database)

	apiKeys := map[string]string{
		"openrouter":  os.Getenv("OPENROUTER_API_KEY"),
		"groq":        os.Getenv("GROQ_API_KEY"),
		"github":      os.Getenv("GITHUB_TOKEN"),
		"deepinfra":   os.Getenv("DEEPINFRA_API_KEY"),
		"cerebras":    os.Getenv("CEREBRAS_API_KEY"),
		"huggingface": os.Getenv("HUGGINGFACE_API_KEY"),
		"nvidia":      os.Getenv("NVIDIA_NIM_API_KEY"),
		"gemini":      os.Getenv("GEMINI_API_KEY"),
		"anthropic":   os.Getenv("ANTHROPIC_API_KEY"),
		"mistral":     os.Getenv("MISTRAL_API_KEY"),
		"cohere":      os.Getenv("COHERE_API_KEY"),
		"sambanova":   os.Getenv("SAMBANOVA_API_KEY"),
		"fireworks":   os.Getenv("FIREWORKS_API_KEY"),
		"hyperbolic":  os.Getenv("HYPERBOLIC_API_KEY"),
		"cloudflare":  os.Getenv("CLOUDFLARE_API_KEY"),
		"opencode_zen": os.Getenv("OPENCODE_ZEN_API_KEY"),
		"codestral":   os.Getenv("CODESTRAL_API_KEY"),
		"nvidia_nim":  os.Getenv("NVIDIA_API_KEY"),
	}

	// Debug: count configured API keys
	keyCount := 0
	for _, v := range apiKeys {
		if v != "" {
			keyCount++
		}
	}
	log.Printf("API keys configured: %d/%d providers have keys", keyCount, len(apiKeys))

	benchmarker := engine.NewBenchmarker(apiKeys, 100, eventLogger)

	// Initialize Configuration
	cfgPath := "freellm-config.yaml"
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Printf("Warning: freellm-config.yaml not found, using defaults: %v", err)
		cfg = &config.Config{Port: 4000}
	}

	// Hot-Reloading
	config.WatchConfig(cfgPath, func(newCfg *config.Config) {
		log.Println("Applying new configuration...")
		cfg = newCfg
		// Update Engine Base URLs
		if newCfg.Providers != nil {
			for p, pcfg := range newCfg.Providers {
				if pcfg.BaseURL != "" {
					benchmarker.BaseURLs[p] = pcfg.BaseURL
				}
				if pcfg.ModelsURL != "" {
					benchmarker.BaseURLs[p+"_models"] = pcfg.ModelsURL
				}
				if pcfg.Completions != "" {
					benchmarker.BaseURLs[p+"_completions"] = pcfg.Completions
				}
			}
		}
	})

	// Initialize Proxy Gateway
	proxyPort := cfg.Port
	if proxyPort == 0 {
		proxyPort = 4000
	}
	if envPort := os.Getenv("FREELLM_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			proxyPort = p
			log.Printf("Using FREELLM_PORT=%d from environment", proxyPort)
		}
	}

	gateway := proxy.NewGateway(10, database) // Max 10 active requests
	gateway.RestoreQueue()

	go func() {
		addr := fmt.Sprintf(":%d", proxyPort)
		log.Printf("Starting FreeLLM Proxy on %s", addr)
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

	// Background worker: Two-tier benchmarking cadence
	// Tier 1: Quick pulse every 10 min (top 5 models only)
	// Tier 2: Full refresh every 60 min (all candidates)
	refreshTrigger := make(chan bool, 1)
	fullRefreshInterval := 60 * time.Minute
	pulseInterval := 10 * time.Minute
	lastFullRefresh := time.Time{} // Zero time forces first cycle to be full refresh

	go func() {
		for {
			ctx := context.Background()
			now := time.Now()
			timeSinceFull := now.Sub(lastFullRefresh)

			if timeSinceFull >= fullRefreshInterval || lastFullRefresh.IsZero() {
				// Full refresh: benchmark all candidates
				log.Println("Full refresh: benchmarking all candidates...")
				mStatus.SetTitle("Status: Syncing...")
				notify("FreeLLM Sync", "Full model discovery started...")

				candidates := benchmarker.FetchModels(ctx, database)
				log.Printf("Discovered %d model candidates", len(candidates))

				ranked := benchmarker.RunBenchmark(ctx, candidates, database)
				gateway.UpdateModels(ranked)
				uiServer.UpdateModels(ranked)

				for _, m := range ranked {
					db.UpdateModelPricing(database, m.ID, m.Provider, m.PromptPrice, m.CompletionPrice)
				}

				topModel := "none"
				if len(ranked) > 0 {
					topModel = ranked[0].ID
					notify("Sync Complete", fmt.Sprintf("Top Model: %s (%.2fs)", topModel, ranked[0].Latency))
				}
				db.LogActivity(database, "Sync Complete", topModel, fmt.Sprintf("Ranked %d models", len(ranked)))
				mStatus.SetTitle(fmt.Sprintf("Status: %d models live", len(ranked)))
				lastFullRefresh = time.Now()
			} else {
				// Quick pulse: re-check only top models
				currentModels := gateway.GetModels()
				if len(currentModels) > 0 {
					ranked, changed := benchmarker.QuickPulse(ctx, currentModels, 5, database)
					if changed {
						gateway.UpdateModels(ranked)
						uiServer.UpdateModels(ranked)
						log.Println("Quick pulse: rankings changed, config updated")
					} else {
						log.Println("Quick pulse: no changes")
					}
				}
			}

			select {
			case <-refreshTrigger:
			case <-time.After(pulseInterval):
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

	// Data Pruning Ticker (every 24h)
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			count, _ := db.PruneOldData(database, 30) // Keep 30 days
			log.Printf("Pruned %d old metric/log records", count)
		}
	}()

	// Proactive Health Monitor with startup grace period
	go func() {
		failCount := 0
		startupGrace := time.Now().Add(30 * time.Second)
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
				if time.Now().Before(startupGrace) {
					continue
				}
				failCount++
				log.Printf("Health check failed for %s (%d/3): %v", top.ID, failCount, err)
				db.LogActivity(database, "Health Check Failure", top.ID, fmt.Sprintf("Attempt %d/3 failed", failCount))
				notify("Health Alert", fmt.Sprintf("Health check failed for %s (%d/3)", top.ID, failCount))
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
				log.Println("Opening Chat Interface...")
				open.Run(fmt.Sprintf("http://localhost:%d", proxyPort))
			case <-mDashboard.ClickedCh:
				log.Println("Opening Dashboard...")
				open.Run("http://localhost:8080")
			case <-mSettings.ClickedCh:
				log.Println("Opening Settings...")
				open.Run("http://localhost:8080#config-tab")
			case <-mRefresh.ClickedCh:
				log.Println("Refreshing models...")
				mStatus.SetTitle("Status: Refreshing...")
				select {
				case refreshTrigger <- true:
				default:
				}
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
