package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gen2brain/beeep"
	"github.com/getlantern/systray"
	"github.com/robertpelloni/freellm/internal/config"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
	"github.com/robertpelloni/freellm/internal/a2a"
	"github.com/robertpelloni/freellm/internal/icon"
	"github.com/robertpelloni/freellm/internal/proxy"
	"github.com/robertpelloni/freellm/internal/ui"
	"github.com/skratchdot/open-golang/open"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex = kernel32.NewProc("CreateMutexW")
	tokdietCmd       *exec.Cmd
	tokdietLogFile   *os.File
	tokdietMu        sync.Mutex
)

// tokdietProxyURL is the local URL the FreeLLM proxy uses to forward
// outbound requests through the tokdiet meter/compactor. Kept here so
// both startTokdiet and the watchdog/health check agree on the address.
const tokdietProxyURL = "http://127.0.0.1:7787"

func startTokdiet() {
	tokdietMu.Lock()
	defer tokdietMu.Unlock()

	// Resolve the tokdiet root relative to the freellm executable's CWD
	// (NOT relative-to-itself, since the child runs with cmd.Dir = tokdiet root).
	// We pass an ABSOLUTE cli.js path so node does not re-resolve it against
	// cmd.Dir — that previously produced the doubled "third_party/tokdiet/third_party/tokdiet/..." path
	// and tokdiet crashed immediately on startup.
	tokdietDir, err := filepath.Abs(filepath.Join("third_party", "tokdiet"))
	if err != nil {
		log.Printf("[TOKDIET] Could not resolve tokdiet directory: %v", err)
		return
	}
	tokdietPath := filepath.Join(tokdietDir, "dist", "cli.js")

	// Check if node exists
	if _, err := exec.LookPath("node"); err != nil {
		log.Printf("[TOKDIET] Error: Node.js not found in PATH. tokdiet will not start.")
		return
	}

	// Check if tokdiet build exists
	if _, err := os.Stat(tokdietPath); os.IsNotExist(err) {
		log.Printf("[TOKDIET] Error: tokdiet build not found at %s. Please run 'pnpm build' in third_party/tokdiet.", tokdietPath)
		return
	}

	// If we already have a live child, don't double-spawn.
	if tokdietCmd != nil && tokdietCmd.Process != nil && isProcessAlive(tokdietCmd.Process.Pid) {
		log.Printf("[TOKDIET] Already running (PID: %d)", tokdietCmd.Process.Pid)
		return
	}

	cmd := exec.Command("node", tokdietPath, "start", "--port", "7787")
	// Set working directory to the tokdiet folder so it can find config/pricing
	// and resolve bare-module imports against its own node_modules.
	cmd.Dir = tokdietDir

	// Capture child stdout/stderr to a log file. Without this on Windows, the
	// child's stdio handles can keep the parent waiting / trigger silent
	// termination, and we never see crash output.
	if err := os.MkdirAll("logs", 0755); err == nil {
		logPath := filepath.Join("logs", "tokdiet.log")
		// Rotate: if a previous log exists and is non-empty, stash it with
		// a timestamp suffix so the user can still inspect historical
		// output (e.g. crash spam from a prior broken build) but the
		// current run gets a clean file. Cap retained rotations at 5.
		if info, err := os.Stat(logPath); err == nil && info.Size() > 0 {
			ts := time.Now().Format("20060102-150405")
			_ = os.Rename(logPath, filepath.Join("logs", fmt.Sprintf("tokdiet.log.%s.old", ts)))
			pruneOldRotations(filepath.Join("logs"), "tokdiet.log", 5)
		}
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			tokdietLogFile = f
			cmd.Stdout = f
			cmd.Stderr = f
		} else {
			log.Printf("[TOKDIET] Could not open logs/tokdiet.log: %v", err)
		}
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[TOKDIET] Failed to start: %v", err)
		return
	}
	tokdietCmd = cmd
	log.Printf("[TOKDIET] Started successfully on port 7787 (PID: %d)", cmd.Process.Pid)

	// Reap the child asynchronously so it doesn't become a zombie, and
	// log its exit status so we know when it dies.
	go func() {
		_ = cmd.Wait()
		tokdietMu.Lock()
		stillOurs := tokdietCmd == cmd
		tokdietMu.Unlock()
		if stillOurs {
			log.Printf("[TOKDIET] Process exited unexpectedly; watchdog will restart it.")
		}
	}()
}

// waitForTokdietReady polls the tokdiet port until it accepts a TCP
// connection, up to the given timeout. Returns true on success.
func waitForTokdietReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := strings.TrimPrefix(tokdietProxyURL, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// tokdietWatchdog restarts tokdiet if it dies, until the process exits.
func tokdietWatchdog() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		tokdietMu.Lock()
		alive := tokdietCmd != nil && tokdietCmd.Process != nil && isProcessAlive(tokdietCmd.Process.Pid)
		tokdietMu.Unlock()
		if !alive {
			log.Printf("[TOKDIET] Watchdog: process not running, attempting restart...")
			startTokdiet()
			if !waitForTokdietReady(10 * time.Second) {
				log.Printf("[TOKDIET] Watchdog: restart did not bind port 7787 within 10s")
			} else {
				log.Printf("[TOKDIET] Watchdog: port 7787 is back up")
			}
		}
	}
}

// pruneOldRotations deletes the oldest `.old` siblings of `baseName` in
// `dir` until at most `keep` of them remain. Called after we rename the
// current log to a timestamped `.old` so the directory doesn't grow
// without bound across restarts.
func pruneOldRotations(dir, baseName string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := baseName + "."
	suffix := ".old"
	var rotated []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			rotated = append(rotated, n)
		}
	}
	// Sort by name (which embeds the timestamp) so the oldest is first.
	sort.Strings(rotated)
	if len(rotated) <= keep {
		return
	}
	for _, name := range rotated[:len(rotated)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// waitForProxyReady polls the FreeLLM proxy port until it accepts TCP
// connections, up to the given timeout.
func waitForProxyReady(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// acquireMutex tries to create a Windows named mutex. Returns true if we are the
// first instance. If the mutex already exists (ERROR_ALREADY_EXISTS) it returns
// false, meaning another FreeLLM is already running.
func acquireMutex(name string) (syscall.Handle, bool) {
	h, _, err := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(name))))
	if h == 0 {
		log.Printf("CreateMutex failed: %v", err)
		return 0, false
	}
	// ERROR_ALREADY_EXISTS = 183
	const ERROR_ALREADY_EXISTS = 183
	lastErr, _, _ := kernel32.NewProc("GetLastError").Call()
	if lastErr == ERROR_ALREADY_EXISTS {
		return syscall.Handle(h), false
	}
	return syscall.Handle(h), true
}

