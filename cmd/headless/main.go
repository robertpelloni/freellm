// Simple headless FreeLLM server — no systray, works on Linux
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/robertpelloni/freellm/internal/a2a"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
	"github.com/robertpelloni/freellm/internal/proxy"
	"github.com/robertpelloni/freellm/internal/ui"
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("FreeLLM headless starting...")

	// --- tokdiet (optional on Linux) ---
	go tryTokdiet()

	// --- database ---
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	eventLogger := engine.NewEventLogger(100, database)

	// --- API keys ---
	apiKeys := buildAPIKeyMap()
	keyCount := 0
	for _, v := range apiKeys {
		if v != "" {
			keyCount++
		}
	}
	log.Printf("API keys configured: %d/%d providers have keys", keyCount, len(apiKeys))

	benchmarker := engine.NewBenchmarker(apiKeys, 100, eventLogger)
	setBaseURLs(benchmarker)

	// --- port ---
	port := 4000
	if p := os.Getenv("FREELLM_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	// --- gateway ---
	maxActive := 50
	if p := os.Getenv("FREELLM_PARALLELISM"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			maxActive = v
		}
	}

	gateway := proxy.NewGateway(maxActive, database, port)

	// --- rankings cache ---
	rankingsCache := engine.NewRankingsCache(".")
	if cached := rankingsCache.Load(true); len(cached) > 0 {
		gateway.UpdateModels(cached)
		log.Printf("Cache-loaded %d models (top: %s, age: %v)", len(cached), cached[0].ID, rankingsCache.Age())
	}

	// --- initial model fetch (async) ---
	go func() {
		log.Println("Starting initial model discovery...")
		ctx := context.Background()
		candidates := benchmarker.FetchModels(ctx, database)
		log.Printf("Discovered %d models from all providers", len(candidates))
		if len(candidates) > 0 {
			gateway.UpdateModels(candidates)
			rankingsCache.Save(candidates)
			log.Println("Models loaded and cached")
		}
	}()

	// --- A2A ---
	a2aBaseURL := fmt.Sprintf("http://localhost:%d", port)
	a2aServer := a2a.NewA2AServer(gateway, a2aBaseURL)
	a2aSwarm := a2a.NewSwarmCoordinator(a2a.DefaultSwarmConfig(), a2aServer)
	_ = a2aSwarm
	log.Printf("[A2A-SWARM] Coordinator ready")

	// --- proxy server ---
	go func() {
		addr := fmt.Sprintf(":%d", port)
		log.Printf("Starting FreeLLM Proxy on %s", addr)
		if err := http.ListenAndServe(addr, gateway); err != nil {
			log.Printf("Proxy failed: %v", err)
		}
	}()

	// --- UI dashboard ---
	uiServer := ui.NewUIServer(database, eventLogger, gateway)
	go func() {
		log.Println("Starting Web Dashboard on :8080")
		if err := uiServer.Start(":8080"); err != nil {
			log.Printf("UI Server failed: %v", err)
		}
	}()

	// --- PID file ---
	pidFile := "/tmp/freellm.pid"
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(pidFile)

	log.Println("[FreeLLM] Ready")

	// --- model refresh loop (every 10 min) ---
	pulseInterval := 10 * time.Minute
	go func() {
		for {
			ctx := context.Background()

			currentModels := gateway.GetModels()
			if len(currentModels) > 0 && database != nil {
				ranked, changed := benchmarker.QuickPulse(ctx, currentModels, 5, database)
				if changed {
					gateway.UpdateModels(ranked)
					uiServer.UpdateModels(ranked)
					log.Println("Quick pulse: rankings changed")
				}
			}

			select {
			case <-time.After(pulseInterval):
			}
		}
	}()

	// --- ranking cache persist (every 60s) ---
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			models := gateway.GetModels()
			if len(models) > 0 {
				rankingsCache.Save(models)
			}
		}
	}()

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("[FreeLLM] Shutting down...")
}

func tryTokdiet() {
	if _, err := os.Stat("/usr/local/bin/tokdiet"); os.IsNotExist(err) {
		log.Println("[TOKDIET] tokdiet not found at /usr/local/bin/tokdiet — compression unavailable")
		return
	}
	log.Println("[TOKDIET] tokdiet binary found — manual start recommended via systemd")
}

func buildAPIKeyMap() map[string]string {
	return map[string]string{
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
		"siliconflow":  os.Getenv("SILICONFLOW_API_KEY"),
		"together":     os.Getenv("TOGETHER_API_KEY"),
		"novita":       os.Getenv("NOVITA_API_KEY"),
		"nebius":       os.Getenv("NEBIUS_API_KEY"),
		"deepseek":     os.Getenv("DEEPSEEK_API_KEY"),
		"ai21":         os.Getenv("AI21_API_KEY"),
		"replicate":    os.Getenv("REPLICATE_API_TOKEN"),
		"dashscope":    os.Getenv("DASHSCOPE_API_KEY"),
		"minimax":      os.Getenv("MINIMAX_API_KEY"),
		"moonshot":     os.Getenv("MOONSHOT_API_KEY"),
		"stepfun":      os.Getenv("STEPFUN_API_KEY"),
		"zhipu":        os.Getenv("ZHIPU_API_KEY"),
		"internlm":     os.Getenv("INTERNLM_API_KEY"),
		"arcee":        os.Getenv("ARCEE_API_KEY"),
		"perplexity":   os.Getenv("PERPLEXITY_API_KEY"),
		"xai":          os.Getenv("XAI_API_KEY"),
		"hunyuan":      os.Getenv("HUNYUAN_API_KEY"),
	}
}

func setBaseURLs(b *engine.Benchmarker) {
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
	b.BaseURLs["replicate"] = "https://api.replicate.com/v1"
	b.BaseURLs["replicate_models"] = "https://api.replicate.com/v1/models"
	b.BaseURLs["replicate_completions"] = "https://api.replicate.com/v1/chat/completions"
	b.BaseURLs["dashscope"] = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	b.BaseURLs["dashscope_models"] = "https://dashscope.aliyuncs.com/compatible-mode/v1/models"
	b.BaseURLs["dashscope_completions"] = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	b.BaseURLs["minimax"] = "https://api.minimax.chat/v1"
	b.BaseURLs["minimax_models"] = "https://api.minimax.chat/v1/models"
	b.BaseURLs["minimax_completions"] = "https://api.minimax.chat/v1/chat/completions"
	b.BaseURLs["moonshot"] = "https://api.moonshot.cn/v1"
	b.BaseURLs["moonshot_models"] = "https://api.moonshot.cn/v1/models"
	b.BaseURLs["moonshot_completions"] = "https://api.moonshot.cn/v1/chat/completions"
	b.BaseURLs["stepfun"] = "https://api.stepfun.com/v1"
	b.BaseURLs["stepfun_models"] = "https://api.stepfun.com/v1/models"
	b.BaseURLs["stepfun_completions"] = "https://api.stepfun.com/v1/chat/completions"
	b.BaseURLs["zhipu"] = "https://open.bigmodel.cn/api/paas/v4"
	b.BaseURLs["zhipu_models"] = "https://open.bigmodel.cn/api/paas/v4/models"
	b.BaseURLs["zhipu_completions"] = "https://open.bigmodel.cn/api/paas/v4/chat/completions"
	b.BaseURLs["internlm"] = "https://internlm-chat.intern-ai.org.cn/v1"
	b.BaseURLs["internlm_models"] = "https://internlm-chat.intern-ai.org.cn/v1/models"
	b.BaseURLs["internlm_completions"] = "https://internlm-chat.intern-ai.org.cn/v1/chat/completions"
}
