package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gen2brain/beeep"
	"github.com/getlantern/systray"
	"github.com/robertpelloni/freellm/internal/config"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
	"github.com/robertpelloni/freellm/internal/icon"
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
	// --- Tray Icon & Title ---
	systray.SetIcon(icon.Gray)
	systray.SetTitle("FreeLLM")
	systray.SetTooltip("FreeLLM - Starting...")

	// --- Menu Structure (matches Python version) ---

	// Status line (disabled, shows current state)
	mStatus := systray.AddMenuItem("FreeLLM: Starting | Primary: None", "Current status")
	mStatus.Disable()

	systray.AddSeparator()

	// Primary Actions
	mOpen := systray.AddMenuItem("Open Chat Interface", "Open the LLM chat in browser")
	mDashboard := systray.AddMenuItem("Open Dashboard", "Open monitoring dashboard")
	mSettings := systray.AddMenuItem("Settings", "Open settings in browser")

	systray.AddSeparator()

	// Submenus
	mControl := systray.AddMenuItem("FreeLLM Control", "Proxy control options")
	mControlStart := mControl.AddSubMenuItem("Refresh Models", "Re-discover and benchmark all models")
	mControlViewLogs := mControl.AddSubMenuItem("View Proxy Logs", "Open log viewer")
	mControlViewConfig := mControl.AddSubMenuItem("View Config", "Open config editor")
	mControlBackup := mControl.AddSubMenuItem("Backup Config", "Save current config to .bak")

	systray.AddSeparator()

	mMaintenance := systray.AddMenuItem("Maintenance", "System maintenance options")
	mMaintClearSkips := mMaintenance.AddSubMenuItem("Clear Skip List", "Clear all manual model skips")
	mMaintClearBlacklist := mMaintenance.AddSubMenuItem("Clear Blacklist", "Remove all blacklisted models")
	mMaintResetStats := mMaintenance.AddSubMenuItem("Reset Provider Stats", "Reset all provider statistics")

	systray.AddSeparator()

	mStartup := systray.AddMenuItem("Start with Windows", "Launch on system startup")
	mStartupEnable := mStartup.AddSubMenuItem("Enable", "Add to Windows startup")
	mStartupDisable := mStartup.AddSubMenuItem("Disable", "Remove from Windows startup")

	systray.AddSeparator()

	mAutoPilot := systray.AddMenuItemCheckbox("Auto-Pilot Mode", "Automatically benchmark and route", true)
	mRouting := systray.AddMenuItemCheckbox("Master Routing", "Enable request routing", true)

	systray.AddSeparator()

	mCopyModel := systray.AddMenuItem("Copy Active Model", "Copy primary model ID to clipboard")

	systray.AddSeparator()

	mQuit := systray.AddMenuItem("Quit", "Quit FreeLLM")

	// --- Initialize Database ---
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	// --- Initialize Engine & Logger ---
	eventLogger := engine.NewEventLogger(100, database)

	apiKeys := map[string]string{
		"openrouter":   os.Getenv("OPENROUTER_API_KEY"),
		"groq":         os.Getenv("GROQ_API_KEY"),
		"github":       os.Getenv("GITHUB_TOKEN"),
		"deepinfra":    os.Getenv("DEEPINFRA_API_KEY"),
		"cerebras":     os.Getenv("CEREBRAS_API_KEY"),
		"huggingface":  os.Getenv("HUGGINGFACE_API_KEY"),
		"nvidia":       os.Getenv("NVIDIA_NIM_API_KEY"),
		"gemini":       os.Getenv("GEMINI_API_KEY"),
		"anthropic":    os.Getenv("ANTHROPIC_API_KEY"),
		"mistral":      os.Getenv("MISTRAL_API_KEY"),
		"cohere":       os.Getenv("COHERE_API_KEY"),
		"sambanova":    os.Getenv("SAMBANOVA_API_KEY"),
		"fireworks":    os.Getenv("FIREWORKS_API_KEY"),
		"hyperbolic":   os.Getenv("HYPERBOLIC_API_KEY"),
		"cloudflare":   os.Getenv("CLOUDFLARE_API_KEY"),
		"opencode_zen": os.Getenv("OPENCODE_ZEN_API_KEY"),
		"codestral":    os.Getenv("CODESTRAL_API_KEY"),
		"nvidia_nim":   os.Getenv("NVIDIA_API_KEY"),
	}

	keyCount := 0
	for _, v := range apiKeys {
		if v != "" {
			keyCount++
		}
	}
	log.Printf("API keys configured: %d/%d providers have keys", keyCount, len(apiKeys))

	benchmarker := engine.NewBenchmarker(apiKeys, 100, eventLogger)

	// --- Initialize Configuration ---
	cfgPath := "freellm-config.yaml"
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Printf("Warning: freellm-config.yaml not found, using defaults: %v", err)
		cfg = &config.Config{Port: 4000}
	}

	config.WatchConfig(cfgPath, func(newCfg *config.Config) {
		log.Println("Applying new configuration...")
		cfg = newCfg
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

	// --- Initialize Proxy Gateway ---
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

	gateway := proxy.NewGateway(10, database)
	gateway.RestoreQueue()

	go func() {
		addr := fmt.Sprintf(":%d", proxyPort)
		log.Printf("Starting FreeLLM Proxy on %s", addr)
		if err := http.ListenAndServe(addr, gateway); err != nil {
			log.Printf("Proxy failed: %v", err)
		}
	}()

	// --- Initialize Web Dashboard ---
	uiServer := ui.NewUIServer(database, eventLogger, gateway)
	go func() {
		log.Println("Starting Web Dashboard on :8080")
		if err := uiServer.Start(":8080"); err != nil {
			log.Printf("UI Server failed: %v", err)
		}
	}()

	// --- State variables ---
	routingEnabled := true
	autoPilot := true

	// --- Background Worker: Two-tier benchmarking ---
	refreshTrigger := make(chan bool, 1)
	fullRefreshInterval := 60 * time.Minute
	pulseInterval := 10 * time.Minute
	lastFullRefresh := time.Time{}

	// Function to update tray icon/tooltip based on model state
	updateTrayStatus := func() {
		models := gateway.GetModels()
		if len(models) == 0 {
			systray.SetIcon(icon.Gray)
			systray.SetTooltip("FreeLLM - No models available")
			mStatus.SetTitle("FreeLLM: Offline | Primary: None")
			return
		}
		top := models[0]
		lat := top.Latency

		var primaryLabel string
		if lat < 0.5 {
			systray.SetIcon(icon.Green)
			primaryLabel = fmt.Sprintf("%s (%.2fs)", top.ID, lat)
		} else if lat < 1.5 {
			systray.SetIcon(icon.Yellow)
			primaryLabel = fmt.Sprintf("%s (%.2fs)", top.ID, lat)
		} else {
			systray.SetIcon(icon.Red)
			primaryLabel = fmt.Sprintf("%s (%.2fs)", top.ID, lat)
		}

		systray.SetTooltip(fmt.Sprintf("FreeLLM - Primary: %s", primaryLabel))
		mStatus.SetTitle(fmt.Sprintf("FreeLLM: Live | Primary: %s | %d models", primaryLabel, len(models)))
	}

	go func() {
		for {
			ctx := context.Background()
			now := time.Now()
			timeSinceFull := now.Sub(lastFullRefresh)

			if timeSinceFull >= fullRefreshInterval || lastFullRefresh.IsZero() {
				log.Println("Full refresh: benchmarking all candidates...")
				systray.SetIcon(icon.Yellow)
				mStatus.SetTitle("FreeLLM: Syncing...")
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
				lastFullRefresh = time.Now()
				updateTrayStatus()
			} else {
				if routingEnabled {
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
				updateTrayStatus()
			}

			select {
			case <-refreshTrigger:
			case <-time.After(pulseInterval):
			}
		}
	}()

	// --- Operational Stability Ticker ---
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
			// Also update tray status every minute
			updateTrayStatus()
		}
	}()

	// --- Data Pruning Ticker (every 24h) ---
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			count, _ := db.PruneOldData(database, 30)
			log.Printf("Pruned %d old metric/log records", count)
		}
	}()

	// --- Proactive Health Monitor ---
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
				systray.SetIcon(icon.Red)
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

	// --- Menu Click Handlers ---
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

			case <-mControlStart.ClickedCh:
				log.Println("Refreshing models...")
				systray.SetIcon(icon.Yellow)
				mStatus.SetTitle("FreeLLM: Refreshing...")
				select {
				case refreshTrigger <- true:
				default:
				}

			case <-mControlViewLogs.ClickedCh:
				log.Println("Opening logs...")
				open.Run("http://localhost:8080#logs-tab")

			case <-mControlViewConfig.ClickedCh:
				log.Println("Opening config...")
				open.Run("http://localhost:8080#config-tab")

			case <-mControlBackup.ClickedCh:
				log.Println("Backing up config...")
				data, err := os.ReadFile(cfgPath)
				if err == nil {
					os.WriteFile(cfgPath+".bak", data, 0644)
					notify("FreeLLM", "Config backed up to "+cfgPath+".bak")
				}

			case <-mMaintClearSkips.ClickedCh:
				log.Println("Clearing skip list...")
				db.ClearSkips(database)
				notify("FreeLLM", "Skip list cleared")

			case <-mMaintClearBlacklist.ClickedCh:
				log.Println("Clearing blacklist...")
				db.ClearBlacklist(database)
				notify("FreeLLM", "Blacklist cleared")

			case <-mMaintResetStats.ClickedCh:
				log.Println("Resetting stats...")
				db.ResetStats(database)
				notify("FreeLLM", "All provider and model stats reset")

			case <-mStartupEnable.ClickedCh:
				log.Println("Enabling startup...")
				config.SetStartWithWindows(true)
				notify("FreeLLM", "Start with Windows enabled")

			case <-mStartupDisable.ClickedCh:
				log.Println("Disabling startup...")
				config.SetStartWithWindows(false)
				notify("FreeLLM", "Start with Windows disabled")

			case <-mAutoPilot.ClickedCh:
				autoPilot = !autoPilot
				if autoPilot {
					log.Println("Auto-Pilot enabled")
				} else {
					log.Println("Auto-Pilot disabled")
				}

			case <-mRouting.ClickedCh:
				routingEnabled = !routingEnabled
				if routingEnabled {
					log.Println("Master Routing enabled")
				} else {
					log.Println("Master Routing disabled")
				}

			case <-mCopyModel.ClickedCh:
				models := gateway.GetModels()
				if len(models) > 0 {
					modelID := models[0].ID
					// Try to copy to clipboard via PowerShell
					cmd := exec.Command("powershell", "-Command", fmt.Sprintf("Set-Clipboard -Value '%s'", modelID))
					cmd.Run()
					notify("FreeLLM", fmt.Sprintf("Copied: %s", modelID))
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
