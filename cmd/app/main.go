package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/robertpelloni/freellm/internal/config"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
	"github.com/robertpelloni/freellm/internal/proxy"
	"github.com/robertpelloni/freellm/internal/tray"
	"github.com/robertpelloni/freellm/internal/ui"
)

var (
	version        = "4.6.5"
	tokdietCmd     *exec.Cmd
	tokdietLogFile *os.File
	tokdietMu      sync.Mutex
)

func main() {
	// Check for --version flag
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("freellm v" + version)
		return
	}

	// Global panic recovery — log stack trace to file instead of dying silently
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, true)
			crashLog := fmt.Sprintf("[%s] PANIC: %v\n%s\n", time.Now().Format(time.RFC3339), r, buf[:n])
			os.WriteFile(filepath.Join("logs", "crash.log"), []byte(crashLog), 0644)
			log.Printf("\n========== CRASH ==========\n%s", crashLog)
		}
	}()

	log.Println("[FreeLLM] Starting headless server on :4000")

	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

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
		"codestral":   os.Getenv("CODESTRAL_API_KEY"),
		"nvidia_nim":  os.Getenv("NVIDIA_API_KEY"),
		"siliconflow": os.Getenv("SILICONFLOW_API_KEY"),
		"together":    os.Getenv("TOGETHER_API_KEY"),
		"novita":      os.Getenv("NOVITA_API_KEY"),
		"nebius":      os.Getenv("NEBIUS_API_KEY"),
		"deepseek":    os.Getenv("DEEPSEEK_API_KEY"),
		"ai21":        os.Getenv("AI21_API_KEY"),
		"replicate":   os.Getenv("REPLICATE_API_TOKEN"),
		"dashscope":   os.Getenv("DASHSCOPE_API_KEY"),
		"perplexity":  os.Getenv("PERPLEXITY_API_KEY"),
	}

	keyCount := 0
	for _, v := range apiKeys {
		if v != "" {
			keyCount++
		}
	}
	log.Printf("API keys configured: %d/%d providers have keys", keyCount, len(apiKeys))

	benchmarker := engine.NewBenchmarker(apiKeys, 100, eventLogger)
	setupBaseURLs(benchmarker)

	cfg, err := config.LoadConfig("freellm-config.yaml")
	if err != nil {
		log.Printf("Warning: freellm-config.yaml not found, using defaults: %v", err)
		cfg = &config.Config{Port: 4000}
	}

	proxyPort := cfg.Port
	if proxyPort == 0 {
		proxyPort = 4000
	}
	if envPort := os.Getenv("FREELLM_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			proxyPort = p
		}
	}

	gateway := proxy.NewGateway(100, database, proxyPort)
	gateway.MinParamsFilter = 120 // Exclude small models <= 120B params
	if gateway.FanOutSize == 0 {
		gateway.FanOutSize = 3
	}
	if cfg != nil {
		gateway.Judge = cfg.JudgeSettings
		gateway.NumRetries = cfg.RouterSettings.NumRetries
	}
	gateway.RestoreQueue()
	if runtime.GOOS == "windows" {
		startPortForwarder()
		go tray.Run(gateway.GlobalEvents, tray.Config{})
	}

	go tokdietWatchdog(gateway)

	// Start UI server early so it can receive model updates
	uiServer := ui.NewUIServer(database, eventLogger, gateway)

	// Pull initial rankings
	log.Println("[FreeLLM] Fetching initial model rankings...")
	ctx := context.Background()
	models := benchmarker.FetchModels(ctx, database)
	filtered := benchmarker.FilterCandidates(models, database)
	gateway.UpdateModels(filtered)
	uiServer.UpdateModels(filtered)
	log.Printf("[FreeLLM] Loaded %d models\n", len(filtered))

	// Start periodic refresh
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			models := benchmarker.FetchModels(context.Background(), database)
			if len(models) > 0 {
				filtered := benchmarker.FilterCandidates(models, database)
				gateway.UpdateModels(filtered)
				uiServer.UpdateModels(filtered)
				log.Printf("[FreeLLM] Refreshed %d models\n", len(filtered))
			}
		}
	}()

	// Start HTTP server (handles proxy + dashboard)
	go func() {
		addr := fmt.Sprintf(":%d", proxyPort)
		log.Printf("[FreeLLM] Listening on %s\n", addr)
		if err := uiServer.Start(addr); err != nil {
			log.Fatalf("[FreeLLM] Server failed: %v\n", err)
		}
	}()

	// Stability metrics collector — logs qpm/tps to DB every 60s
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var qpm float64
			var totalTokens float64
			oneMinAgo := time.Now().Add(-1 * time.Minute)
			err := database.QueryRow("SELECT COUNT(*), COALESCE(SUM(prompt_tokens + completion_tokens),0) FROM usage WHERE timestamp > ?", oneMinAgo).Scan(&qpm, &totalTokens)
			if err == nil {
				tps := totalTokens / 60.0
				if qpm > 0 {
					if e := db.LogStabilityMetric(database, qpm, tps); e == nil {
						log.Printf("[METRICS] qpm=%.0f tps=%.1f\n", qpm, tps)
					}
				}
			}
		}
	}()

	// Prune old data daily
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			count, _ := db.PruneOldData(database, 30)
			log.Printf("Pruned %d old records", count)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("[FreeLLM] Shutting down...")
}