// isProcessAlive checks if a PID is actually running on Windows.
func isProcessAlive(pid int) bool {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil || h == 0 {
		return false
	}
	var exitCode uint32
	kernel32.NewProc("GetExitCodeProcess").Call(uintptr(h), uintptr(unsafe.Pointer(&exitCode)))
	syscall.CloseHandle(h)
	return exitCode == 259 // STILL_ACTIVE
}

func showNotification(title, message string) {
	beeep.Notify(title, message, "")
}

// menuAction represents a click event from any menu item
type menuAction struct {
	id   string
	data string
}

// menuEventBus collects all menu clicks into a single channel
var menuEventBus = make(chan menuAction, 256)

func click(id, data string) {
	select {
	case menuEventBus <- menuAction{id: id, data: data}:
	default:
		log.Printf("menuEventBus full, dropping action %s", id)
	}
}

// watchMenuItem launches a goroutine that sends a menu action when the item is clicked
func watchMenuItem(item *systray.MenuItem, id, data string) {
	go func() {
		for range item.ClickedCh {
			click(id, data)
		}
	}()
}

func loadEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		// Remove quotes if present
		value = strings.Trim(value, `"'`)
		
		// Fix doubled keys (common copy-paste error)
		if len(value) > 40 && strings.HasPrefix(value, "sk-") {
			half := len(value) / 2
			if value[:half] == value[half:] {
				value = value[:half]
			}
		}

		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

func main() {
	loadEnv(".env")
	// Use a Windows named mutex for single-instance enforcement.
	// Unlike lockfiles, the mutex is automatically released if the process crashes.
	const mutexName = "Global\\FreeLLM_SingleInstance"
	mutex, ok := acquireMutex(mutexName)
	if !ok {
		// Another instance is running — try to bring it to focus or just exit
		log.Println("Another FreeLLM instance is already running. Exiting.")
		return
	}
	defer syscall.CloseHandle(mutex)

	// Also write PID file for the watchdog
	pidFile := filepath.Join(os.TempDir(), "freellm.pid")
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(pidFile)

	systray.Run(onReady, onExit)
}

