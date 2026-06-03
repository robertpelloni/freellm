package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
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

// menuAction represents a click event from any menu item
type menuAction struct {
	id   string
	data interface{}
}

// menuEventBus collects all menu clicks into a single channel
var menuEventBus = make(chan menuAction, 100)

func click(id string, data interface{}) {
	menuEventBus <- menuAction{id: id, data: data}
}

// watchMenuItem launches a goroutine that sends a menu action when the item is clicked
func watchMenuItem(item *systray.MenuItem, id string, data interface{}) {
	go func() {
		for range item.ClickedCh {
			click(id, data)
		}
	}()
}

func main() {
	// Single instance enforcement
	lockFile := filepath.Join(os.TempDir(), "freellm.lock")
	if data, err := os.ReadFile(lockFile); err == nil {
		if pid, err := strconv.Atoi(string(bytes.TrimSpace(data))); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				if proc.Signal(nil) == nil {
					log.Fatalf("Another FreeLLM instance is already running (PID %d)", pid)
				}
			}
		}
		os.Remove(lockFile)
	}
	os.WriteFile(lockFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(lockFile)

	systray.Run(onReady, onExit)
}

func onReady() {
	// ============================================================
	//  Initialize Core Services
	// ============================================================

	systray.SetIcon(icon.Gray)
	systray.SetTitle("FreeLLM")
	systray.SetTooltip("FreeLLM - Starting...")

	// --- Database ---
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	// --- Event Logger ---
	eventLogger := engine.NewEventLogger(100, database)

	// --- API Keys ---
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

	// --- Configuration ---
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

	// --- Proxy Gateway ---
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

	// --- Web Dashboard ---
	uiServer := ui.NewUIServer(database, eventLogger, gateway)
	go func() {
		log.Println("Starting Web Dashboard on :8080")
		if err := uiServer.Start(":8080"); err != nil {
			log.Printf("UI Server failed: %v", err)
		}
	}()

	// ============================================================
	//  State Variables
	// ============================================================

	routingEnabled := true
	autoPilot := true
	refreshTrigger := make(chan bool, 1)
	fullRefreshInterval := 60 * time.Minute
	pulseInterval := 10 * time.Minute
	lastFullRefresh := time.Time{}

	// ============================================================
	//  Build the Full Right-Click Menu (matching Python version)
	// ============================================================

	// --- Top Section: Master Routing + Copy Active Model ---
	mRouting := systray.AddMenuItemCheckbox("Master Routing", "Enable request routing", true)
	watchMenuItem(mRouting, "toggle_routing", nil)

	mCopyModel := systray.AddMenuItem("Copy Active Model", "Copy primary model ID to clipboard")
	watchMenuItem(mCopyModel, "copy_active_model", nil)

	systray.AddSeparator()

	// --- Status Line ---
	mStatus := systray.AddMenuItem("FreeLLM: Starting | Primary: None", "Current status")
	mStatus.Disable()

	systray.AddSeparator()

	// --- Primary Actions ---
	mOpen := systray.AddMenuItem("Open LLM Interface", "Open the LLM chat in browser")
	watchMenuItem(mOpen, "open_interface", nil)

	mSettings := systray.AddMenuItem("Settings", "Open settings")
	watchMenuItem(mSettings, "open_settings", nil)

	systray.AddSeparator()

	// --- UI Windows ---
	mQuickQuery := systray.AddMenuItem("Quick Query", "Send a quick query to the LLM")
	watchMenuItem(mQuickQuery, "open_quick_query", nil)

	mModelComparison := systray.AddMenuItem("Model Comparison", "Compare model responses side by side")
	watchMenuItem(mModelComparison, "open_comparison", nil)

	mDashboard := systray.AddMenuItem("Show Dashboard", "Open monitoring dashboard")
	watchMenuItem(mDashboard, "open_dashboard", nil)

	mLeaderboard := systray.AddMenuItem("Model Leaderboard", "View model rankings")
	watchMenuItem(mLeaderboard, "open_leaderboard", nil)

	mSavings := systray.AddMenuItem("Cost Savings", "View cost savings report")
	watchMenuItem(mSavings, "open_savings", nil)

	mMonitoring := systray.AddMenuItem("Monitoring Dashboard", "Real-time monitoring")
	watchMenuItem(mMonitoring, "open_monitoring", nil)

	mProtocol := systray.AddMenuItem("Protocol Oversight", "View protocol compliance")
	watchMenuItem(mProtocol, "open_protocol", nil)

	mExecution := systray.AddMenuItem("Execution Dashboard", "View execution metrics")
	watchMenuItem(mExecution, "open_execution", nil)

	mSystemStatus := systray.AddMenuItem("System Status", "View system health")
	watchMenuItem(mSystemStatus, "open_status", nil)

	systray.AddSeparator()

	// --- ★ Primary Models Submenu (populated dynamically) ---
	mPrimaryGroup := systray.AddMenuItem("★ Primary (0)", "Primary model group")
	mPrimaryPlaceholder := mPrimaryGroup.AddSubMenuItem("No models loaded yet...", "")
	mPrimaryPlaceholder.Disable()

	// --- Fallback Models Submenu (populated dynamically) ---
	mFallbackGroup := systray.AddMenuItem("  Fallback (0)", "Fallback model group")
	mFallbackPlaceholder := mFallbackGroup.AddSubMenuItem("No models loaded yet...", "")
	mFallbackPlaceholder.Disable()

	systray.AddSeparator()

	// --- Auto-Pilot & Refresh ---
	mAutoPilot := systray.AddMenuItemCheckbox("Auto-Pilot Mode", "Automatically benchmark and route", true)
	watchMenuItem(mAutoPilot, "toggle_autopilot", nil)

	mRefreshNow := systray.AddMenuItem("Refresh Now", "Force a model refresh now")
	watchMenuItem(mRefreshNow, "refresh_now", nil)

	systray.AddSeparator()

	// --- Enable Providers Submenu (populated dynamically) ---
	mProviders := systray.AddMenuItem("Enable Providers", "Toggle provider on/off")
	mProviderPlaceholder := mProviders.AddSubMenuItem("Loading providers...", "")
	mProviderPlaceholder.Disable()

	// --- Documentation ---
	mDocs := systray.AddMenuItem("Documentation", "Open FreeLLM documentation")
	watchMenuItem(mDocs, "open_docs", nil)

	// --- Start with Windows ---
	mStartup := systray.AddMenuItem("Start with Windows", "Launch on system startup")
	mStartupEnable := mStartup.AddSubMenuItem("Enable", "Add to Windows startup")
	watchMenuItem(mStartupEnable, "startup_enable", nil)
	mStartupDisable := mStartup.AddSubMenuItem("Disable", "Remove from Windows startup")
	watchMenuItem(mStartupDisable, "startup_disable", nil)

	// --- Maintenance ---
	mMaintenance := systray.AddMenuItem("Maintenance", "System maintenance options")
	mMaintClearSkips := mMaintenance.AddSubMenuItem("Clear Skip List", "Clear all manual model skips")
	watchMenuItem(mMaintClearSkips, "maint_clear_skips", nil)
	mMaintClearBlacklist := mMaintenance.AddSubMenuItem("Clear Blacklist", "Remove all blacklisted models")
	watchMenuItem(mMaintClearBlacklist, "maint_clear_blacklist", nil)
	mMaintResetStats := mMaintenance.AddSubMenuItem("Reset Provider Stats", "Reset all provider and model statistics")
	watchMenuItem(mMaintResetStats, "maint_reset_stats", nil)
	mMaintCleanupProbes := mMaintenance.AddSubMenuItem("Cleanup Old Probes (>90d)", "Delete probe history older than 90 days")
	watchMenuItem(mMaintCleanupProbes, "maint_cleanup_probes", nil)
	mMaintBackupConfig := mMaintenance.AddSubMenuItem("Backup FreeLLM Config", "Save current config to .bak")
	watchMenuItem(mMaintBackupConfig, "maint_backup_config", nil)
	mMaintRestoreConfig := mMaintenance.AddSubMenuItem("Restore FreeLLM Config", "Restore config from .bak backup")
	watchMenuItem(mMaintRestoreConfig, "maint_restore_config", nil)

	systray.AddSeparator()

	// --- FreeLLM Control ---
	mControl := systray.AddMenuItem("FreeLLM Control", "Proxy control options")
	mControlRefresh := mControl.AddSubMenuItem("Refresh Models", "Re-discover and benchmark all models")
	watchMenuItem(mControlRefresh, "control_refresh", nil)
	mControlViewLogs := mControl.AddSubMenuItem("View Proxy Logs", "Open log viewer")
	watchMenuItem(mControlViewLogs, "control_view_logs", nil)
	mControlViewEngineLogs := mControl.AddSubMenuItem("View Engine Logs", "View engine/benchmark logs")
	watchMenuItem(mControlViewEngineLogs, "control_view_engine_logs", nil)
	mControlViewConfig := mControl.AddSubMenuItem("View Config", "Open config editor")
	watchMenuItem(mControlViewConfig, "control_view_config", nil)

	systray.AddSeparator()

	// --- Quit ---
	mQuit := systray.AddMenuItem("Quit", "Quit FreeLLM")
	watchMenuItem(mQuit, "quit", nil)

	// ============================================================
	//  Dynamic Menu Building (models + providers)
	// ============================================================

	type modelMenuEntry struct {
		modelID   string
		isPrimary bool
	}

	var dynamicModels []modelMenuEntry
	var dynamicProviders []string
	var dynMu sync.Mutex
	menuBuilt := false

	rebuildDynamicMenu := func() {
		dynMu.Lock()
		defer dynMu.Unlock()

		models := gateway.GetModels()
		primaryCount := gateway.PrimaryCount

		pCount := 0
		if len(models) < primaryCount {
			pCount = len(models)
		} else {
			pCount = primaryCount
		}
		fCount := len(models) - primaryCount
		if fCount < 0 {
			fCount = 0
		}

		mPrimaryGroup.SetTitle(fmt.Sprintf("★ Primary (%d)", pCount))
		mFallbackGroup.SetTitle(fmt.Sprintf("  Fallback (%d)", fCount))

		if menuBuilt && len(dynamicModels) == len(models) {
			// Same count, just update titles
			for i, m := range models {
				isPrimary := i < primaryCount
				latStr := "?"
				if m.Latency > 0 {
					latStr = fmt.Sprintf("%.2fs", m.Latency)
				}
				scoreStr := ""
				if m.Score > 0 {
					scoreStr = fmt.Sprintf("score=%.0f", m.Score)
				}
				paramsStr := ""
				if m.Parameters > 0 {
					paramsStr = fmt.Sprintf("%dB", m.Parameters)
				}
				groupTag := "★"
				if !isPrimary {
					groupTag = "  "
				}
				label := fmt.Sprintf("%s %s (%s) %s %s %s", groupTag, m.ID, m.Provider, paramsStr, latStr, scoreStr)
				_ = label // Titles can't be updated on sub-items in systray v1.2.2
			}
			return
		}

		// Build new model submenus (note: systray doesn't support removing items,
		// so we only build once and add incrementally)
		if !menuBuilt {
			for i, m := range models {
				isPrimary := i < primaryCount
				latStr := "?"
				if m.Latency > 0 {
					latStr = fmt.Sprintf("%.2fs", m.Latency)
				}
				scoreStr := ""
				if m.Score > 0 {
					scoreStr = fmt.Sprintf("score=%.0f", m.Score)
				}
				paramsStr := ""
				if m.Parameters > 0 {
					paramsStr = fmt.Sprintf("%dB", m.Parameters)
				}
				groupTag := "★"
				if !isPrimary {
					groupTag = "  "
				}
				label := fmt.Sprintf("%s %s (%s) %s %s %s", groupTag, m.ID, m.Provider, paramsStr, latStr, scoreStr)

				var parent *systray.MenuItem
				if isPrimary {
					parent = mPrimaryGroup
				} else {
					parent = mFallbackGroup
				}

				mEntry := parent.AddSubMenuItem(label, m.ID)

				// "Set as Primary ★"
				mSetPrimary := mEntry.AddSubMenuItem("Set as Primary ★", "Make this the #1 model")
				watchMenuItem(mSetPrimary, "model_set_primary", m.ID)

				if isPrimary {
					// "↓ Demote to Fallback"
					if i >= 0 { // always show for primary models
						mDemote := mEntry.AddSubMenuItem("↓ Demote to Fallback", "Move to fallback group")
						watchMenuItem(mDemote, "model_demote", m.ID)
					}
					if i > 0 {
						mUp := mEntry.AddSubMenuItem("↑ Move Up", "Move higher in priority")
						watchMenuItem(mUp, "model_move_up", m.ID)
					}
					if i < primaryCount-1 {
						mDown := mEntry.AddSubMenuItem("↓ Move Down", "Move lower in priority")
						watchMenuItem(mDown, "model_move_down", m.ID)
					}
				} else {
					// "↑ Promote to Primary"
					mPromote := mEntry.AddSubMenuItem("↑ Promote to Primary", "Move to primary group")
					watchMenuItem(mPromote, "model_promote", m.ID)
					if i > primaryCount {
						mUp := mEntry.AddSubMenuItem("↑ Move Up", "Move higher in priority")
						watchMenuItem(mUp, "model_move_up", m.ID)
					}
					if i < len(models)-1 {
						mDown := mEntry.AddSubMenuItem("↓ Move Down", "Move lower in priority")
						watchMenuItem(mDown, "model_move_down", m.ID)
					}
				}

				// Skip & Blacklist (always available)
				mSkip := mEntry.AddSubMenuItem("Skip (24h)", "Skip this model for 24 hours")
				watchMenuItem(mSkip, "model_skip", m.ID)
				mBlacklist := mEntry.AddSubMenuItem("Blacklist", "Permanently blacklist this model")
				watchMenuItem(mBlacklist, "model_blacklist", m.ID)

				dynamicModels = append(dynamicModels, modelMenuEntry{
					modelID:   m.ID,
					isPrimary: isPrimary,
				})
			}

			// Build provider toggle checkboxes
			providerHealth, _ := db.GetProviderHealth(database)
			for _, ph := range providerHealth {
				cb := mProviders.AddSubMenuItemCheckbox(ph.Name,
					fmt.Sprintf("Toggle %s (latency=%.1fs, success=%.0f%%)", ph.Name, ph.AvgLatency, ph.SuccessRate),
					ph.Enabled)
				watchMenuItem(cb, "toggle_provider", ph.Name)
				dynamicProviders = append(dynamicProviders, ph.Name)
			}

			menuBuilt = true
		}
	}

	// ============================================================
	//  Tray Status Update
	// ============================================================

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

	// ============================================================
	//  Background Workers
	// ============================================================

	// --- Two-tier benchmarking worker ---
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
				rebuildDynamicMenu()
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

	// --- Initial dynamic menu build (deferred to let models load) ---
	go func() {
		time.Sleep(15 * time.Second)
		rebuildDynamicMenu()
	}()

	// ============================================================
	//  Central Menu Event Handler
	// ============================================================

	go func() {
		for action := range menuEventBus {
			switch action.id {

			// --- Top Section ---
			case "toggle_routing":
				routingEnabled = !routingEnabled
				if routingEnabled {
					log.Println("Master Routing enabled")
				} else {
					log.Println("Master Routing disabled")
				}

			case "copy_active_model":
				models := gateway.GetModels()
				if len(models) > 0 {
					modelID := models[0].ID
					cmd := exec.Command("powershell", "-Command",
						fmt.Sprintf("Set-Clipboard -Value '%s'", modelID))
					cmd.Run()
					notify("FreeLLM", fmt.Sprintf("Copied: %s", modelID))
				}

			// --- Primary Actions ---
			case "open_interface":
				log.Println("Opening LLM Interface...")
				open.Run(fmt.Sprintf("http://localhost:%d", proxyPort))

			case "open_settings":
				log.Println("Opening Settings...")
				open.Run("http://localhost:8080#config-tab")

			// --- UI Windows ---
			case "open_quick_query":
				log.Println("Opening Quick Query...")
				open.Run(fmt.Sprintf("http://localhost:%d", proxyPort))

			case "open_comparison":
				log.Println("Opening Model Comparison...")
				open.Run("http://localhost:8080#comparison-tab")

			case "open_dashboard":
				log.Println("Opening Dashboard...")
				open.Run("http://localhost:8080")

			case "open_leaderboard":
				log.Println("Opening Leaderboard...")
				open.Run("http://localhost:8080#rankings-tab")

			case "open_savings":
				log.Println("Opening Cost Savings...")
				open.Run("http://localhost:8080#savings-tab")

			case "open_monitoring":
				log.Println("Opening Monitoring Dashboard...")
				open.Run("http://localhost:8080#monitoring-tab")

			case "open_protocol":
				log.Println("Opening Protocol Oversight...")
				open.Run("http://localhost:8080#protocol-tab")

			case "open_execution":
				log.Println("Opening Execution Dashboard...")
				open.Run("http://localhost:8080#execution-tab")

			case "open_status":
				log.Println("Opening System Status...")
				open.Run("http://localhost:8080#status-tab")

			// --- Auto-Pilot & Refresh ---
			case "toggle_autopilot":
				autoPilot = !autoPilot
				if autoPilot {
					log.Println("Auto-Pilot enabled")
				} else {
					log.Println("Auto-Pilot disabled")
				}

			case "refresh_now":
				log.Println("Refreshing models...")
				systray.SetIcon(icon.Yellow)
				mStatus.SetTitle("FreeLLM: Refreshing...")
				lastFullRefresh = time.Time{} // force full refresh
				select {
				case refreshTrigger <- true:
				default:
				}

			// --- Documentation ---
			case "open_docs":
				log.Println("Opening documentation...")
				open.Run("https://docs.freellm.ai/")

			// --- Startup ---
			case "startup_enable":
				log.Println("Enabling startup...")
				config.SetStartWithWindows(true)
				notify("FreeLLM", "Start with Windows enabled")

			case "startup_disable":
				log.Println("Disabling startup...")
				config.SetStartWithWindows(false)
				notify("FreeLLM", "Start with Windows disabled")

			// --- Maintenance ---
			case "maint_clear_skips":
				log.Println("Clearing skip list...")
				db.ClearSkips(database)
				notify("FreeLLM", "Skip list cleared")

			case "maint_clear_blacklist":
				log.Println("Clearing blacklist...")
				db.ClearBlacklist(database)
				notify("FreeLLM", "Blacklist cleared")

			case "maint_reset_stats":
				log.Println("Resetting stats...")
				db.ResetStats(database)
				notify("FreeLLM", "All provider and model stats reset")

			case "maint_cleanup_probes":
				log.Println("Cleaning up old probes...")
				count, err := db.PruneOldData(database, 90)
				if err == nil {
					notify("FreeLLM", fmt.Sprintf("Cleaned up %d old probe records", count))
				}

			case "maint_backup_config":
				log.Println("Backing up config...")
				data, err := os.ReadFile(cfgPath)
				if err == nil {
					os.WriteFile(cfgPath+".bak", data, 0644)
					notify("FreeLLM", "Config backed up to "+cfgPath+".bak")
				}

			case "maint_restore_config":
				log.Println("Restoring config from backup...")
				data, err := os.ReadFile(cfgPath + ".bak")
				if err == nil {
					os.WriteFile(cfgPath, data, 0644)
					notify("FreeLLM", "Config restored from backup")
					if newCfg, err := config.LoadConfig(cfgPath); err == nil {
						cfg = newCfg
					}
				} else {
					notify("FreeLLM", "No backup config found")
				}

			// --- FreeLLM Control ---
			case "control_refresh":
				log.Println("Refreshing models (control)...")
				systray.SetIcon(icon.Yellow)
				mStatus.SetTitle("FreeLLM: Refreshing...")
				lastFullRefresh = time.Time{}
				select {
				case refreshTrigger <- true:
				default:
				}

			case "control_view_logs":
				log.Println("Opening proxy logs...")
				open.Run("http://localhost:8080#logs-tab")

			case "control_view_engine_logs":
				log.Println("Opening engine logs...")
				open.Run("http://localhost:8080#engine-logs-tab")

			case "control_view_config":
				log.Println("Opening config...")
				open.Run("http://localhost:8080#config-tab")

			// --- Model Submenu Actions ---
			case "model_set_primary":
				modelID := action.data.(string)
				log.Printf("Setting %s as primary ★", modelID)
				gateway.SetModelPrimary(modelID)
				db.LogActivity(database, "Set Primary", modelID, "Manually set as primary model")
				notify("FreeLLM", fmt.Sprintf("Primary set to: %s", modelID))
				updateTrayStatus()

			case "model_demote":
				modelID := action.data.(string)
				log.Printf("Demoting %s to fallback", modelID)
				gateway.DemoteModel(modelID)
				db.LogActivity(database, "Demote Model", modelID, "Demoted to fallback group")
				notify("FreeLLM", fmt.Sprintf("Demoted: %s", modelID))

			case "model_promote":
				modelID := action.data.(string)
				log.Printf("Promoting %s to primary", modelID)
				gateway.PromoteModel(modelID)
				db.LogActivity(database, "Promote Model", modelID, "Promoted to primary group")
				notify("FreeLLM", fmt.Sprintf("Promoted: %s", modelID))
				updateTrayStatus()

			case "model_move_up":
				modelID := action.data.(string)
				log.Printf("Moving %s up", modelID)
				gateway.MoveModelUp(modelID)

			case "model_move_down":
				modelID := action.data.(string)
				log.Printf("Moving %s down", modelID)
				gateway.MoveModelDown(modelID)

			case "model_skip":
				modelID := action.data.(string)
				log.Printf("Skipping %s for 24h", modelID)
				db.SkipModel(database, modelID, 24)
				db.LogActivity(database, "Skip Model", modelID, "Skipped for 24 hours")
				notify("FreeLLM", fmt.Sprintf("Skipped (24h): %s", modelID))

			case "model_blacklist":
				modelID := action.data.(string)
				log.Printf("Blacklisting %s", modelID)
				db.BlacklistModel(database, modelID)
				db.LogActivity(database, "Blacklist Model", modelID, "Permanently blacklisted")
				notify("FreeLLM", fmt.Sprintf("Blacklisted: %s", modelID))

			// --- Provider Toggle ---
			case "toggle_provider":
				providerName := action.data.(string)
				// Get current status from DB
				providers, _ := db.GetProviderHealth(database)
				var currentEnabled bool
				for _, p := range providers {
					if p.Name == providerName {
						currentEnabled = p.Enabled
						break
					}
				}
				newState := !currentEnabled
				log.Printf("Toggling provider %s: enabled=%v", providerName, newState)
				db.SetProviderStatus(database, providerName, newState)
				db.LogActivity(database, "Toggle Provider", providerName,
					fmt.Sprintf("Provider %s: enabled=%v", providerName, newState))

			// --- Quit ---
			case "quit":
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	// Cleanup
}