func setupBaseURLs(b *engine.Benchmarker) {
	b.BaseURLs["siliconflow"] = "https://api.siliconflow.cn/v1"
	b.BaseURLs["siliconflow_models"] = "https://api.siliconflow.cn/v1/models"
	b.BaseURLs["siliconflow_completions"] = "https://api.siliconflow.cn/v1/chat/completions"
	b.BaseURLs["together"] = "https://api.together.xyz/v1"
	b.BaseURLs["together_models"] = "https://api.together.xyz/v1/models"
	b.BaseURLs["together_completions"] = "https://api.together.xyz/v1/chat/completions"
	b.BaseURLs["novita"] = "https://api.novita.ai/v3"
	b.BaseURLs["novita_models"] = "https://api.novita.ai/v3/model"
	b.BaseURLs["novita_completions"] = "https://api.novita.ai/v3/chat/completions"
	b.BaseURLs["nebius"] = "https://api.studio.nebius.ai/v1"
	b.BaseURLs["nebius_models"] = "https://api.studio.nebius.ai/v1/models"
	b.BaseURLs["nebius_completions"] = "https://api.studio.nebius.ai/v1/chat/completions"
	b.BaseURLs["deepseek"] = "https://api.deepseek.com/v1"
	b.BaseURLs["deepseek_models"] = "https://api.deepseek.com/v1/models"
	b.BaseURLs["deepseek_completions"] = "https://api.deepseek.com/v1/chat/completions"
	b.BaseURLs["ai21"] = "https://api.ai21.com/v1"
	b.BaseURLs["ai21_models"] = "https://api.ai21.com/v1/models"
	b.BaseURLs["ai21_completions"] = "https://api.ai21.com/v1/chat/completions"
	b.BaseURLs["perplexity"] = "https://api.perplexity.ai/v1"
	b.BaseURLs["perplexity_models"] = "https://api.perplexity.ai/v1/models"
	b.BaseURLs["perplexity_completions"] = "https://api.perplexity.ai/v1/chat/completions"
}

func tokdietWatchdog(gateway *proxy.Gateway) {
	// Periodically check and restart tokdiet if it goes down.
	const tokdietPort = 7787
	const tokdietDir = "third_party/tokdiet"

	// Check if tokdiet is installed on this system
	if _, statErr := os.Stat(tokdietDir); os.IsNotExist(statErr) {
		return
	}

	go func() {
		for {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tokdietPort), 2*time.Second)
			if err == nil {
				conn.Close()
				time.Sleep(30 * time.Second)
				continue
			}

			log.Printf("[TOKDIET] Port %d not reachable, starting tokdiet...", tokdietPort)

			tokdietMu.Lock()
			if tokdietCmd != nil && tokdietCmd.Process != nil {
				tokdietCmd.Process.Kill()
				tokdietCmd.Wait()
			}

			logFile, logErr := os.OpenFile("logs/tokdiet.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if logErr != nil {
				log.Printf("[TOKDIET] Cannot open log file: %v", logErr)
				logFile = nil
			}
			tokdietLogFile = logFile

			absTokdietDir, _ := filepath.Abs(tokdietDir)
			tokdietCmd = exec.Command("node", "dist/cli.js", "start", "--port", strconv.Itoa(tokdietPort))
			tokdietCmd.Dir = absTokdietDir
			tokdietCmd.Stdout = tokdietLogFile
			tokdietCmd.Stderr = tokdietLogFile

			// Wait a moment then verify tokdiet came up
			if err := tokdietCmd.Start(); err != nil {
				log.Printf("[TOKDIET] Failed to start: %v", err)
				tokdietMu.Unlock()
				time.Sleep(15 * time.Second)
				continue
			}
			tokdietMu.Unlock()

			log.Printf("[TOKDIET] Started on port %d", tokdietPort)

			go func() {
				tokdietCmd.Wait()
				// Wait() returning doesn't mean tokdiet died; the CLI backgrounds itself.
				// Verify by checking if port is still listening.
				_, probeErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tokdietPort), 500*time.Millisecond)
				if probeErr != nil {
					log.Printf("[TOKDIET] Process exited and port %d not reachable, will restart on next check", tokdietPort)
				}
			}()

			time.Sleep(30 * time.Second)
		}
	}()
}

func startPortForwarder() {
	// Try "python" then "python3"
	pyCmd := "python"
	if _, err := exec.LookPath("python"); err != nil {
		if _, err := exec.LookPath("python3"); err == nil {
			pyCmd = "python3"
		} else {
			log.Println("[FORWARDER] Neither python nor python3 found in PATH. Cannot start port forwarder automatically.")
			return
		}
	}

	cmd := exec.Command(pyCmd, "port_forwarder.py")
	wd, err := os.Getwd()
	if err == nil {
		cmd.Dir = wd
	}

	os.MkdirAll("logs", 0755)
	logFile, err := os.OpenFile("logs/port_forwarder.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[FORWARDER] Failed to start: %v\n", err)
		return
	}
	log.Println("[FORWARDER] Started local bidirectional port forwarder in the background")
	go func() {
		cmd.Wait()
		log.Println("[FORWARDER] Process exited")
	}()
}