func onReady() {
	// ============================================================
	//  Initialize Core Services
	// ============================================================

	systray.SetIcon(icon.Gray)
	systray.SetTitle("FreeLLM")
	systray.SetTooltip("FreeLLM - Starting...")

	startTokdiet()
	// Block startup briefly so the FreeLLM proxy never races against a
	// not-yet-bound tokdiet port — the RoundTripper will dial 127.0.0.1:7787
	// on the first request, and we don't want every early call to fail
	// because tokdiet was still starting.
	if waitForTokdietReady(15 * time.Second) {
		log.Printf("[TOKDIET] Port 7787 is accepting connections")
	} else {
		log.Printf("[TOKDIET] Port 7787 not yet ready after 15s; watchdog will keep trying")
	}
	go tokdietWatchdog()

	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

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
		"nvidia_nim": os.Getenv("NVIDIA_API_KEY"),
		"siliconflow": os.Getenv("SILICONFLOW_API_KEY"),
		"together": os.Getenv("TOGETHER_API_KEY"),
		"novita": os.Getenv("NOVITA_API_KEY"),
		"nebius": os.Getenv("NEBIUS_API_KEY"),
		"deepseek": os.Getenv("DEEPSEEK_API_KEY"),
		"ai21": os.Getenv("AI21_API_KEY"),
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

	keyCount := 0
	for _, v := range apiKeys {
		if v != "" {
			keyCount++
		}
	}
	log.Printf("API keys configured: %d/%d providers have keys", keyCount, len(apiKeys))

	benchmarker := engine.NewBenchmarker(apiKeys, 100, eventLogger)
	// Hardcoded provider BaseURLs for benchmarking (config can override)
	benchmarker.BaseURLs["siliconflow"] = "https://api.siliconflow.cn/v1"
	benchmarker.BaseURLs["siliconflow_models"] = "https://api.siliconflow.cn/v1/models"
	benchmarker.BaseURLs["siliconflow_completions"] = "https://api.siliconflow.cn/v1/chat/completions"
	benchmarker.BaseURLs["together"] = "https://api.together.xyz/v1"
	benchmarker.BaseURLs["together_models"] = "https://api.together.xyz/v1/models"
	benchmarker.BaseURLs["together_completions"] = "https://api.together.xyz/v1/chat/completions"
	benchmarker.BaseURLs["novita"] = "https://api.novita.ai/v3"
	benchmarker.BaseURLs["novita_models"] = "https://api.novita.ai/v3/model"
	benchmarker.BaseURLs["novita_completions"] = "https://api.novita.ai/v3/chat/completions"
	benchmarker.BaseURLs["nebius"] = "https://api.studio.nebius.ai/v1"
	benchmarker.BaseURLs["nebius_models"] = "https://api.studio.nebius.ai/v1/models"
	benchmarker.BaseURLs["nebius_completions"] = "https://api.studio.nebius.ai/v1/chat/completions"
	benchmarker.BaseURLs["deepseek"] = "https://api.deepseek.com/v1"
	benchmarker.BaseURLs["deepseek_models"] = "https://api.deepseek.com/v1/models"
	benchmarker.BaseURLs["deepseek_completions"] = "https://api.deepseek.com/v1/chat/completions"
	benchmarker.BaseURLs["ai21"] = "https://api.ai21.com/v1"
	benchmarker.BaseURLs["ai21_models"] = "https://api.ai21.com/v1/models"
	benchmarker.BaseURLs["ai21_completions"] = "https://api.ai21.com/v1/chat/completions"
	benchmarker.BaseURLs["replicate"] = "https://api.replicate.com/v1"
	benchmarker.BaseURLs["replicate_models"] = "https://api.replicate.com/v1/models"
	benchmarker.BaseURLs["replicate_completions"] = "https://api.replicate.com/v1/chat/completions"
	benchmarker.BaseURLs["dashscope"] = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	benchmarker.BaseURLs["dashscope_models"] = "https://dashscope.aliyuncs.com/compatible-mode/v1/models"
	benchmarker.BaseURLs["dashscope_completions"] = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	benchmarker.BaseURLs["minimax"] = "https://api.minimax.chat/v1"
	benchmarker.BaseURLs["minimax_models"] = "https://api.minimax.chat/v1/models"
	benchmarker.BaseURLs["minimax_completions"] = "https://api.minimax.chat/v1/chat/completions"
	benchmarker.BaseURLs["moonshot"] = "https://api.moonshot.cn/v1"
	benchmarker.BaseURLs["moonshot_models"] = "https://api.moonshot.cn/v1/models"
	benchmarker.BaseURLs["moonshot_completions"] = "https://api.moonshot.cn/v1/chat/completions"
	benchmarker.BaseURLs["stepfun"] = "https://api.stepfun.com/v1"
	benchmarker.BaseURLs["stepfun_models"] = "https://api.stepfun.com/v1/models"
	benchmarker.BaseURLs["stepfun_completions"] = "https://api.stepfun.com/v1/chat/completions"
	benchmarker.BaseURLs["zhipu"] = "https://open.bigmodel.cn/api/paas/v4"
	benchmarker.BaseURLs["zhipu_models"] = "https://open.bigmodel.cn/api/paas/v4/models"
	benchmarker.BaseURLs["zhipu_completions"] = "https://open.bigmodel.cn/api/paas/v4/chat/completions"
	benchmarker.BaseURLs["internlm"] = "https://internlm-chat.intern-ai.org.cn/v1"
	benchmarker.BaseURLs["internlm_models"] = "https://internlm-chat.intern-ai.org.cn/v1/models"
	benchmarker.BaseURLs["internlm_completions"] = "https://internlm-chat.intern-ai.org.cn/v1/chat/completions"
	benchmarker.BaseURLs["arcee"] = "https://api.arcee.ai/v1"
	benchmarker.BaseURLs["arcee_models"] = "https://api.arcee.ai/v1/models"
	benchmarker.BaseURLs["arcee_completions"] = "https://api.arcee.ai/v1/chat/completions"
	benchmarker.BaseURLs["perplexity"] = "https://api.perplexity.ai/v1"
	benchmarker.BaseURLs["perplexity_models"] = "https://api.perplexity.ai/v1/models"
	benchmarker.BaseURLs["perplexity_completions"] = "https://api.perplexity.ai/v1/chat/completions"
	benchmarker.BaseURLs["xai"] = "https://api.x.ai/v1"
	benchmarker.BaseURLs["xai_models"] = "https://api.x.ai/v1/models"
	benchmarker.BaseURLs["xai_completions"] = "https://api.x.ai/v1/chat/completions"
	benchmarker.BaseURLs["hunyuan"] = "https://api.hunyuan.cloud.tencent.com/v1"
	benchmarker.BaseURLs["hunyuan_models"] = "https://api.hunyuan.cloud.tencent.com/v1/models"
	benchmarker.BaseURLs["hunyuan_completions"] = "https://api.hunyuan.cloud.tencent.com/v1/chat/completions"

	cfgPath := "freellm-config.yaml"
	cfg, err := config.LoadConfig(cfgPath)
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

	gateway := proxy.NewGateway(100, database, 4000)
    // Default exclusions and fan‑out settings
    // Exclude any model with ≤130 billion parameters unless overridden via config
    if gateway.MinParamsFilter == 0 {
        gateway.MinParamsFilter = 120 // 120 billion parameters
    }
    // Default fan‑out size – the UI can change this at runtime via the context menu
    if gateway.FanOutSize == 0 {
        gateway.FanOutSize = 1
    }
	gateway.RestoreQueue()

	// Apply settings from config
	applyConfigToGateway := func(c *config.Config, g *proxy.Gateway) {
		if c.RouterSettings.MinParamsFilter > 0 {
			g.MinParamsFilter = c.RouterSettings.MinParamsFilter
		}
		if c.ProxySettings.RequestTimeout > 0 { g.RequestTimeout = time.Duration(c.ProxySettings.RequestTimeout) * time.Second }
		if c.ProxySettings.StreamTimeout > 0 { g.StreamTimeout = time.Duration(c.ProxySettings.StreamTimeout) * time.Second }
		if c.ProxySettings.ConnectTimeout > 0 { g.ConnectTimeout = time.Duration(c.ProxySettings.ConnectTimeout) * time.Second }
		if c.ProxySettings.WatchdogTimeout > 0 { g.WatchdogTimeout = time.Duration(c.ProxySettings.WatchdogTimeout) * time.Second }
		if c.ProxySettings.ProvenWatchdogTimeout > 0 { g.ProvenWatchdogTimeout = time.Duration(c.ProxySettings.ProvenWatchdogTimeout) * time.Second }
		if c.ProxySettings.ReasoningWatchdogTimeout > 0 { g.ReasoningWatchdogTimeout = time.Duration(c.ProxySettings.ReasoningWatchdogTimeout) * time.Second }
		if c.ProxySettings.LockDuration > 0 { g.LockDuration = time.Duration(c.ProxySettings.LockDuration) * time.Second }
		if c.ProxySettings.SmartSwitchDelay > 0 { g.SmartSwitchDelay = time.Duration(c.ProxySettings.SmartSwitchDelay) * time.Millisecond }
		if c.ProxySettings.FanOutSize > 0 { g.FanOutSize = c.ProxySettings.FanOutSize }
	}

	applyConfigToGateway(cfg, gateway)

	config.WatchConfig(cfgPath, func(newCfg *config.Config) {
		log.Println("Applying new configuration...")
		cfg = newCfg
		applyConfigToGateway(newCfg, gateway)
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

	// Initialize A2A protocol server for agent-to-agent communication
	a2aBaseURL := fmt.Sprintf("http://localhost:%d", proxyPort)
	a2aServer := a2a.NewA2AServer(gateway, a2aBaseURL)
	gateway.A2A = a2aServer
	log.Printf("[A2A] Agent-to-Agent protocol server initialized at %s/a2a", a2aBaseURL)

	// Initialize Swarm Coordinator
	swarm := a2a.NewSwarmCoordinator(a2a.DefaultSwarmConfig(), a2aServer)
	_ = swarm // Available for API-driven agent registration
	log.Printf("[A2A-SWARM] Coordinator ready (max %d concurrent agents)", a2a.DefaultSwarmConfig().MaxConcurrentAgents)


	// Load cached rankings from disk for instant startup
	rankingsCache := engine.NewRankingsCache(".")
	if cached := rankingsCache.Load(true); len(cached) > 0 {
		gateway.UpdateModels(cached)
		log.Printf("Cache-loaded %d models (top: %s, age: %v)", len(cached), cached[0].ID, rankingsCache.Age())
	}
	go func() {
		addr := fmt.Sprintf(":%d", proxyPort)
		log.Printf("Starting FreeLLM Proxy on %s", addr)
		if err := http.ListenAndServe(addr, gateway); err != nil {
			log.Printf("Proxy failed: %v", err)
		}
	}()

	uiServer := ui.NewUIServer(database, eventLogger, gateway)
	go func() {
		log.Println("Starting Web Dashboard on :8080")
		if err := uiServer.Start(":8080"); err != nil {
			log.Printf("UI Server failed: %v", err)
		}
	}()

	// ============================================================
	//  State
	// ============================================================

	routingEnabled := true
	autoPilot := true
	refreshTrigger := make(chan bool, 1)
	pulseInterval := 10 * time.Minute
	menuBuilt := false
	primarySlots := make([]*systray.MenuItem, 15)
	fallbackSlots := make([]*systray.MenuItem, 50)
	primarySlotIDs := make([]string, 15)
	fallbackSlotIDs := make([]string, 50)

	// ============================================================
	//  Build Static Menu (matches Python version layout)
	//  Model submenus are flat (2 levels max) to work on Windows.
	//  Each model is listed as a clickable item under its group.
	//  Clicking a model = Set as Primary.
	//  Skip/Blacklist are separate items per model.
	// ============================================================

	// --- Top ---
	mRouting := systray.AddMenuItemCheckbox("Master Routing", "Enable request routing", true)
	watchMenuItem(mRouting, "toggle_routing", "")

	mParallel := systray.AddMenuItem("Parallel Fan-out", "Number of parallel requests")
	mParallel1 := mParallel.AddSubMenuItemCheckbox("1 Model", "", false)
	mParallel2 := mParallel.AddSubMenuItemCheckbox("2 Models", "", false)
	mParallel3 := mParallel.AddSubMenuItemCheckbox("3 Models", "", true)
	mParallel4 := mParallel.AddSubMenuItemCheckbox("4 Models", "", false)
	mParallel5 := mParallel.AddSubMenuItemCheckbox("5 Models", "", false)

	watchMenuItem(mParallel1, "set_parallel", "1")
	watchMenuItem(mParallel2, "set_parallel", "2")
	watchMenuItem(mParallel3, "set_parallel", "3")
	watchMenuItem(mParallel4, "set_parallel", "4")
	watchMenuItem(mParallel5, "set_parallel", "5")

	mCopyModel := systray.AddMenuItem("Copy Active Model", "Copy primary model ID to clipboard")
	watchMenuItem(mCopyModel, "copy_active_model", "")

	systray.AddSeparator()

	// --- Status ---
	mStatus := systray.AddMenuItem("FreeLLM: Starting | Primary: None", "Current status")
	mStatus.Disable()

	systray.AddSeparator()

	// --- Primary Actions ---
	mOpen := systray.AddMenuItem("Open LLM Interface", "Open the LLM chat in browser")
	watchMenuItem(mOpen, "open_interface", "")

	mSettings := systray.AddMenuItem("Settings", "Open settings")
	watchMenuItem(mSettings, "open_settings", "")

	systray.AddSeparator()

	// --- UI Windows ---
	mQuickQuery := systray.AddMenuItem("Quick Query", "Send a quick query to the LLM")
	watchMenuItem(mQuickQuery, "open_quick_query", "")

	mModelComparison := systray.AddMenuItem("Model Comparison", "Compare model responses side by side")
	watchMenuItem(mModelComparison, "open_comparison", "")

	mDashboard := systray.AddMenuItem("Show Dashboard", "Open monitoring dashboard")
	watchMenuItem(mDashboard, "open_dashboard", "")

	mLeaderboard := systray.AddMenuItem("Model Leaderboard", "View model rankings")
	watchMenuItem(mLeaderboard, "open_leaderboard", "")

	mSavings := systray.AddMenuItem("Cost Savings", "View cost savings report")
	watchMenuItem(mSavings, "open_savings", "")

	mMonitoring := systray.AddMenuItem("Monitoring Dashboard", "Real-time monitoring")
	watchMenuItem(mMonitoring, "open_monitoring", "")

	mProtocol := systray.AddMenuItem("Protocol Oversight", "View protocol compliance")
	watchMenuItem(mProtocol, "open_protocol", "")

	mExecution := systray.AddMenuItem("Execution Dashboard", "View execution metrics")
	watchMenuItem(mExecution, "open_execution", "")

	mSystemStatus := systray.AddMenuItem("System Status", "View system health")
	watchMenuItem(mSystemStatus, "open_status", "")

	systray.AddSeparator()

	// --- ★ Primary Models Submenu (flat, populated dynamically) ---
	mPrimaryGroup := systray.AddMenuItem("★ Primary (0)", "Primary model group — click model to set as #1")

	// --- Fallback Models Submenu (flat, populated dynamically) ---
	mFallbackGroup := systray.AddMenuItem("  Fallback (0)", "Fallback model group")

	// Pre-create primary model slots (up to 10)
	for i := 0; i < 10; i++ {
		slot := mPrimaryGroup.AddSubMenuItem("--", "")
		primarySlots[i] = slot
		watchMenuItem(slot, "model_set_primary_slot", fmt.Sprintf("%d", i))
	}

	// Pre-create fallback model slots (up to 50)
	for i := 0; i < 50; i++ {
		slot := mFallbackGroup.AddSubMenuItem("--", "")
		fallbackSlots[i] = slot
		watchMenuItem(slot, "model_set_fallback_slot", fmt.Sprintf("%d", i))
	}

	systray.AddSeparator()

	// --- Auto-Pilot & Refresh ---
	mAutoPilot := systray.AddMenuItemCheckbox("Auto-Pilot Mode", "Automatically benchmark and route", true)
	watchMenuItem(mAutoPilot, "toggle_autopilot", "")

	mRefreshNow := systray.AddMenuItem("Refresh Now", "Force a model refresh now")
	watchMenuItem(mRefreshNow, "refresh_now", "")

	systray.AddSeparator()

	// --- Enable Providers Submenu (populated dynamically) ---
	mProviders := systray.AddMenuItem("Enable Providers", "Toggle provider on/off")

	// --- Documentation ---
	mDocs := systray.AddMenuItem("Documentation", "Open FreeLLM documentation")
	watchMenuItem(mDocs, "open_docs", "")

	// --- Start with Windows ---
	mStartup := systray.AddMenuItem("Start with Windows", "Launch on system startup")
	mStartupEnable := mStartup.AddSubMenuItem("Enable", "Add to Windows startup")
	watchMenuItem(mStartupEnable, "startup_enable", "")
	mStartupDisable := mStartup.AddSubMenuItem("Disable", "Remove from Windows startup")
	watchMenuItem(mStartupDisable, "startup_disable", "")

	// --- Maintenance ---
	mMaintenance := systray.AddMenuItem("Maintenance", "System maintenance options")
	mMaintClearSkips := mMaintenance.AddSubMenuItem("Clear Skip List", "Clear all manual model skips")
	watchMenuItem(mMaintClearSkips, "maint_clear_skips", "")
	mMaintClearBlacklist := mMaintenance.AddSubMenuItem("Clear Blacklist", "Remove all blacklisted models")
	watchMenuItem(mMaintClearBlacklist, "maint_clear_blacklist", "")
	mMaintResetStats := mMaintenance.AddSubMenuItem("Reset Provider Stats", "Reset all provider and model statistics")
	watchMenuItem(mMaintResetStats, "maint_reset_stats", "")
	mMaintCleanupProbes := mMaintenance.AddSubMenuItem("Cleanup Old Probes (>90d)", "Delete probe history older than 90 days")
	watchMenuItem(mMaintCleanupProbes, "maint_cleanup_probes", "")
	mMaintBackupConfig := mMaintenance.AddSubMenuItem("Backup FreeLLM Config", "Save current config to .bak")
	watchMenuItem(mMaintBackupConfig, "maint_backup_config", "")
	mMaintRestoreConfig := mMaintenance.AddSubMenuItem("Restore FreeLLM Config", "Restore config from .bak backup")
	watchMenuItem(mMaintRestoreConfig, "maint_restore_config", "")

	systray.AddSeparator()

	// --- FreeLLM Control ---
	mControl := systray.AddMenuItem("FreeLLM Control", "Proxy control options")
	mControlRefresh := mControl.AddSubMenuItem("Refresh Models", "Re-discover and benchmark all models")
	watchMenuItem(mControlRefresh, "control_refresh", "")
	mControlViewLogs := mControl.AddSubMenuItem("View Proxy Logs", "Open log viewer")
	watchMenuItem(mControlViewLogs, "control_view_logs", "")
	mControlViewEngineLogs := mControl.AddSubMenuItem("View Engine Logs", "View engine/benchmark logs")
	watchMenuItem(mControlViewEngineLogs, "control_view_engine_logs", "")
	mControlViewConfig := mControl.AddSubMenuItem("View Config", "Open config editor")
	watchMenuItem(mControlViewConfig, "control_view_config", "")

	systray.AddSeparator()

	// --- Quit ---
	mQuit := systray.AddMenuItem("Quit", "Quit FreeLLM")
	watchMenuItem(mQuit, "quit", "")

	// ============================================================
	//  Build Dynamic Model Items (FLAT — max 2 nesting levels)
	//
	//  Structure per model (under ★ Primary / Fallback submenu):
	//    ★ model-name (provider) 2.1s score=85    [click = Set as Primary]
	//       ↳ Skip (24h)                          [click = skip 24h]
	//       ↳ Blacklist                            [click = blacklist]
	//       ↳ ↓ Demote to Fallback  (primary only)
	//       ↳ ↑ Promote to Primary   (fallback only)
	//       ↳ ↑ Move Up              (not first)
	//       ↳ ↓ Move Down            (not last)
	//
	//  This keeps it at exactly 2 levels: Group → Item + Actions
	//  Windows systray renders this correctly.
	// ============================================================

	rebuildDynamicMenu := func() {
		models := gateway.GetModels()
		if len(models) == 0 {
			return
		}

		// Ensure last-used model appears in the primary list
		lastModel, lastProvider := gateway.GetLastUsed()
		if lastModel != "" {
			// Check if last-used model is already in top 10
			found := false
			for i, m := range models {
				if i >= 10 { break }
				if m.ID == lastModel && m.Provider == lastProvider {
					found = true
					break
				}
			}
			if !found {
				// Find the model in the full list and move it to front
				for i, m := range models {
					if m.ID == lastModel && m.Provider == lastProvider {
						models[0], models[i] = models[i], models[0]
						break
					}
				}
			}
		}

		pCount := gateway.PrimaryCount
		if pCount > 15 { pCount = 15 }
		if len(models) < pCount { pCount = len(models) }
		maxModels := 65
		if len(models) < maxModels { maxModels = len(models) }
		fCount := maxModels - pCount
		if fCount > 50 { fCount = 50 }
		if fCount < 0 { fCount = 0 }

		mPrimaryGroup.SetTitle(fmt.Sprintf("\u2605 Primary (%d)", pCount))
		mFallbackGroup.SetTitle(fmt.Sprintf("  Fallback (%d)", fCount))

		// Update primary slots
		for i := 0; i < 10; i++ {
			if i < pCount && i < len(models) {
				m := models[i]
				latStr := "?"
				if m.Latency > 0 { latStr = fmt.Sprintf("%.2fs", m.Latency) }
				scoreStr := ""
				if m.Score > 0 { scoreStr = fmt.Sprintf("score=%.0f", m.Score) }
				primarySlots[i].SetTitle(fmt.Sprintf("\u2605 %s (%s) %s %s", m.ID, m.Provider, latStr, scoreStr))
				primarySlotIDs[i] = m.ID
				primarySlots[i].Enable()
			} else {
				primarySlots[i].SetTitle("---")
				primarySlotIDs[i] = ""
				primarySlots[i].Disable()
			}
		}

		// Update fallback slots
		for i := 0; i < 50; i++ {
			mi := pCount + i
			if i < fCount && mi < len(models) {
				m := models[mi]
				latStr := "?"
				if m.Latency > 0 { latStr = fmt.Sprintf("%.2fs", m.Latency) }
				scoreStr := ""
				if m.Score > 0 { scoreStr = fmt.Sprintf("score=%.0f", m.Score) }
				fallbackSlots[i].SetTitle(fmt.Sprintf("  %s (%s) %s %s", m.ID, m.Provider, latStr, scoreStr))
				fallbackSlotIDs[i] = m.ID
				fallbackSlots[i].Enable()
			} else {
				fallbackSlots[i].SetTitle("---")
				fallbackSlotIDs[i] = ""
				fallbackSlots[i].Disable()
			}
		}

		// Build provider toggles once
		if !menuBuilt {
			providerHealth, _ := db.GetProviderHealth(database)
			for _, ph := range providerHealth {
				cb := mProviders.AddSubMenuItemCheckbox(ph.Name, fmt.Sprintf("Toggle %s", ph.Name), ph.Enabled)
				watchMenuItem(cb, "toggle_provider", ph.Name)
			}
			menuBuilt = true
		}
	}

	// ============================================================
	//  Tray Status Update
	// ============================================================

	updateTrayStatus := func() {
		models := gateway.GetModels()
		lastModel, lastProvider := gateway.GetLastUsed()
		if len(models) == 0 {
			systray.SetIcon(icon.Gray)
			systray.SetTooltip("FreeLLM - No models available")
			mStatus.SetTitle("FreeLLM: Offline | Primary: None")
			return
		}
		top := models[0]
		lat := top.Latency
		if lat < 0.5 {
			systray.SetIcon(icon.Green)
		} else if lat < 1.5 {
			systray.SetIcon(icon.Yellow)
		} else {
			systray.SetIcon(icon.Red)
		}
		topLatStr := "?"
		if lat > 0 { topLatStr = fmt.Sprintf("%.2fs", lat) }
		// Show last-used model in tray title (what was actually routed)
		if lastModel != "" {
			systray.SetTitle(fmt.Sprintf("FreeLLM: %s", lastModel))
			systray.SetTooltip(fmt.Sprintf("FreeLLM - Last: %s (%s) | Top: %s (%s)", lastModel, lastProvider, top.ID, topLatStr))
			mStatus.SetTitle(fmt.Sprintf("FreeLLM: Live | Last: %s (%s) | Top: %s | %d models", lastModel, lastProvider, top.ID, len(models)))
		} else {
			systray.SetTitle(fmt.Sprintf("FreeLLM: %s", top.ID))
			systray.SetTooltip(fmt.Sprintf("FreeLLM - Primary: %s (%s)", top.ID, topLatStr))
			mStatus.SetTitle(fmt.Sprintf("FreeLLM: Live | Primary: %s (%s) | %d models", top.ID, topLatStr, len(models)))
		}
	}

	// ============================================================
	//  Startup smoke test
	// ============================================================
	// Once the proxy and UI are up, fire a minimal request through the full
	// chain (client → FreeLLM → tokdiet → upstream). The point is NOT to
	// verify the upstream call succeeds — only that the plumbing is wired.
	// Any HTTP response (even a 4xx from the provider) proves the proxy can
	// reach tokdiet, which is the actual failure mode we want to surface.
	// On total failure, flip the tray icon red so the user sees the
	// breakage instead of a green "Live" tooltip.
	runStartupSmokeTest := func() {
		if !waitForProxyReady(proxyPort, 10*time.Second) {
			log.Printf("[SMOKE] Proxy never came up on :%d", proxyPort)
			systray.SetIcon(icon.Red)
			systray.SetTooltip(fmt.Sprintf("FreeLLM - proxy failed to bind :%d", proxyPort))
			mStatus.SetTitle(fmt.Sprintf("FreeLLM: Proxy down on :%d", proxyPort))
			return
		}

		models := gateway.GetModels()
		if len(models) == 0 {
			log.Printf("[SMOKE] No cached models yet, skipping end-to-end probe (will retry on first real request)")
			return
		}
		top := models[0]

		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"stream":false}`, top.ID)
		req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", proxyPort), strings.NewReader(body))
		if err != nil {
			log.Printf("[SMOKE] build request failed: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[SMOKE] FAILED: chain is broken: %v", err)
			systray.SetIcon(icon.Red)
			systray.SetTooltip(fmt.Sprintf("FreeLLM - smoke test failed: %v", err))
			mStatus.SetTitle(fmt.Sprintf("FreeLLM: Smoke test FAILED (%v)", err))
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		log.Printf("[SMOKE] OK: chain alive (HTTP %d via %s/%s)", resp.StatusCode, top.ID, top.Provider)
	}
	go runStartupSmokeTest()

	// ============================================================
	//  Background Workers
	// ============================================================

	go func() {
		for {
			ctx := context.Background()
			
			// Auto-pulse routine
			if routingEnabled {
				currentModels := gateway.GetModels()
				if len(currentModels) > 0 {
					ranked, changed := benchmarker.QuickPulse(ctx, currentModels, 5, database)
					if changed {
						gateway.UpdateModels(ranked)
						uiServer.UpdateModels(ranked)
						log.Println("Quick pulse: rankings changed")
					}
				}
			}
			updateTrayStatus()
			rebuildDynamicMenu()

			select {
			case <-refreshTrigger:
			case <-time.After(pulseInterval):
			}
		}
	}()

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

	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			count, _ := db.PruneOldData(database, 30)
			log.Printf("Pruned %d old records", count)
		}
	}()

	go func() {
		failCount := 0
		startupGrace := time.Now().Add(60 * time.Second)
		for {
			time.Sleep(1 * time.Minute)
			models := gateway.GetModels()
			if len(models) == 0 {
				continue
			}

			// Check top 3 models, not just #1
			topCheck := 3
			if len(models) < topCheck {
				topCheck = len(models)
			}
			allHealthy := false
			var lastErr error
			for i := 0; i < topCheck; i++ {
				m := models[i]
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, err := benchmarker.MeasureLatency(ctx, m.ID, m.Provider)
				cancel()
				if err == nil {
					allHealthy = true
					break
				}
				lastErr = err
			}

			if !allHealthy {
				if time.Now().Before(startupGrace) {
					continue
				}
				failCount++
				top := models[0]
				log.Printf("Health check failed for top %d models (%d/3): %v", topCheck, failCount, lastErr)
				db.LogActivity(database, "Health Check Failure", top.ID, fmt.Sprintf("Attempt %d/3 failed", failCount))
				if failCount == 1 {
					showNotification("Health Alert", fmt.Sprintf("Health check failed for %s (%d/3)", top.ID, failCount))
				}
				systray.SetIcon(icon.Red)
			} else {
				failCount = 0
			}

			if failCount >= 3 {
				log.Println("Proactive health threshold reached. Triggering refresh...")
				db.LogActivity(database, "Fallback Triggered", models[0].ID, "Consecutive health failures")
				select {
				case refreshTrigger <- true:
				default:
				}
				failCount = 0
			}
		}
	}()

	// Deferred initial menu build — retry until models are loaded
	go func() {
		for i := 0; i < 60; i++ {
			time.Sleep(10 * time.Second)
			if menuBuilt {
				return
			}
			models := gateway.GetModels()
			if len(models) > 0 {
				rebuildDynamicMenu()
				return
			}
		}
	}()

	// ============================================================
	//  Central Menu Event Handler
	// ============================================================

	go func() {
		for action := range menuEventBus {
			switch action.id {

			// --- Top Section ---
			case "set_parallel":
				n, _ := strconv.Atoi(action.data)
				gateway.FanOutSize = n
				log.Printf("Parallel Fan-out set to %d", n)
				
				// Uncheck all first
				mParallel1.Uncheck()
				mParallel2.Uncheck()
				mParallel3.Uncheck()
				mParallel4.Uncheck()
				mParallel5.Uncheck()
				
				// Check the selected one
				switch n {
				case 1: mParallel1.Check()
				case 2: mParallel2.Check()
				case 3: mParallel3.Check()
				case 4: mParallel4.Check()
				case 5: mParallel5.Check()
				}
				showNotification("FreeLLM", fmt.Sprintf("Parallel Fan-out: %d models", n))

			case "toggle_routing":
				routingEnabled = !routingEnabled
				log.Printf("Master Routing: %v", routingEnabled)

			case "copy_active_model":
				models := gateway.GetModels()
				if len(models) > 0 {
					modelID := models[0].ID
					exec.Command("powershell", "-Command",
						fmt.Sprintf("Set-Clipboard -Value '%s'", modelID)).Run()
					showNotification("FreeLLM", fmt.Sprintf("Copied: %s", modelID))
				}

			// --- Primary Actions ---
			case "open_interface":
				open.Run(fmt.Sprintf("http://localhost:%d", proxyPort))

			case "open_settings":
				open.Run("http://localhost:8080#config-tab")

			// --- UI Windows ---
			case "open_quick_query":
				open.Run(fmt.Sprintf("http://localhost:%d", proxyPort))

			case "open_comparison":
				open.Run("http://localhost:8080#comparison-tab")

			case "open_dashboard":
				open.Run("http://localhost:8080")

			case "open_leaderboard":
				open.Run("http://localhost:8080#rankings-tab")

			case "open_savings":
				open.Run("http://localhost:8080#savings-tab")

			case "open_monitoring":
				open.Run("http://localhost:8080#monitoring-tab")

			case "open_protocol":
				open.Run("http://localhost:8080#protocol-tab")

			case "open_execution":
				open.Run("http://localhost:8080#execution-tab")

			case "open_status":
				open.Run("http://localhost:8080#status-tab")

			// --- Auto-Pilot & Refresh ---
			case "toggle_autopilot":
				autoPilot = !autoPilot
				log.Printf("Auto-Pilot: %v", autoPilot)

			case "refresh_now":
				log.Println("Refreshing models...")
				systray.SetIcon(icon.Yellow)
				mStatus.SetTitle("FreeLLM: Refreshing...")
				select {
				case refreshTrigger <- true:
				default:
				}

			// --- Documentation ---
			case "open_docs":
				open.Run("https://docs.freellm.ai/")

			// --- Startup ---
			case "startup_enable":
				config.SetStartWithWindows(true)
				showNotification("FreeLLM", "Start with Windows enabled")

			case "startup_disable":
				config.SetStartWithWindows(false)
				showNotification("FreeLLM", "Start with Windows disabled")

			// --- Maintenance ---
			case "maint_clear_skips":
				db.ClearSkips(database)
				showNotification("FreeLLM", "Skip list cleared")

			case "maint_clear_blacklist":
				db.ClearBlacklist(database)
				showNotification("FreeLLM", "Blacklist cleared")

			case "maint_reset_stats":
				db.ResetStats(database)
				showNotification("FreeLLM", "All provider and model stats reset")

			case "maint_cleanup_probes":
				count, err := db.PruneOldData(database, 90)
				if err == nil {
					showNotification("FreeLLM", fmt.Sprintf("Cleaned up %d old probe records", count))
				}

			case "maint_backup_config":
				data, err := os.ReadFile(cfgPath)
				if err == nil {
					os.WriteFile(cfgPath+".bak", data, 0644)
					showNotification("FreeLLM", "Config backed up to "+cfgPath+".bak")
				}

			case "maint_restore_config":
				data, err := os.ReadFile(cfgPath + ".bak")
				if err == nil {
					os.WriteFile(cfgPath, data, 0644)
					showNotification("FreeLLM", "Config restored from backup")
					if newCfg, err := config.LoadConfig(cfgPath); err == nil {
						cfg = newCfg
					}
				} else {
					showNotification("FreeLLM", "No backup config found")
				}

			// --- FreeLLM Control ---
			case "control_refresh":
				systray.SetIcon(icon.Yellow)
				mStatus.SetTitle("FreeLLM: Refreshing...")
				select {
				case refreshTrigger <- true:
				default:
				}

			case "control_view_logs":
				open.Run("http://localhost:8080#logs-tab")

			case "control_view_engine_logs":
				open.Run("http://localhost:8080#engine-logs-tab")

			case "control_view_config":
				open.Run("http://localhost:8080#config-tab")

			// --- Model Actions ---
			case "model_set_primary_slot":
				slotIdx, _ := strconv.Atoi(action.data)
				if slotIdx >= 0 && slotIdx < 15 && primarySlotIDs[slotIdx] != "" {
					modelID := primarySlotIDs[slotIdx]
					log.Printf("Setting %s as primary", modelID)
					gateway.SetModelPrimary(modelID)
					db.LogActivity(database, "Set Primary", modelID, "Manually set as primary model")
					showNotification("FreeLLM", fmt.Sprintf("Primary set to: %s", modelID))
					updateTrayStatus()
					rebuildDynamicMenu()
				}
			case "model_set_fallback_slot":
				slotIdx2, _ := strconv.Atoi(action.data)
				if slotIdx2 >= 0 && slotIdx2 < 50 && fallbackSlotIDs[slotIdx2] != "" {
					modelID := fallbackSlotIDs[slotIdx2]
					log.Printf("Setting %s as primary from fallback", modelID)
					gateway.SetModelPrimary(modelID)
					db.LogActivity(database, "Set Primary", modelID, "Manually set as primary from fallback")
					showNotification("FreeLLM", fmt.Sprintf("Primary set to: %s", modelID))
					updateTrayStatus()
					rebuildDynamicMenu()
				}
			case "model_set_primary":
				log.Printf("Setting %s as primary", action.data)
				gateway.SetModelPrimary(action.data)
				db.LogActivity(database, "Set Primary", action.data, "Manually set as primary model")
				showNotification("FreeLLM", fmt.Sprintf("Primary set to: %s", action.data))
				updateTrayStatus()

			case "model_set_fallback":
				log.Printf("Setting %s as fallback", action.data)
				gateway.SetAsFallback(action.data)
				db.LogActivity(database, "Set Fallback", action.data, "Manually set as fallback model")
				showNotification("FreeLLM", fmt.Sprintf("Fallback set to: %s", action.data))

			case "model_demote":
				log.Printf("Demoting %s to fallback", action.data)
				gateway.DemoteModel(action.data)
				db.LogActivity(database, "Demote Model", action.data, "Demoted to fallback group")
				showNotification("FreeLLM", fmt.Sprintf("Demoted: %s", action.data))

			case "model_promote":
				log.Printf("Promoting %s to primary", action.data)
				gateway.PromoteModel(action.data)
				db.LogActivity(database, "Promote Model", action.data, "Promoted to primary group")
				showNotification("FreeLLM", fmt.Sprintf("Promoted: %s", action.data))
				updateTrayStatus()

			case "model_move_up":
				log.Printf("Moving %s up", action.data)
				gateway.MoveModelUp(action.data)

			case "model_move_down":
				log.Printf("Moving %s down", action.data)
				gateway.MoveModelDown(action.data)

			case "model_skip":
				log.Printf("Skipping %s for 24h", action.data)
				db.SkipModel(database, action.data, 24)
				db.LogActivity(database, "Skip Model", action.data, "Skipped for 24 hours")
				showNotification("FreeLLM", fmt.Sprintf("Skipped (24h): %s", action.data))

			case "model_blacklist":
				log.Printf("Blacklisting %s", action.data)
				db.BlacklistModel(database, action.data)
				db.LogActivity(database, "Blacklist Model", action.data, "Permanently blacklisted")
				showNotification("FreeLLM", fmt.Sprintf("Blacklisted: %s", action.data))

			// --- Provider Toggle ---
			case "toggle_provider":
				providers, _ := db.GetProviderHealth(database)
				var currentEnabled bool
				for _, p := range providers {
					if p.Name == action.data {
						currentEnabled = p.Enabled
						break
					}
				}
				newState := !currentEnabled
				log.Printf("Toggling provider %s: enabled=%v", action.data, newState)
				db.SetProviderStatus(database, action.data, newState)
				db.LogActivity(database, "Toggle Provider", action.data,
					fmt.Sprintf("Provider %s: enabled=%v", action.data, newState))

			// --- Quit ---
			case "quit":
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	tokdietMu.Lock()
	cmd := tokdietCmd
	tokdietMu.Unlock()
	if cmd != nil && cmd.Process != nil {
		log.Printf("[TOKDIET] Stopping process %d...", cmd.Process.Pid)
		_ = cmd.Process.Kill()
	}
	if tokdietLogFile != nil {
		_ = tokdietLogFile.Close()
	}
}
