package proxy

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xeipuuv/gojsonschema"
	"github.com/robertpelloni/freellm/internal/config"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
	"github.com/robertpelloni/freellm/internal/tokdiet"
)

type cacheEntry struct {
	data   []byte
	expiry time.Time
}

type Gateway struct {
	Port int
	RankedModels engine.RankedModels
	mu sync.RWMutex
	Queue chan *RequestJob
	HighPriQueue chan *RequestJob
	MaxActive int
	DB *sql.DB
	PrimaryCount int
	Cache map[string]cacheEntry
	cacheMu sync.RWMutex
	cooldownMu sync.Mutex
	providerCooldown map[string]time.Time // provider -> cooldown until
	providerSems     map[string]chan struct{} // per-provider concurrency semaphores
	upstreamSem      chan struct{}             // global upstream request limiter
	Redis *redis.Client
	Client *http.Client
	preflightCache map[string]preflightEntry
	preflightCacheMu sync.RWMutex
	cbRecoveryMu sync.Mutex
	cbLogMu sync.Mutex
	cbLogTime time.Time
	LastUsedModel string
	LastUsedProvider string
	A2A A2ARouter
	Sessions   *SessionTracker
	activeSem  chan struct{} // main proxy path concurrency semaphore (MaxActive)
	FanOutSize int           // number of parallel requests in fan-out
	ShuffleEnabled bool      // whether to shuffle models after successful request
	MinParamsFilter int // filter out models with params <= this value (billions); 0 = disabled
	provenModels     map[string]bool // map of modelID+provider that have successfully worked
	provenMu         sync.RWMutex
	sessionModelLocks map[string]time.Time // modelID+provider -> locked until
	sessionLockMu    sync.RWMutex

	// Persistent circuit breaker state (lives in-process; cleared on restart).
	// After modelFailureThreshold consecutive fatal errors, the model is
	// blocked for modelDisableDuration. The same applies at the provider
	// level: after providerFailureThreshold failures across any of its
	// models, the whole provider is put on cooldown.
	modelFailureMu        sync.RWMutex
	modelFailureCount     map[string]int       // modelID+provider -> consecutive fatal failures
	modelDisabledUntil    map[string]time.Time // modelID+provider -> blocked until
	providerFailureMu     sync.RWMutex
	providerFailureCount  map[string]int       // provider -> consecutive failures across all its models

	// Configurable Timeouts & Settings
	RequestTimeout           time.Duration
	StreamTimeout            time.Duration
	ConnectTimeout           time.Duration
	WatchdogTimeout          time.Duration
	ProvenWatchdogTimeout    time.Duration
	ReasoningWatchdogTimeout time.Duration
	LockDuration             time.Duration
	SmartSwitchDelay         time.Duration // Wait window to see if a better model responds

	Compression config.CompressionSettings

	RouterLogger *log.Logger

	persistentDisables map[string]string // modelID+provider -> reason
	persistMu          sync.RWMutex

	RankingsCache *engine.RankingsCache
}

// A2ARouter is the interface for A2A protocol route handling.
type A2ARouter interface {
	ServeAgentCard(w http.ResponseWriter, r *http.Request)
	ServeA2A(w http.ResponseWriter, r *http.Request)
	ServeAgentList(w http.ResponseWriter, r *http.Request)
}

type preflightEntry struct {
	ok        bool
	checkedAt time.Time
}

type RequestJob struct {
	Request  *http.Request
	Response chan *ProxyResponse
	Ctx      context.Context
	DBID     int64

	IsStream bool // Whether client expects SSE streaming

	// Events carries notable router activity (fan-out, failures, cooldowns)
	// emitted by processJob while a request is in flight. ServeHTTP drains it
	// and injects each event into the response body so the chain's state is
	// visible to the client (e.g. the pi coding agent's transcript) instead
	// of being lost to the log file. The channel is buffered; senders use a
	// non-blocking send so a slow consumer can never stall the router.
	Events chan RouterEvent
}

// RouterEvent is a single notable router activity line shipped to the client.
type RouterEvent struct {
	Tag     string // e.g. ROUTER, CB
	Message string // human-readable detail
}

// String renders a RouterEvent as a single terse line for injection into the
// assistant message content.
func (e RouterEvent) String() string {
	tag := e.Tag
	if tag == "" {
		tag = "ROUTER"
	}
	msg := strings.TrimSpace(e.Message)
	return fmt.Sprintf("[freellm:%s] %s", tag, msg)
}

type ProxyResponse struct {
	Status int
	Body []byte
	Header http.Header
	Err error
	ErrorMessage string
	Stream io.ReadCloser
	ModelID string
	Provider string
	OriginalPayload map[string]interface{}
	Alternatives []engine.ModelCandidate
}

// writeSSEError sends an error message as a valid SSE event for streaming clients
func writeSSEError(w http.ResponseWriter, message string, modelID string) {
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	if modelID == "" {
		modelID = "error"
	}

	flusher, ok := w.(http.Flusher)

	// First send the error as a text delta so the user can see it
	errTextChunk, _ := json.Marshal(map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": fmt.Sprintf("\n\n[FreeLLM Proxy Error: %s]\n\n", message),
				},
			},
		},
	})
	fmt.Fprintf(w, "data: %s\n\n", string(errTextChunk))
	if ok {
		flusher.Flush()
	}

	// Then send the formal error object as a chunk
	errObjChunk, _ := json.Marshal(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "server_error",
			"code":    "proxy_error",
		},
	})
	fmt.Fprintf(w, "data: %s\n\n", string(errObjChunk))
	if ok {
		flusher.Flush()
	}

	// Finally send the finish reason and DONE sentinel
	stopChunk, _ := json.Marshal(map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": "stop",
			},
		},
	})
	fmt.Fprintf(w, "data: %s\n\n", string(stopChunk))
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if ok {
		flusher.Flush()
	}
}

// writeSSETextChunk emits a single text delta as a valid OpenAI
// chat.completion.chunk, then flushes. It's used to inject router activity
// into a streaming response while the model is still being selected/streamed.
func writeSSETextChunk(w http.ResponseWriter, text string, model string) {
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	if model == "" {
		model = "freellm"
	}
	chunk, _ := json.Marshal(map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": text,
				},
			},
		},
	})
	fmt.Fprintf(w, "data: %s\n\n", string(chunk))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func NewGateway(maxActive int, database *sql.DB, port int) *Gateway {
	g := &Gateway{
		Port: port,
		Queue: make(chan *RequestJob, 100),
		HighPriQueue: make(chan *RequestJob, 200),
		MaxActive: 50,
		DB: database,
		PrimaryCount: 10,
		Cache: make(map[string]cacheEntry),
		providerCooldown: make(map[string]time.Time),
		Client: tokdiet.NewClient(900 * time.Second),
		preflightCache: make(map[string]preflightEntry),
		Sessions:   NewSessionTracker(),
		activeSem: make(chan struct{}, maxActive),
		FanOutSize: 3, // Default to 3
		ShuffleEnabled: true,
		MinParamsFilter: 120, // Exclude models <= 120B by default
		provenModels: make(map[string]bool),
		sessionModelLocks: make(map[string]time.Time),
		providerSems:      make(map[string]chan struct{}),
		upstreamSem:        make(chan struct{}, 1000), // Global limit increased to 1000
		modelFailureCount:    make(map[string]int),
		modelDisabledUntil:   make(map[string]time.Time),
		providerFailureCount: make(map[string]int),

		// Default Compression Settings (Selective)
		Compression: config.CompressionSettings{
			EnableRTK:       false,
			EnableHeadroom:  true,
			EnableLLMLingua: false,
			EnableTokdiet:   true,
		},

		// Default Timeouts
		RequestTimeout:           900 * time.Second,
		StreamTimeout:            900 * time.Second,
		ConnectTimeout:           120 * time.Second,
		WatchdogTimeout:          120 * time.Second,
		ProvenWatchdogTimeout:    240 * time.Second,
		ReasoningWatchdogTimeout: 240 * time.Second,
		LockDuration:             60 * time.Second,
		SmartSwitchDelay:         30 * time.Second,

		persistentDisables: make(map[string]string),
	}

	if database != nil {
		if disabled, err := db.GetDisabledModels(database); err == nil {
			g.persistentDisables = disabled
			log.Printf("[DB] Loaded %d permanently disabled models", len(disabled))
		}
	}

	os.MkdirAll("logs", 0755)
	logFile, err := os.OpenFile("logs/router.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		g.RouterLogger = log.New(logFile, "", log.LstdFlags)
	}

	// Update client timeout to match RequestTimeout
	g.Client.Timeout = 900 * time.Second

	go g.workerLoop()
	go g.cacheMaintenanceLoop()
	return g
}

func (g *Gateway) IsProven(modelID, provider string) bool {
	g.provenMu.RLock()
	defer g.provenMu.RUnlock()
	return g.provenModels[modelID+"|"+provider]
}

func (g *Gateway) MarkProven(modelID, provider string) {
	g.provenMu.Lock()
	defer g.provenMu.Unlock()
	key := modelID + "|" + provider
	if !g.provenModels[key] {
		log.Printf("[ROUTER] Marking model %s(%s) as PROVEN - will receive extended timeouts", modelID, provider)
		g.provenModels[key] = true
	}
}

func (g *Gateway) IsModelLocked(modelID, provider string) bool {
	g.sessionLockMu.RLock()
	defer g.sessionLockMu.RUnlock()
	until, ok := g.sessionModelLocks[modelID+provider]
	return ok && time.Now().Before(until)
}

func (g *Gateway) LockModelForSession(modelID, provider string) {
	g.sessionLockMu.Lock()
	defer g.sessionLockMu.Unlock()
	g.sessionModelLocks[modelID+provider] = time.Now().Add(g.LockDuration)
	log.Printf("[SESSION] Locked model %s(%s) for %v", modelID, provider, g.LockDuration)
}

func (g *Gateway) cleanupExpiredLocks() {
	g.sessionLockMu.Lock()
	defer g.sessionLockMu.Unlock()
	now := time.Now()
	for k, until := range g.sessionModelLocks {
		if now.After(until) {
			delete(g.sessionModelLocks, k)
		}
	}
}

// Circuit breaker thresholds. Tuned to clear dead models out of the
// rotation quickly while still allowing legitimate transient blips to
// recover. The key tradeoff: if the duration is too long, the pool
// drains faster than it refills and the proxy stalls in a "no fresh
// candidates" sleep loop; if it's too short, broken models never get
// pruned. 5 minutes is the sweet spot — a permanently broken model
// keeps re-tripping on every probe and stays sidelined, while a model
// with a genuine transient issue comes back into rotation.
const (
	modelFailureThreshold    = 3             // consecutive fatal errors before model is disabled
	modelDisableDuration     = 5 * time.Minute
	providerFailureThreshold = 3             // consecutive failures (across any of its models) before provider cooldown
	providerCooldownOnTrip   = 2 * time.Minute
)

// isModelFatalStatus reports whether a status code indicates the model is
// fundamentally broken (vs transient: 408/413/429/5xx). Only fatal codes
// contribute to the model circuit breaker; transient codes just trigger
// short cooldowns.
func isModelFatalStatus(status int) bool {
	switch status {
	case 400, 401, 402, 403, 404, 413, 422:
		return true
	default:
		return false
	}
}

// recordModelFailure increments the consecutive-failure counter for a model.
// When the counter crosses modelFailureThreshold, the model is disabled for
// modelDisableDuration and the function returns true.
func (g *Gateway) recordModelFailure(modelID, provider string, status int, reason string) bool {
	if !isModelFatalStatus(status) {
		return false
	}
	key := modelID + "|" + provider

	// Permanently disable on auth/payment errors (401, 402, 403) or 404
	if status == 401 || status == 402 || status == 403 || status == 404 {
		g.persistMu.Lock()
		g.persistentDisables[key] = reason
		g.persistMu.Unlock()
		if g.DB != nil {
			_ = db.DisableModel(g.DB, modelID, provider, reason)
		}
		log.Printf("[CB] Model %s(%s) permanently disabled: %s (status %d)",
			modelID, provider, reason, status)
		return true
	}

	g.modelFailureMu.Lock()
	defer g.modelFailureMu.Unlock()
	g.modelFailureCount[key]++
	count := g.modelFailureCount[key]
	if count >= modelFailureThreshold {
		g.modelDisabledUntil[key] = time.Now().Add(modelDisableDuration)
		log.Printf("[CB] Model %s(%s) disabled for %v after %d consecutive fatal errors (last status %d)",
			modelID, provider, modelDisableDuration, count, status)
		// Reset counter so the model gets a fresh start after the disable expires.
		g.modelFailureCount[key] = 0
		return true
	}
	return false
}

// recordModelSuccess clears any pending failure counter / disable for a model.
func (g *Gateway) recordModelSuccess(modelID, provider string) {
	key := modelID + "|" + provider
	g.modelFailureMu.Lock()
	defer g.modelFailureMu.Unlock()
	if _, ok := g.modelFailureCount[key]; ok {
		delete(g.modelFailureCount, key)
	}
	if _, ok := g.modelDisabledUntil[key]; ok {
		delete(g.modelDisabledUntil, key)
	}
}

func (g *Gateway) persistRankings() {
	g.mu.RLock()
	models := make(engine.RankedModels, len(g.RankedModels))
	copy(models, g.RankedModels)
	cache := g.RankingsCache
	g.mu.RUnlock()

	if cache != nil {
		_ = cache.Save(models)
	}
}

// AdjustModelScore updates the score of a model in the global list.
// multiplier > 1.0 promotes, < 1.0 demotes.
func (g *Gateway) AdjustModelScore(modelID, provider string, multiplier float64) {
	g.mu.Lock()
	for i := range g.RankedModels {
		if g.RankedModels[i].ID == modelID && g.RankedModels[i].Provider == provider {
			g.RankedModels[i].Score *= multiplier
			// Apply bounds
			if g.RankedModels[i].Score < 0.1 {
				g.RankedModels[i].Score = 0.1
			}
			if g.RankedModels[i].Score > 10.0 {
				g.RankedModels[i].Score = 10.0
			}
			log.Printf("[ROUTER] Adjusted score for %s(%s) to %.2f (multiplier %.2f)",
				modelID, provider, g.RankedModels[i].Score, multiplier)
			break
		}
	}
	g.mu.Unlock()
	g.persistRankings()
}

// isModelDisabled returns true if the model is currently circuit-broken.
func (g *Gateway) isModelDisabled(modelID, provider string) bool {
	key := modelID + "|" + provider

	g.persistMu.RLock()
	if _, ok := g.persistentDisables[key]; ok {
		g.persistMu.RUnlock()
		return true
	}
	g.persistMu.RUnlock()

	g.modelFailureMu.RLock()
	defer g.modelFailureMu.RUnlock()
	until, ok := g.modelDisabledUntil[key]
	return ok && time.Now().Before(until)
}

// recordProviderFailure increments the provider failure counter. When the
// threshold is crossed, the whole provider is put on cooldown and the
// counter resets so the next trip can fire again.
func (g *Gateway) recordProviderFailure(provider string) {
	g.providerFailureMu.Lock()
	g.providerFailureCount[provider]++
	count := g.providerFailureCount[provider]
	trip := count >= providerFailureThreshold
	if trip {
		g.providerFailureCount[provider] = 0
	}
	g.providerFailureMu.Unlock()

	if trip {
		g.applyProviderCooldown(provider, providerCooldownOnTrip)
		log.Printf("[CB] Provider %s cooldown triggered for %v after %d consecutive failures across its models",
			provider, providerCooldownOnTrip, count)
	}
}

// recordProviderSuccess clears the provider failure counter.
func (g *Gateway) recordProviderSuccess(provider string) {
	g.providerFailureMu.Lock()
	defer g.providerFailureMu.Unlock()
	delete(g.providerFailureCount, provider)
}

// cleanupExpiredModelBlocks removes expired disables from the in-memory map.
// Called from filterCandidates so expired entries free up automatically.
func (g *Gateway) cleanupExpiredModelBlocks() {
	g.modelFailureMu.Lock()
	defer g.modelFailureMu.Unlock()
	now := time.Now()
	for k, until := range g.modelDisabledUntil {
		if now.After(until) {
			delete(g.modelDisabledUntil, k)
			delete(g.modelFailureCount, k)
		}
	}
}

func (g *Gateway) GetProviderSem(provider string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sem, ok := g.providerSems[provider]; ok {
		return sem
	}
	// Default: 3 concurrent requests per provider
	sem := make(chan struct{}, 3)
	g.providerSems[provider] = sem
	return sem
}

func (g *Gateway) UpdateModels(models engine.RankedModels) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.RankedModels = models
}

func (g *Gateway) LogRouterEvent(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	log.Printf("[ROUTER] %s", msg)
	if g.RouterLogger != nil {
		g.RouterLogger.Println(msg)
	}
}

func (g *Gateway) GetModels() engine.RankedModels {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.RankedModels
}

func (g *Gateway) GetLastUsed() (string, string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.LastUsedModel, g.LastUsedProvider
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authKey := os.Getenv("FREELLM_MASTER_KEY")
	if authKey != "" {
		token := r.Header.Get("Authorization")
		if token != "Bearer "+authKey {
			writeJSONError(w, 401, "Unauthorized", "invalid_api_key", "auth")
			return
		}
	}

	if r.URL.Path == "/health" || r.URL.Path == "/health/liveness" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
		return
	}
	if r.URL.Path == "/health/readiness" {
		g.mu.RLock()
		count := len(g.RankedModels)
		g.mu.RUnlock()
		if count > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ready"}`))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"not_ready"}`))
		}
		return
	}
	if r.URL.Path == "/v1/reset-circuit-breaker" {
		g.modelFailureMu.Lock()
		g.modelDisabledUntil = make(map[string]time.Time)
		g.modelFailureCount = make(map[string]int)
		g.modelFailureMu.Unlock()

		g.providerFailureMu.Lock()
		g.providerFailureCount = make(map[string]int)
		g.providerFailureMu.Unlock()

		g.cooldownMu.Lock()
		g.providerCooldown = make(map[string]time.Time)
		g.cooldownMu.Unlock()

		g.mu.Lock()
		for i := range g.RankedModels {
			g.RankedModels[i].Disabled = false
			if g.RankedModels[i].Score < 1.0 {
				g.RankedModels[i].Score = 1.0
			}
		}
		g.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok", "message":"all models and providers re-enabled"}`))
		return
	}

	if r.URL.Path == "/v1/models" || strings.HasPrefix(r.URL.Path, "/v1/models/") {
		g.handleModels(w, r)
		return
	}
	if r.URL.Path == "/v1/messages" {
		g.handleAnthropicMessages(w, r)
		return
	}
	if r.URL.Path == "/v1/responses" {
		g.handleResponsesAPI(w, r)
		return
	}
	// Session info endpoint
	if r.URL.Path == "/api/sessions" {
		g.mu.RLock()
		count := 0
		if g.Sessions != nil {
			count = g.Sessions.ActiveSessionCount()
		}
		g.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active_sessions": count,
		})
		return
	}

	// A2A protocol routes
	if g.A2A != nil {
		if r.URL.Path == "/.well-known/agent-card" {
			g.A2A.ServeAgentCard(w, r)
			return
		}
		if r.URL.Path == "/a2a" {
			g.A2A.ServeA2A(w, r)
			return
		}
		if r.URL.Path == "/a2a/agents" {
			g.A2A.ServeAgentList(w, r)
			return
		}

	}
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}

	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	schema := gojsonschema.NewStringLoader(`{"type":"object","properties":{"model":{"type":"string"},"messages":{"type":"array"}},"required":["model","messages"]}`)
	result, err := gojsonschema.Validate(schema, gojsonschema.NewBytesLoader(body))
	if err == nil && !result.Valid() {
		writeJSONError(w, 400, "Invalid OpenAI Request Schema", "invalid_request_error", "request")
		return
	}

	ctx := r.Context()
	cacheKey := g.normalizeBody(body)

	if g.Redis != nil {
		if val, err := g.Redis.Get(ctx, cacheKey).Bytes(); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-FreeLLM-Cache", "HIT (Redis)")
			w.Write(val)
			return
		}
	} else {
		g.cacheMu.RLock()
		if entry, ok := g.Cache[cacheKey]; ok && time.Now().Before(entry.expiry) {
			g.cacheMu.RUnlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-FreeLLM-Cache", "HIT")
			w.Write(entry.data)
			return
		}
		g.cacheMu.RUnlock()
	}

	dbID := int64(0)
	if g.DB != nil {
		var headers strings.Builder
		for k, v := range r.Header {
			fmt.Fprintf(&headers, "%s: %s\n", k, strings.Join(v, ","))
		}
		dbID, _ = db.EnqueueRequest(g.DB, r.Method, r.URL.String(), headers.String(), body)
	}

	// Detect if client expects SSE streaming
	isStream := false
	var peekPayload map[string]interface{}
	if json.Unmarshal(body, &peekPayload) == nil {
		if s, ok := peekPayload["stream"].(bool); ok {
			isStream = s
		}
	}

	wroteHeader := false
	// For streaming requests, send SSE headers IMMEDIATELY so the client
	// knows the connection is alive. This prevents "terminated" errors
	// from clients that timeout waiting for the initial response headers.
	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("X-FreeLLM-Model", "routing")
		w.WriteHeader(http.StatusOK)
		wroteHeader = true
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		log.Printf("[PROXY] Sent SSE headers immediately (stream=true)")
	}

	job := &RequestJob{
		Request:  r,
		Response: make(chan *ProxyResponse, 1),
		Ctx:      ctx,
		DBID:     dbID,
		IsStream: isStream,
		Events:   make(chan RouterEvent, 64),
	}

	// Process job with concurrency control: up to MaxActive (50) concurrent
	// requests. Additional requests wait for a slot via the semaphore in the background.
	go func() {
		select {
		case g.activeSem <- struct{}{}:
			defer func() { <-g.activeSem }() // release slot
			g.processJob(job)
		case <-ctx.Done():
			// Client disconnected before we even started processing
			return
		}
	}()

	log.Printf("[PROXY] Waiting for job response (stream=%v)...", isStream)
	var resp *ProxyResponse
	// Keepalive loop: send periodic pings to prevent client timeouts
	// Reduced to 5s to ensure compatibility with aggressive client-side watchdogs
	keepaliveInterval := 5 * time.Second
	keepaliveTicker := time.NewTicker(keepaliveInterval)
	defer keepaliveTicker.Stop()

	// routerEvents collects notable router activity emitted by processJob.
	// For streaming clients each event is also written live as a content
	// chunk (SSE headers are already flushed); for non-stream clients the
	// collected lines are prepended to the final response body.
	var routerEvents []string
	flusher, _ := w.(http.Flusher)

WaitLoop:
	for {
		select {
		case resp = <-job.Response:
			break WaitLoop
		case ev := <-job.Events:
			line := ev.String()
			routerEvents = append(routerEvents, line)
			if isStream {
				// SSE headers are already flushed; emit the event live as a
				// content chunk so it shows up before the model's content.
				writeSSETextChunk(w, line+"\n", "freellm")
			}
		case <-ctx.Done():
			log.Printf("[PROXY] Request context cancelled: %v", ctx.Err())
			// Attempt to send a timeout error if we haven't sent headers yet
			if !wroteHeader {
				if ctx.Err() == context.DeadlineExceeded {
					writeJSONError(w, 504, "Gateway Timeout", "timeout", "request")
				}
			}
			return
		case <-keepaliveTicker.C:
			if isStream {
				// SSE headers already sent, just send keepalive comment
				// No logging here to prevent flooding the logs on high concurrency
				fmt.Fprintf(w, ": keepalive\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}
	log.Printf("[PROXY] Got response, err=%v status=%d", resp.Err, resp.Status)
	if resp.Err != nil {
		log.Printf("[PROXY] Job error: %v", resp.Err)
		if isStream {
			writeSSEError(w, resp.Err.Error(), "")
		} else {
			writeJSONError(w, http.StatusBadGateway, resp.Err.Error(), "server_error", "proxy")
		}
		return
	}

	if !wroteHeader {
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		// Add FreeLLM routing metadata so clients know which model/provider served them
		w.Header().Set("X-FreeLLM-Model", resp.ModelID)
		w.Header().Set("X-FreeLLM-Provider", resp.Provider)
		fullModelID := resp.ModelID + "(" + resp.Provider + ")"
		w.Header().Set("X-FreeLLM-FullModel", fullModelID)
		w.WriteHeader(resp.Status)
	} else {
		// Headers already sent (SSE keepalive), but add model info to trailers
		w.Header().Set("X-FreeLLM-Model", resp.ModelID)
		w.Header().Set("X-FreeLLM-Provider", resp.Provider)
	}

	if resp.Stream != nil {
		defer resp.Stream.Close()
		if flusher, ok := w.(http.Flusher); ok {
			g.streamSSE(w, flusher, resp.Stream, resp.ModelID, resp.Provider)
		} else {
			io.Copy(w, resp.Stream)
		}
		g.logUsage(resp.ModelID, nil, nil)
	} else if isStream {
		// If the response is not a stream but the client expects a stream (e.g. error or fallback/mock),
		// we must format it as SSE events.
		flusher, ok := w.(http.Flusher)

		// Transform plaintext tool calls into formal ones if needed
		resp.Body = g.transformPlaintextToolCalls(resp.Body)

		var bodyMap map[string]interface{}
		isErrorBody := resp.Status >= 400
		hasErrorKey := false

		if json.Unmarshal(resp.Body, &bodyMap) == nil {
			if _, ok := bodyMap["error"]; ok {
				hasErrorKey = true
				isErrorBody = true
			}
		} else {
			// If not valid JSON, treat as error string
			isErrorBody = true
		}

		if isErrorBody && !hasErrorKey {
			// Extract a meaningful error message from JSON/non-JSON response
			errMsg := ""
			if bodyMap != nil {
				if msg, ok := bodyMap["message"].(string); ok && msg != "" {
					errMsg = msg
				} else if msg, ok := bodyMap["error_message"].(string); ok && msg != "" {
					errMsg = msg
				} else if msg, ok := bodyMap["msg"].(string); ok && msg != "" {
					errMsg = msg
				} else if msg, ok := bodyMap["detail"].(string); ok && msg != "" {
					errMsg = msg
				} else {
					// Convert bodyMap to a JSON string if possible, or fall back to raw body
					if rawJSON, err := json.Marshal(bodyMap); err == nil {
						errMsg = string(rawJSON)
					} else {
						errMsg = string(resp.Body)
					}
				}
			} else {
				errMsg = string(resp.Body)
			}

			// Wrap in standard OpenAI error format
			errJSON, _ := json.Marshal(map[string]interface{}{
				"error": map[string]interface{}{
					"message": errMsg,
					"type":    "server_error",
					"code":    "upstream_error",
				},
			})
			resp.Body = errJSON
		}

		modelID := resp.ModelID
		if modelID == "" {
			modelID = "error"
		}

		if isErrorBody {
			// Extract error message from resp.Body
			errMsg := "Unknown error"
			var errDetail map[string]interface{}
			if json.Unmarshal(resp.Body, &errDetail) == nil {
				if errSub, ok := errDetail["error"].(map[string]interface{}); ok {
					if msg, ok := errSub["message"].(string); ok {
						errMsg = msg
					}
				}
			}
			writeSSEError(w, errMsg, modelID)
			return
		} else {
			// It's a successful non-streaming response. Let's translate it into SSE chunks.
			id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
			
			content := ""
			finishReason := "stop"
			var bodyMap map[string]interface{}
			if json.Unmarshal(resp.Body, &bodyMap) == nil {
				if choices, ok := bodyMap["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if msg, ok := choice["message"].(map[string]interface{}); ok {
							content, _ = msg["content"].(string)
						}
						if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
							finishReason = fr
						}
					}
				}
			}

			// Send text delta chunk
			if content != "" {
				chunk := map[string]interface{}{
					"id":      id,
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   modelID,
					"choices": []interface{}{
						map[string]interface{}{
							"index": 0,
							"delta": map[string]interface{}{
								"role":    "assistant",
								"content": content,
							},
						},
					},
				}
				if cleaned, err := json.Marshal(chunk); err == nil {
					fmt.Fprintf(w, "data: %s\n\n", string(cleaned))
					if ok {
						flusher.Flush()
					}
				}
			}

			// Send final stop chunk
			stopChunk := map[string]interface{}{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   modelID,
				"choices": []interface{}{
					map[string]interface{}{
						"index":         0,
						"delta":         map[string]interface{}{},
						"finish_reason": finishReason,
					},
				},
			}
			if cleaned, err := json.Marshal(stopChunk); err == nil {
				fmt.Fprintf(w, "data: %s\n\n", string(cleaned))
				if ok {
					flusher.Flush()
				}
			}
			fmt.Fprintf(w, "data: [DONE]\n\n")
		}
	} else {
		// Non-streaming response.
		// Rewrite model field to include provider for client visibility
		var respMap map[string]interface{}
		if json.Unmarshal(resp.Body, &respMap) == nil {
			if _, ok := respMap["model"]; ok {
				respMap["model"] = resp.ModelID + "(" + resp.Provider + ")"
				if rewritten, err := json.Marshal(respMap); err == nil {
					w.Write(rewritten)
					return
				}
			}
		}
		w.Write(resp.Body)
	}
}

// prependRouterTrailNonStream injects the collected router activity lines at
// the start of the assistant message content in a non-streaming OpenAI
// response body. It mirrors prependModelPrefixNonStream's JSON surgery.
func prependRouterTrailNonStream(respBody []byte, events []string) []byte {
	var resp map[string]interface{}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return respBody
	}
	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return respBody
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return respBody
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return respBody
	}
	content, _ := msg["content"].(string)
	prefix := strings.Join(events, "\n") + "\n\n"
	msg["content"] = prefix + content
	if merged, err := json.Marshal(resp); err == nil {
		return merged
	}
	return respBody
}

// streamSSE reads an SSE stream from upstream, sanitizes each chunk,
// and forwards it to the client. It ensures proper [DONE] sentinel
// and finish_reason even if the upstream drops unexpectedly.
func (g *Gateway) streamSSE(w http.ResponseWriter, flusher http.Flusher, body io.ReadCloser, modelID string, provider string) {
	// Strip Content-Length since we may modify chunks
	w.Header().Del("Content-Length")

	bufReader := bufio.NewReader(body)
	sentFinishReason := false
	sentPrefix := false

	prefix := fmt.Sprintf("[Model: %s | Provider: %s]\n\n", modelID, provider)

	for {
		line, err := bufReader.ReadString('\n')
		if err != nil && line == "" {
			break
		}

		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			// Empty lines are SSE field separators — forward them
			fmt.Fprintf(w, "\n")
			flusher.Flush()
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			// Non-data lines (e.g. comments) — forward as-is
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
			continue
		}

		data := line[6:]

		if data == "[DONE]" {
			// Upstream signaled completion. Break out of the read loop so we
			// can inject a synthetic finish_reason (if needed) before
			// emitting the final [DONE] below.
			break
		}

		// Parse the JSON chunk to sanitize it
		var chunk map[string]interface{}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			// Not valid JSON — forward as-is
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
			continue
		}

		// Rewrite model field to include provider in every chunk
		if _, hasModel := chunk["model"]; hasModel {
			chunk["model"] = modelID + "(" + provider + ")"
		}

		if choices, ok := chunk["choices"].([]interface{}); ok {
			for _, c := range choices {
				if choice, ok := c.(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						// Migrate reasoning_content to content if content is empty
						if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
							if existing, ok := delta["content"].(string); !ok || existing == "" {
								delta["content"] = rc
							}
						}
						if r, ok := delta["reasoning"].(string); ok && r != "" {
							if existing, ok := delta["content"].(string); !ok || existing == "" {
								delta["content"] = r
							}
						}
						delete(delta, "reasoning_content")
						delete(delta, "reasoning")

						// Inject prefix on the very first text content we stream
						if !sentPrefix {
							if content, ok := delta["content"].(string); ok && content != "" {
								// We only inject if it doesn't look like a tool call
								lowered := strings.ToLower(content)
								if !strings.Contains(lowered, "[tool_call]") &&
									!strings.Contains(lowered, "<tool_call") &&
									!strings.Contains(lowered, "<function=") &&
									!strings.Contains(lowered, "```bash") &&
									!strings.Contains(lowered, "```python") {
									delta["content"] = prefix + content
									sentPrefix = true
								}
							}
						}
					}
					// Check if this chunk has a finish_reason
					if fr, ok := choice["finish_reason"]; ok && fr != nil && fr != "null" {
						frStr := fmt.Sprintf("%v", fr)
						if frStr != "" && frStr != "<nil>" {
							sentFinishReason = true
						}
					}
				}
			}
		}

		// Set model name in chunk
		// Re-serialize and forward
		cleaned, err := json.Marshal(chunk)
		if err != nil {
			fmt.Fprintf(w, "%s\n", line)
		} else {
			fmt.Fprintf(w, "data: %s\n", string(cleaned))
		}
		fmt.Fprintf(w, "\n")
		flusher.Flush()
	}

// If the stream ended without a finish_reason, synthesize one.
if !sentFinishReason {
		id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		synthetic := map[string]interface{}{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   modelID,
			"choices": []interface{}{
				map[string]interface{}{
					"index":         0,
					"delta":         map[string]interface{}{},
					"finish_reason": "stop",
				},
			},
		}
		synthJSON, _ := json.Marshal(synthetic)
		fmt.Fprintf(w, "data: %s\n\n", string(synthJSON))
		flusher.Flush()
}

// Always send the [DONE] sentinel so the client never hangs.
// If upstream sent [DONE] (upstreamDone=true) we still emit one here
// since the break above skipped forwarding it; if upstream closed
// without it, this guarantees a clean stream termination.
fmt.Fprintf(w, "data: [DONE]\n\n")
flusher.Flush()
}

func (g *Gateway) workerLoop() {
	if g.MaxActive <= 0 {
		return
	}
	sem := make(chan struct{}, g.MaxActive)

	// Start 5 reserved high-priority workers that bypass the semaphore
	for i := 0; i < 5; i++ {
		go func() {
			for {
				job := <-g.HighPriQueue
				g.processJob(job)
			}
		}()
	}

	for {
		var job *RequestJob
		select {
		case job = <-g.HighPriQueue:
		default:
			select {
			case job = <-g.HighPriQueue:
			case job = <-g.Queue:
			}
		}
		sem <- struct{}{}
		go func(j *RequestJob) {
			defer func() { <-sem }()
			g.processJob(j)
		}(job)
	}
}

// processJob: GUARANTEED DELIVERY routing engine
// isTransientError returns true for HTTP status codes that are not the model's fault
func isTransientError(status int) bool {
	return status == 413 || status == 429 || status == 402 || status == 408 || status == 503
}


func (g *Gateway) normalizeBody(body []byte) string {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return string(body)
	}
	// json.Marshal recursively sorts map keys, providing a canonical representation.
	normalized, err := json.Marshal(m)
	if err != nil {
		return string(body)
	}
	return string(normalized)
}

func (g *Gateway) cacheMaintenanceLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		g.cacheMu.Lock()
		now := time.Now()
		for k, v := range g.Cache {
			if now.After(v.expiry) {
				delete(g.Cache, k)
			}
		}
		if len(g.Cache) > 1000 {
			// If cache exceeds limit, reset to prevent OOM
			log.Printf("[CACHE] Cache limit exceeded (%d), resetting.", len(g.Cache))
			g.Cache = make(map[string]cacheEntry)
		}
		g.cacheMu.Unlock()

		g.preflightCacheMu.Lock()
		for k, v := range g.preflightCache {
			if now.Sub(v.checkedAt) > 10*time.Minute {
				delete(g.preflightCache, k)
			}
		}
		g.preflightCacheMu.Unlock()
	}
}

func (g *Gateway) onSuccess(job *RequestJob, model engine.ModelCandidate, proxyResp *ProxyResponse, body []byte) {
	log.Printf("[PROXY] Routed to: %s (%s) score=%.1f", model.ID, model.Provider, model.Score)
	g.mu.Lock()
	g.LastUsedModel = model.ID
	g.LastUsedProvider = model.Provider
	g.mu.Unlock()
	if job.DBID > 0 && g.DB != nil {
		db.DequeueRequest(g.DB, job.DBID)
	}
	if g.DB != nil {
		db.RecordSuccess(g.DB, model.ID)
	}
	g.logUsage(model.ID, body, proxyResp.Body)
	if proxyResp.Stream == nil {
		normalizedKey := g.normalizeBody(body)
		if g.Redis != nil {
			g.Redis.Set(job.Ctx, normalizedKey, proxyResp.Body, 1*time.Hour)
		} else {
			g.cacheMu.Lock()
			g.Cache[normalizedKey] = cacheEntry{
				data:   proxyResp.Body,
				expiry: time.Now().Add(1 * time.Hour),
			}
			g.cacheMu.Unlock()
		}
	}
	// Reset circuit breaker counters and promote the winner so subsequent
	// requests are biased toward models that just produced a 200. The old
	// code only did this for streaming success via SetModelPrimary, which
	// meant non-stream winners (the more common case in this workload)
	// never got the boost.
	g.recordModelSuccess(model.ID, model.Provider)
	g.recordProviderSuccess(model.Provider)
	if g.ShuffleEnabled {
		g.SetModelPrimary(model.ID)
	}
	// Set model/provider metadata on the response so ServeHTTP can expose them
	proxyResp.ModelID = model.ID
	proxyResp.Provider = model.Provider
	job.Response <- proxyResp
}

// classifyRequest detects tool-call requests and splits models by tool compatibility
func (g *Gateway) classifyRequest(body []byte, models []engine.ModelCandidate) (hasTools bool, toolModels []engine.ModelCandidate, plainModels []engine.ModelCandidate) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, nil, nil
	}
			if _, ok := payload["tools"]; ok {
		hasTools = true
	}
	if tc, ok := payload["tool_choice"]; ok && tc != nil && tc != "none" {
		hasTools = true
	}
	if msgs, ok := payload["messages"].([]interface{}); ok {
		for _, msg := range msgs {
			if m, ok := msg.(map[string]interface{}); ok {
				if r, ok := m["role"].(string); ok && r == "tool" {
					hasTools = true
				}
				if _, ok := m["tool_calls"]; ok {
					hasTools = true
				}
			}
		}
	}

	log.Printf("[ROUTER] classifyRequest: hasTools=%v", hasTools)
	noTool := map[string]bool{
		"cloudflare": true,
		"deepinfra":  true,
		"nvidia":     true,
		"nvidia_nim": true,
		"cerebras":   true,
	}
	for _, m := range models {
		if noTool[m.Provider] {
			plainModels = append(plainModels, m)
		} else {
			toolModels = append(toolModels, m)
		}
	}
	return
}

// filterCandidates removes circuit-broken models
func (g *Gateway) filterCandidates(all engine.RankedModels) []engine.ModelCandidate {
	return g.filterCandidatesWithOverride(all, g.MinParamsFilter)
}

func (g *Gateway) filterCandidatesWithOverride(all engine.RankedModels, minParams int) []engine.ModelCandidate {
	g.cleanupExpiredLocks()
	g.cleanupExpiredModelBlocks()

	skipProvider := map[string]bool{"ollama": true, "lm_studio": true}
	
	// Create a local copy of models to apply score floor without affecting global state
	localModels := make(engine.RankedModels, len(all))
	copy(localModels, all)
	
	valid := make([]engine.ModelCandidate, 0, len(localModels))
	skipped := 0
	// Check provider cooldowns
	g.cooldownMu.Lock()
	now := time.Now()
	activeCooldowns := make(map[string]bool)
	for prov, until := range g.providerCooldown {
		if now.Before(until) {
			activeCooldowns[prov] = true
		} else {
			delete(g.providerCooldown, prov)
		}
	}
	g.cooldownMu.Unlock()

	// Apply score floor to our local copy: models with large parameters should never have negative scores
	// This prevents benchmark failures (429/timeout) from permanently sinking good models
	for i := range localModels {
		if localModels[i].Score < 0 && localModels[i].Parameters > 0 {
			minScore := (float64(min(localModels[i].Parameters, 405)) / 100.0) * 0.2
			localModels[i].Score = minScore
		}
	}

	var blocked map[string]bool
	if g.DB != nil {
		blocked, _ = db.GetCircuitBreakerList(g.DB)
	}

	for _, m := range localModels {
		// Dead model filter
		if engine.IsDeadModel(m.ID) {
			skipped++
			continue
		}

		// Param filter: skip models with params <= minParams threshold
		if minParams > 0 && m.Parameters > 0 && m.Parameters <= minParams {
			skipped++
			continue
		}

		// Name-based filter: skip models with <=120b in their name by default
		if minParams > 0 {
			lowered := strings.ToLower(m.ID)
			// Check for patterns like "7b", "70b", "120b"
			isSmall := false
			// Find the number before 'b'
			bIdx := strings.Index(lowered, "b")
			if bIdx > 0 {
				start := bIdx - 1
				for start >= 0 && lowered[start] >= '0' && lowered[start] <= '9' {
					start--
				}
				numStr := lowered[start+1 : bIdx]
				if numStr != "" {
					if val, err := strconv.Atoi(numStr); err == nil && val <= 120 {
						isSmall = true
					}
				}
			}
			if isSmall {
				skipped++
				continue
			}
		}

		// Session lock: skip models currently locked by another session
		if g.IsModelLocked(m.ID, m.Provider) {
			skipped++
			continue
		}

		// In-memory circuit breaker: skip models that have failed repeatedly
		if g.isModelDisabled(m.ID, m.Provider) {
			skipped++
			continue
		}

		// Only skip negative scores if we are NOT in emergency mode (minParams == 0)
		if m.Score < 0 && minParams > 0 {
			skipped++
			continue
		}
		if skipProvider[m.Provider] {
			skipped++
			continue
		}
		// Only skip cooldowns if we are NOT in emergency mode (minParams == 0)
		if activeCooldowns[m.Provider] && minParams > 0 {
			skipped++
			continue
		}
		if g.getAPIKey(m.Provider) == "" {
			skipped++
			continue
		}
		if g.DB != nil && blocked != nil {
			if blocked[m.ID] || blocked[m.Provider+"/"+m.ID] {
				skipped++
				continue
			}
		}
		valid = append(valid, m)
	}
	nvidiaCount := 0
	for _, m := range localModels {
		if m.Provider == "nvidia" || m.Provider == "nvidia_nim" {
			nvidiaCount++
		}
	}
	log.Printf("[ROUTER] filterCandidates: %d/%d valid (skipped %d, nvidiaInAll=%d)", len(valid), len(all), skipped, nvidiaCount)
	if len(valid) > 0 {
		top3 := make([]string, 0, 3)
		for i := 0; i < 3 && i < len(valid); i++ {
			top3 = append(top3, valid[i].ID+"("+valid[i].Provider+")="+fmt.Sprintf("%.1f", valid[i].Score))
		}
		log.Printf("[ROUTER] filterCandidates top3: %v", top3)
	}
	return valid
}

// autoRecoverCircuitBreakers resets all circuit-broken models for a second chance
func (g *Gateway) autoRecoverCircuitBreakers() {
	g.cbRecoveryMu.Lock()
	defer g.cbRecoveryMu.Unlock()

	// Clear in-memory provider cooldowns
	g.cooldownMu.Lock()
	g.providerCooldown = make(map[string]time.Time)
	g.cooldownMu.Unlock()

	if g.DB != nil {
		g.DB.Exec("UPDATE model_history SET failure_count = 0, retry_after = NULL WHERE failure_count >= 3")
	}
	log.Println("[ROUTER] Circuit breakers and provider cooldowns auto-recovered")
}

// sanitizeRequestBody cleans request body for provider compatibility:
// - ALWAYS strips reasoning_content from messages (DeepSeek adds this, breaks other providers)
// - ALWAYS strips unsupported params per provider
// - For no-tool providers: strips tools/tool_choice/tool messages/tool_calls
// - Sets null assistant content to ""
// isReasoningModel detects models that produce reasoning/thinking tokens.
// These models split max_tokens between thinking and content, so low limits
// cause empty responses (all tokens consumed by thinking).
func isReasoningModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	// DeepSeek R1 and V3+ produce reasoning tokens
	if strings.Contains(lower, "deepseek-r") || strings.Contains(lower, "deepseek-v") || strings.Contains(lower, "deepseek-v4") {
		return true
	}
	// OpenAI o-series reasoning models
	if strings.Contains(lower, "/o1-") || strings.Contains(lower, "/o3-") || strings.Contains(lower, "/o4-") {
		return true
	}
	if strings.HasPrefix(lower, "o1-") || strings.HasPrefix(lower, "o3-") || strings.HasPrefix(lower, "o4-") {
		return true
	}
	// Other known reasoning models
	if strings.Contains(lower, "reason") || strings.Contains(lower, "thinking") {
		return true
	}
	return false
}

// - Sets default max_tokens if not provided
func (g *Gateway) sanitizeRequestBody(provider string, body []byte, hasTools bool, resolvedModelID string) []byte {
	noTool := map[string]bool{
		"cloudflare": true,
		"deepinfra":  true,
		"nvidia":     true,
		"nvidia_nim": true,
		"cerebras":   true,
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	// Clean messages: strip reasoning_content, fix null content, handle tool fields
	if msgs, ok := payload["messages"].([]interface{}); ok {
		var clean []interface{}
		for _, msg := range msgs {
			m, ok := msg.(map[string]interface{})
			if !ok {
				clean = append(clean, msg)
				continue
			}
			// ALWAYS strip reasoning_content - DeepSeek models add this and
			// other providers reject it with "Extra inputs are not permitted"
			// Migrate reasoning to content if content is empty
			if rc, ok := m["reasoning_content"].(string); ok && rc != "" {
				if existing, ok := m["content"].(string); !ok || existing == "" {
					m["content"] = rc
				}
			}
			if r, ok := m["reasoning"].(string); ok && r != "" {
				if existing, ok := m["content"].(string); !ok || existing == "" {
					m["content"] = r
				}
			}
			delete(m, "reasoning_content")
			delete(m, "reasoning")

			// For no-tool providers: strip tool-related fields
			if noTool[provider] && hasTools {
				if r, ok := m["role"].(string); ok && r == "tool" {
					continue // skip tool result messages entirely
				}
				delete(m, "tool_calls")
				delete(m, "tool_call_id")
			}

			// Fix null assistant content -> ""
			if r, ok := m["role"].(string); ok && r == "assistant" {
				if content, exists := m["content"]; exists && content == nil {
					m["content"] = ""
				}
				// If we stripped tools and the assistant message is now completely empty, skip it
				if noTool[provider] && hasTools {
					if c, ok := m["content"].(string); ok && strings.TrimSpace(c) == "" {
						continue
					}
				}
			}

			clean = append(clean, m)
		}
		payload["messages"] = clean

		// Ensure last message is from user - many providers reject
		// requests where the last message is role=assistant
		if len(clean) > 0 {
			if last, ok := clean[len(clean)-1].(map[string]interface{}); ok {
				if r, ok := last["role"].(string); ok && r == "assistant" {
					// Append a continuation prompt so the provider accepts the request
					clean = append(clean, map[string]interface{}{
						"role":    "user",
						"content": "continue",
					})
					payload["messages"] = clean
				}
			}
		}
	}

	// For no-tool providers: strip tools and tool_choice
	if noTool[provider] && hasTools {
		delete(payload, "tools")
		delete(payload, "tool_choice")
	}

	// Strip unsupported params per provider
	switch provider {
	case "mistral", "codestral":
		delete(payload, "frequency_penalty")
		delete(payload, "presence_penalty")
	case "nvidia", "nvidia_nim", "cerebras":
		delete(payload, "frequency_penalty")
		delete(payload, "presence_penalty")
		delete(payload, "logit_bias")
		delete(payload, "logprobs")
		delete(payload, "top_logprobs")
	case "cohere":
		delete(payload, "logit_bias")
		delete(payload, "logprobs")
	case "groq":
		delete(payload, "logit_bias")
		delete(payload, "logprobs")
		delete(payload, "top_logprobs")
	}

	// Smart max_tokens handling for reasoning vs non-reasoning models
	// Use the resolved model ID (e.g. "deepseek-v4-flash-free") not the alias ("free-llm")
	isReasoning := isReasoningModel(resolvedModelID)
	if mt, ok := payload["max_tokens"]; ok {
		// User provided max_tokens - check if it's too low for reasoning models
		if isReasoning {
			if mtFloat, ok := mt.(float64); ok && mtFloat < 2048 {
				// Reasoning models split tokens between thinking and content.
				// Low max_tokens causes all tokens to be spent on thinking,
				// leaving 0 for content. Boost to safe minimum.
				delete(payload, "max_tokens")
			}
		}
	} else {
		// No max_tokens provided - set a generous default
		if isReasoning {
			// Reasoning models need headroom for thinking + content
			payload["max_tokens"] = 8192
		} else {
			payload["max_tokens"] = 4096
		}
	}

	if out, err := json.Marshal(payload); err == nil {
		return out
	}
	return body
}

// PreFlightCheck with 5-minute cache
func (g *Gateway) PreFlightCheck(model engine.ModelCandidate) bool {
	// If the provider has no URL mapped, it's definitely not ready
	targetURL := g.getProviderURL(model.ID, model.Provider)
	if targetURL == "" {
		return false
	}

	// For FreeLLM, we mainly care if we have an API key for the provider
	// (except for local or special providers that don't need them)
	if model.Provider == "opencode_zen" || model.Provider == "ollama" || model.Provider == "lm_studio" {
		return true
	}

	return g.getAPIKey(model.Provider) != ""
}

// forwardRequestInternal sends the request to a specific provider.
func (g *Gateway) forwardRequestInternal(ctx context.Context, client *http.Client, r *http.Request, model engine.ModelCandidate, body []byte, isContinuation bool, alternatives []engine.ModelCandidate) *ProxyResponse {
	trimmedBody := strings.TrimSpace(string(body))
	if len(trimmedBody) == 0 || (trimmedBody[0] != '{' && trimmedBody[0] != '[') {
		return &ProxyResponse{Err: fmt.Errorf("invalid request body: must be JSON object or array")}
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &ProxyResponse{Err: fmt.Errorf("unmarshal request: %v", err)}
	}

	// Set model ID, stripping provider prefixes where needed
	modelForAPI := model.ID
	if model.Provider == "nvidia_nim" {
		modelForAPI = strings.TrimPrefix(modelForAPI, "nvidia_nim/")
	}
	payload["model"] = modelForAPI

	stream, _ := payload["stream"].(bool)

	targetURL := g.getProviderURL(model.ID, model.Provider)
	if targetURL == "" {
		return &ProxyResponse{Err: fmt.Errorf("unsupported provider: %s", model.Provider)}
	}

	// 413 handling: providers reject oversized payloads. We progressively
	// truncate the message history and retry the SAME model, but BOUNDED
	// (at most 5 attempts). If unable to reduce size after that, we return
	// a 413 response so the outer fan-out router can demote this model
	// and switch to an alternative. This avoids the old infinite-recursion
	// bug where a small slice was barely shrunk on each pass and the loop
	// could re-send the still-too-large body forever.
	const maxTruncationSteps = 5
	var resp *http.Response
	var sendErr error
	var req *http.Request
	var err error
	var newBody []byte
	for truncStep := 0; truncStep < maxTruncationSteps; truncStep++ {
		newBody, _ = json.Marshal(payload)
		if mapped, mapErr := TransformRequestBody(model.Provider, newBody); mapErr == nil {
			newBody = mapped
			// IMPORTANT: Update the 'payload' map from the sanitized body 
			// so that future retries/continuations use the sanitized fields.
			var sanitizedPayload map[string]interface{}
			if json.Unmarshal(newBody, &sanitizedPayload) == nil {
				payload = sanitizedPayload
			}
		}

		req, err = http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewBuffer(newBody))
		if err != nil {
			return &ProxyResponse{Err: err}
		}
		g.transformRequest(req, model.Provider, stream)
		log.Printf("[PROXY] Sending request to %s via %s (Stream=%v)", model.ID, model.Provider, stream)

		resp, sendErr = client.Do(req)
		if sendErr != nil {
			log.Printf("[PROXY] Request error for %s(%s): %v", model.ID, model.Provider, sendErr)
			return &ProxyResponse{Err: fmt.Errorf("%s: %v", model.Provider, sendErr)}
		}

		isTooLarge := resp.StatusCode == http.StatusRequestEntityTooLarge
		var bodyStr string
		if resp.StatusCode == http.StatusBadRequest || isTooLarge {
			// Some providers return 400 with a "too large" or "token limit" message instead of 413.
			// NVIDIA NIM often returns 400 for validation errors or large payloads.
			bodyBytes, _ := io.ReadAll(resp.Body)
			bodyStr = strings.ToLower(string(bodyBytes))
			if resp.StatusCode == http.StatusBadRequest {
				if strings.Contains(bodyStr, "too large") || strings.Contains(bodyStr, "token limit") || 
					strings.Contains(bodyStr, "maximum context") || strings.Contains(bodyStr, "validation error") ||
					strings.Contains(bodyStr, "tool choice") || strings.Contains(bodyStr, "tool-call-parser") {
					isTooLarge = true
					log.Printf("[PROXY] Detected context limit or tool validation error in 400 response from %s, triggering recovery loop...", model.Provider)
				}
			}
			// Re-wrap body for later use if we don't truncate
			resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		if !isTooLarge {
			break // not a size/validation error, proceed to normal handling
		}

		// 413 or Validation Error: try to recovery, but stop after bounded attempts.
		resp.Body.Close()
		
		msgs, ok := payload["messages"].([]interface{})
		// If we can't truncate further AND this doesn't look like a tool/validation error 
		// that could be fixed by sanitization, then we bail out.
		isValidationError := strings.Contains(bodyStr, "tool") || strings.Contains(bodyStr, "validation") || strings.Contains(bodyStr, "parameter")
		if (!ok || len(msgs) <= 1) && !isValidationError {
			log.Printf("[PROXY] Model %s(%s) still 413 after %d truncation step(s) and cannot shrink further; giving up.",
				model.ID, model.Provider, truncStep+1)
			return &ProxyResponse{
				Status:   http.StatusRequestEntityTooLarge,
				ModelID:  model.ID,
				Provider: model.Provider,
				Err:      fmt.Errorf("%s: payload too large or invalid for model context", model.Provider),
			}
		}

		if ok && len(msgs) > 1 {
			// Drop the oldest half of the conversation each step (aggressive but bounded).
			log.Printf("[PROXY] Model %s(%s) returned 413/400, truncating context (step %d, %d -> %d messages)...",
				model.ID, model.Provider, truncStep+1, len(msgs), len(msgs)/2)
			payload["messages"] = msgs[len(msgs)/2:]
		} else {
			log.Printf("[PROXY] Model %s(%s) returned validation error, retrying with sanitization (step %d)...",
				model.ID, model.Provider, truncStep+1)
			// We don't truncate messages, but the next loop iteration will call TransformRequestBody
			// which applies provider-specific sanitization (like stripping tool_choice).
		}
	}

	isHTML := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")
	if isHTML {
		defer resp.Body.Close()
		return &ProxyResponse{
			Status: http.StatusNotAcceptable,
			Err:    fmt.Errorf("%s: provider returned HTML instead of API response", model.Provider),
		}
	}

	if stream && resp.StatusCode == http.StatusOK {
		var streamBody io.ReadCloser = resp.Body
		if !isContinuation {
			streamBody = g.newContinuationStream(client, r, model, body, resp.Body, alternatives)
		}

		var payload map[string]interface{}
		json.Unmarshal(body, &payload)

		return &ProxyResponse{
			Status:          resp.StatusCode,
			Header:          resp.Header,
			Stream:          streamBody,
			ModelID:         model.ID,
			Provider:        model.Provider,
			OriginalPayload: payload,
			Alternatives:    alternatives,
		}
	}

	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if !stream && resp.StatusCode == 200 {
		if mapped, err := TransformResponseBody(model.Provider, respBody); err == nil {
			respBody = mapped
		}
		var rd map[string]interface{}
		if json.Unmarshal(respBody, &rd) == nil {
			// Strip reasoning_content from response messages too
			if choices, ok := rd["choices"].([]interface{}); ok {
				for _, c := range choices {
					if choice, ok := c.(map[string]interface{}); ok {
						if msg, ok := choice["message"].(map[string]interface{}); ok {
							// Migrate reasoning to content if content is empty
							if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
								if existing, ok := msg["content"].(string); !ok || existing == "" {
									msg["content"] = rc
								}
							}
							if r, ok := msg["reasoning"].(string); ok && r != "" {
								if existing, ok := msg["content"].(string); !ok || existing == "" {
									msg["content"] = r
								}
							}
							delete(msg, "reasoning_content")
							delete(msg, "reasoning")
						}
					}
				}
				rd["choices"] = choices
			}
			// Add usage if missing
			if _, ok := rd["usage"]; !ok {
				rd["usage"] = map[string]int{
					"prompt_tokens":     len(body) / 4,
					"completion_tokens": len(respBody) / 4,
					"total_tokens":      (len(body) + len(respBody)) / 4,
				}
			}
			// Set model name in response
			rd["model"] = model.ID

			respBody, _ = json.Marshal(rd)
		}
	}

	// Strip Content-Length when we modify the response body (reasoning_content stripping, usage injection)
	if !stream && resp.StatusCode == 200 {
		resp.Header.Del("Content-Length")
	}

	if !stream && resp.StatusCode == 200 && !isContinuation {
		if g.isResponseTruncated(respBody) {
			log.Printf("[AUTO-CONTINUE] Non-stream response truncated, starting auto-continuation...")
			respBody = g.autoContinueNonStream(client, r, model, body, respBody, alternatives)
		}

		// Transform plaintext tool calls into formal ones if needed
		respBody = g.transformPlaintextToolCalls(respBody)

		// Only add model prefix if it's NOT a tool call (to avoid breaking client-side tool parsers)
		if !g.hasToolCallMarkers(respBody) {
			respBody = g.prependModelPrefixNonStream(respBody, model)
		}
	}

	var payload_final map[string]interface{}
	json.Unmarshal(body, &payload_final)

	errMsg := ""
	if resp.StatusCode >= 400 {
		errMsg = "Unknown error"
		var errDetail map[string]interface{}
		if json.Unmarshal(respBody, &errDetail) == nil {
			if errSub, ok := errDetail["error"].(map[string]interface{}); ok {
				if msg, ok := errSub["message"].(string); ok {
					errMsg = msg
				}
			} else if msg, ok := errDetail["message"].(string); ok {
				errMsg = msg
			}
		}
	}

	return &ProxyResponse{
		Status:          resp.StatusCode,
		Body:            respBody,
		Header:          resp.Header,
		ErrorMessage:    errMsg,
		ModelID:         model.ID,
		Provider:        model.Provider,
		OriginalPayload: payload_final,
		Alternatives:    alternatives,
	}
}

// prependModelPrefixNonStream appends [Model: id | Provider: provider] to the assistant message
func (g *Gateway) prependModelPrefixNonStream(respBody []byte, model engine.ModelCandidate) []byte {
	var resp map[string]interface{}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return respBody
	}
	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return respBody
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return respBody
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return respBody
	}

	content, _ := msg["content"].(string)
	
	// Skip if it looks like a tool call to avoid breaking parsers
	lowered := strings.ToLower(content)
	if strings.Contains(lowered, "[tool_call]") ||
		strings.Contains(lowered, "<tool_call") ||
		strings.Contains(lowered, "<function=") ||
		strings.Contains(lowered, "```bash") ||
		strings.Contains(lowered, "```python") {
		return respBody
	}

	// Prepend prefix at the start of the message
	prefix := fmt.Sprintf("[Model: %s | Provider: %s]\n\n", model.ID, model.Provider)
	msg["content"] = prefix + content

	if merged, err := json.Marshal(resp); err == nil {
		return merged
	}
	return respBody
}

// hasToolCallMarkers checks if the assistant message contains plaintext tool call tags
func (g *Gateway) hasToolCallMarkers(respBody []byte) bool {
	var resp map[string]interface{}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return false
	}
	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return false
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return false
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return false
	}

	// Formal tool calls
	if _, hasToolCalls := msg["tool_calls"]; hasToolCalls {
		return true
	}

	// Plaintext tool calls
	if content, ok := msg["content"].(string); ok {
		lowered := strings.ToLower(content)
		if strings.Contains(lowered, "[tool_call]") ||
			strings.Contains(lowered, "<tool_call") ||
			strings.Contains(lowered, "<function=") ||
			strings.Contains(lowered, "```bash") ||
			strings.Contains(lowered, "```python") {
			return true
		}
	}
	return false
}

// isResponseTruncated checks if a response was cut off (no proper completion)
func (g *Gateway) isResponseTruncated(respBody []byte) bool {
	var resp map[string]interface{}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return false // Can't parse, assume not truncated
	}
	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return false
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return false
	}

	// Check if this is a tool call response
	if msg, ok := choice["message"].(map[string]interface{}); ok {
		if _, hasToolCalls := msg["tool_calls"]; hasToolCalls {
			return false // Don't auto-continue formal tool calls
		}
		if content, ok := msg["content"].(string); ok {
			lowered := strings.ToLower(content)
			// If it contains a tool call tag but NO closure, it's definitely truncated
			hasStart := strings.Contains(lowered, "[tool_call]") ||
				strings.Contains(lowered, "<tool_call") ||
				strings.Contains(lowered, "<longcat_tool_call") ||
				strings.Contains(lowered, "```bash") ||
				strings.Contains(lowered, "```python")

			hasEnd := strings.Contains(lowered, "[/tool_call]") ||
				strings.Contains(lowered, "</tool_call>") ||
				strings.Contains(lowered, "</longcat_tool_call>") ||
				(strings.Contains(lowered, "```") && strings.HasSuffix(strings.TrimSpace(lowered), "```"))

			if hasStart && !hasEnd {
				return true // Truncated mid-tool-call
			}
			if hasEnd {
				return false // Completed tool call
			}
		}
	}

	// Check finish_reason
	if fr, ok := choice["finish_reason"]; ok && fr != nil {
		frStr := fmt.Sprintf("%v", fr)
		if frStr == "" || frStr == "<nil>" || frStr == "null" {
			// No finish_reason - treat as truncated
			return true
		}
		// Definite completions
		if frStr == "stop" || frStr == "end_turn" || frStr == "tool_calls" || frStr == "function_call" {
			return false
		}
		// Any other finish_reason (like "length", "max_tokens", etc.) - treat as truncated
		return true
	}
	// No finish_reason field at all - treat as truncated
	return true
}

// autoContinueNonStream continues a truncated response by sending a follow-up request
func (g *Gateway) autoContinueNonStream(client *http.Client, r *http.Request, initialModel engine.ModelCandidate, originalBody []byte, firstRespBody []byte, alternatives []engine.ModelCandidate) []byte {
	currentRespBody := firstRespBody
	var accumulatedContent strings.Builder

	// Parse first response content
	var resp map[string]interface{}
	if err := json.Unmarshal(firstRespBody, &resp); err != nil {
		return firstRespBody
	}
	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return firstRespBody
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return firstRespBody
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return firstRespBody
	}
	content, _ := msg["content"].(string)
	accumulatedContent.WriteString(content)

	// Keep track of the request payload we are building
	var currentPayload map[string]interface{}
	if err := json.Unmarshal(originalBody, &currentPayload); err != nil {
		return firstRespBody
	}

	currentModel := initialModel
	fallbackIdx := 0

	for i := 0; i < 5; i++ { // limit to 5 continuations to avoid infinite loop
		// Get original messages
		originalMsgs, ok := currentPayload["messages"].([]interface{})
		if !ok {
			break
		}

		// Build new messages: original messages + the accumulated assistant response so far + "continue" user message
		newMsgs := make([]interface{}, len(originalMsgs))
		copy(newMsgs, originalMsgs)
		newMsgs = append(newMsgs, map[string]interface{}{
			"role":    "assistant",
			"content": accumulatedContent.String(),
		})
		newMsgs = append(newMsgs, map[string]interface{}{
			"role":    "user",
			"content": "continue",
		})

		newPayload := make(map[string]interface{})
		for k, v := range currentPayload {
			newPayload[k] = v
		}
		newPayload["messages"] = newMsgs

		// Ensure max_tokens is set high enough for continuation
		newPayload["max_tokens"] = 8192

		newBody, err := json.Marshal(newPayload)
		if err != nil {
			break
		}

		log.Printf("[AUTO-CONTINUE] Sending non-stream continuation attempt %d for model %s", i+1, currentModel.ID)

		// Make the request using forwardRequestInternal with isContinuation = true
		contResp := g.forwardRequestInternal(r.Context(), client, r, currentModel, newBody, true, nil)

		// Fallback switching if the continuation fails
		for (contResp.Err != nil || contResp.Status != 200) && fallbackIdx < len(alternatives) {
			fallbackModel := alternatives[fallbackIdx]
			fallbackIdx++
			log.Printf("[AUTO-CONTINUE] Model %s failed in continuation, switching to fallback %s...", currentModel.ID, fallbackModel.ID)

			contResp = g.forwardRequestInternal(r.Context(), client, r, fallbackModel, newBody, true, nil)
			if contResp.Err == nil && contResp.Status == 200 {
				currentModel = fallbackModel
				accumulatedContent.WriteString(fmt.Sprintf("\n\n[Switched to Model: %s | Provider: %s due to error/cutoff]\n\n", currentModel.ID, currentModel.Provider))
				break
			}
		}

		if contResp.Err != nil || contResp.Status != 200 {
			log.Printf("[AUTO-CONTINUE] Continuation attempt %d failed completely.", i+1)
			errMsg := "[FreeLLM Proxy Error: Continuation failed. Response may be incomplete.]"
			if contResp.Err != nil {
				errMsg = fmt.Sprintf("[FreeLLM Proxy Error: Continuation failed: %v]", contResp.Err)
			}
			accumulatedContent.WriteString("\n\n" + errMsg + "\n\n")
			
			// Update the original choice's message content with what we have
			msg["content"] = accumulatedContent.String()
			if merged, err := json.Marshal(resp); err == nil {
				currentRespBody = merged
			}
			break
		}

		var contRespMap map[string]interface{}
		if err := json.Unmarshal(contResp.Body, &contRespMap); err != nil {
			break
		}
		contChoices, ok := contRespMap["choices"].([]interface{})
		if !ok || len(contChoices) == 0 {
			break
		}
		contChoice, ok := contChoices[0].(map[string]interface{})
		if !ok {
			break
		}
		contMsg, ok := contChoice["message"].(map[string]interface{})
		if !ok {
			break
		}
		contContent, _ := contMsg["content"].(string)
		if contContent == "" || len(contContent) < 50 {
			log.Printf("[AUTO-CONTINUE] Continuation returned too-short content (len=%d), trying fallback...", len(contContent))
			// Try next fallback model for this iteration
			for fallbackIdx < len(alternatives) {
				fallbackModel := alternatives[fallbackIdx]
				fallbackIdx++
				log.Printf("[AUTO-CONTINUE] Trying fallback %s for continuation...", fallbackModel.ID)
				newContResp := g.forwardRequestInternal(r.Context(), client, r, fallbackModel, newBody, true, nil)
				if newContResp.Err == nil && newContResp.Status == 200 {
					// Try to parse the fallback response
					var fbMap map[string]interface{}
					if err := json.Unmarshal(newContResp.Body, &fbMap); err != nil {
						continue
					}
					fbChoices, ok := fbMap["choices"].([]interface{})
					if !ok || len(fbChoices) == 0 {
						continue
					}
					fbChoice, ok := fbChoices[0].(map[string]interface{})
					if !ok {
						continue
					}
					fbMsg, ok := fbChoice["message"].(map[string]interface{})
					if !ok {
						continue
					}
					fbContent, _ := fbMsg["content"].(string)
					if len(fbContent) >= 50 {
						// Found a good fallback
						currentModel = fallbackModel
						contResp = newContResp
						contContent = fbContent
						contRespMap = fbMap
						contChoice = fbChoice
						log.Printf("[AUTO-CONTINUE] Fallback %s produced good continuation (%d chars)", fallbackModel.ID, len(fbContent))
						break
					}
					log.Printf("[AUTO-CONTINUE] Fallback %s content too short (%d chars), trying next...", fallbackModel.ID, len(fbContent))
				}
			}
			// If all fallbacks failed too, check if we still got content
			if contContent == "" || len(contContent) < 50 {
				log.Printf("[AUTO-CONTINUE] All fallbacks for this iteration produced too-short content, stopping.")
				break
			}
		}

		// Inject continuation marker
		accumulatedContent.WriteString(fmt.Sprintf("\n\n[Continued with Model: %s | Provider: %s]\n\n", currentModel.ID, currentModel.Provider))
		accumulatedContent.WriteString(contContent)

		// Update finish_reason from the continuation
		newFinishReason := ""
		if fr, ok := contChoice["finish_reason"]; ok && fr != nil {
			newFinishReason = fmt.Sprintf("%v", fr)
		}

		// Update the original choice's message content and finish_reason
		msg["content"] = accumulatedContent.String()
		choice["finish_reason"] = contChoice["finish_reason"]

		// Re-serialize the response
		if merged, err := json.Marshal(resp); err == nil {
			currentRespBody = merged
		}

		// Check if we need to continue again
		if !g.isResponseTruncated(contResp.Body) {
			log.Printf("[AUTO-CONTINUE] Continuation complete with finish_reason: %s", newFinishReason)
			break
		}
	}

	// Ensure we have enough total content
	if accumulatedContent.Len() < 100 {
		log.Printf("[AUTO-CONTINUE] Total response too short after all continuations (%d chars), returning first response instead", accumulatedContent.Len())
		return firstRespBody
	}

	return currentRespBody
}

type continuationStream struct {
	g                 *Gateway
	client            *http.Client
	req               *http.Request
	model             engine.ModelCandidate
	alternatives      []engine.ModelCandidate
	fallbackIdx       int
	originalBody      []byte
	currentStream     io.ReadCloser
	reader            *bufio.Reader
	accumulatedText   strings.Builder
	finishReason      string
	eofReached        bool
	err               error
	buffer            bytes.Buffer
	continuationCount int
	firstChunkParsed  bool
	prefixSent        bool
	isToolCall        bool
	finishReasonSent  bool
	lastAccumulatedLen int
	failedInRequest    map[string]bool // modelID -> true if failed in this request
	lastActivity       time.Time
	
	lineChan chan result
	stopChan chan struct{}
}

type result struct {
	line string
	err  error
}

func (g *Gateway) newContinuationStream(client *http.Client, r *http.Request, model engine.ModelCandidate, originalBody []byte, firstStream io.ReadCloser, alternatives []engine.ModelCandidate) *continuationStream {
	s := &continuationStream{
		g:             g,
		client:        client,
		req:           r,
		model:         model,
		alternatives:  alternatives,
		originalBody:  originalBody,
		currentStream: firstStream,
		reader:        bufio.NewReader(firstStream),
		lastAccumulatedLen: 0,
		failedInRequest:    make(map[string]bool),
		lastActivity:       time.Now(),
		lineChan:           make(chan result, 100),
		stopChan:           make(chan struct{}),
	}
	go s.runReader(s.stopChan)
	return s
}

func (s *continuationStream) runReader(stop chan struct{}) {
	for {
		line, err := s.reader.ReadString('\n')
		select {
		case <-stop:
			return
		case s.lineChan <- result{line, err}:
		}
		if err != nil {
			return
		}
	}
}

func (s *continuationStream) resetReader() {
	close(s.stopChan)
	s.stopChan = make(chan struct{})
	s.reader = bufio.NewReader(s.currentStream)
	go s.runReader(s.stopChan)
}

func (s *continuationStream) injectTextChunk(text string) {
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   s.model.ID,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": text,
				},
			},
		},
	}
	if cleaned, err := json.Marshal(chunk); err == nil {
		s.buffer.WriteString("data: " + string(cleaned) + "\n\n")
	}
}

func (s *continuationStream) injectFinishReasonChunk(fr string) {
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   s.model.ID,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": fr,
			},
		},
	}
	if cleaned, err := json.Marshal(chunk); err == nil {
		s.buffer.WriteString("data: " + string(cleaned) + "\n\n")
	}
}

func (s *continuationStream) Read(p []byte) (int, error) {
	// If there are bytes in the buffer, read them first
	if s.buffer.Len() > 0 {
		return s.buffer.Read(p)
	}

	if s.eofReached {
		if s.err != nil {
			return 0, s.err
		}
		return 0, io.EOF
	}

	// Read lines from the current stream until we can fill the buffer or hit EOF
	for s.buffer.Len() == 0 {
		var line string
		var err error
		
		timeout := 15 * time.Second
		if s.g.WatchdogTimeout > 0 {
			timeout = s.g.WatchdogTimeout
		}

		select {
		case res := <-s.lineChan:
			line = res.line
			err = res.err
			s.lastActivity = time.Now()
		case <-time.After(timeout):
			// Heartbeat: send empty comment to keep connection open
			s.buffer.WriteString(":\n\n")
			s.lastActivity = time.Now()
			return s.buffer.Read(p)
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed != "" {
			if strings.HasPrefix(trimmed, "data: ") {
				data := trimmed[6:]
				if data != "[DONE]" {
					var chunk map[string]interface{}
					if err := json.Unmarshal([]byte(data), &chunk); err == nil {
						s.firstChunkParsed = true
						if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
							if choice, ok := choices[0].(map[string]interface{}); ok {
								if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
									s.finishReason = fr
									s.finishReasonSent = true
								}
								if delta, ok := choice["delta"].(map[string]interface{}); ok {
									if content, ok := delta["content"].(string); ok {
										s.accumulatedText.WriteString(content)
										if strings.Contains(s.accumulatedText.String(), "<tool_call") {
											s.isToolCall = true
										}
									}
									if rc, ok := delta["reasoning_content"].(string); ok {
										s.accumulatedText.WriteString(rc)
									}
									if r, ok := delta["reasoning"].(string); ok {
										s.accumulatedText.WriteString(r)
									}
									if _, ok := delta["tool_calls"]; ok {
										s.isToolCall = true
									}
								}
							}
						}
					}
				}
				s.buffer.WriteString(line)
			} else {
				// Pass through comments/etc, but detect JSON errors
				lineTrimmed := strings.TrimSpace(trimmed)
				if lineTrimmed != "" {
					isError := (strings.Contains(lineTrimmed, "\"error\"") || 
						strings.Contains(lineTrimmed, "\"success\":false") || 
						strings.Contains(lineTrimmed, "\"code\":")) && 
						strings.Contains(lineTrimmed, "{")
					
					if isError || (s.accumulatedText.Len() == 0 && (strings.Contains(lineTrimmed, "Authorization") || strings.Contains(lineTrimmed, "auth"))) {
						log.Printf("[STREAM-ERROR] Provider %s sent invalid SSE line: %s", s.model.Provider, lineTrimmed)
						s.g.DemoteModel(s.model.ID)
						if strings.Contains(lineTrimmed, "Authorization") || strings.Contains(lineTrimmed, "auth") || strings.Contains(lineTrimmed, "key") {
							s.g.recordProviderAuthFail(s.model.Provider)
						}
						s.currentStream.Close()
						err = fmt.Errorf("provider error: %s", lineTrimmed)
					} else {
						s.buffer.WriteString(line)
					}
				} else {
					s.buffer.WriteString(line)
				}
			}
		}

		if err != nil {
			// Current stream ended or failed
			s.currentStream.Close()
			log.Printf("[STREAM-END] Stream from %s(%s) ended with: %v", s.model.ID, s.model.Provider, err)

			isFailedToolCall := s.isToolCall && err != io.EOF && s.finishReason != "tool_calls" && s.finishReason != "stop" && s.finishReason != ""
			if ((s.accumulatedText.Len() == 0 && !s.isToolCall) || isFailedToolCall || (s.finishReason == "" && s.accumulatedText.Len() > 0)) && s.continuationCount < 20 {
				log.Printf("[AUTO-CONTINUE] Stream failed, retrying global fan-out...")
				
				s.g.DemoteModel(s.model.ID)
				if s.failedInRequest == nil {
					s.failedInRequest = make(map[string]bool)
				}
				s.failedInRequest[s.model.ID] = true

				success := false
				allModels := s.g.GetModels()
				validModels := s.g.filterCandidates(allModels)
				
				for batch := 0; batch < 15; batch++ {
					fanOutSize := s.g.FanOutSize
					if fanOutSize < 1 { fanOutSize = 1 }
					if fanOutSize > 50 { fanOutSize = 50 }
					
					var fanModels []engine.ModelCandidate
					indices := rand.Perm(len(validModels))
					for _, idx := range indices {
						candidate := validModels[idx]
						if s.failedInRequest[candidate.ID] { continue }
						fanModels = append(fanModels, candidate)
						if len(fanModels) >= fanOutSize { break }
					}
					
					if len(fanModels) == 0 { break }

					type fanRes struct {
						model engine.ModelCandidate
						resp  *ProxyResponse
					}
					fanCh := make(chan fanRes, len(fanModels))
					for _, m := range fanModels {
						go func(candidate engine.ModelCandidate) {
							mc := tokdiet.NewClient(90 * time.Second)
							fanCh <- fanRes{
								model: candidate,
								resp:  s.g.forwardRequestInternal(s.req.Context(), mc, s.req, candidate, s.originalBody, true, nil),
							}
						}(m)
					}
					
					var winner *fanRes
					for j := 0; j < len(fanModels); j++ {
						res := <-fanCh
						if res.resp.Err == nil && res.resp.Status == 200 && res.resp.Stream != nil {
							if winner == nil { winner = &res } else { res.resp.Stream.Close() }
						}
					}

					if winner != nil {
						s.model = winner.model
						s.currentStream = winner.resp.Stream
						s.finishReason = ""
						s.finishReasonSent = false
						s.firstChunkParsed = false
						s.continuationCount++
						s.prefixSent = false
						s.resetReader()
						s.injectTextChunk(fmt.Sprintf("\n\n[Switched to Model: %s | Provider: %s via Global Random Retry]\n\n", s.model.ID, s.model.Provider))
						success = true
						break
					}
				}

				if success { return s.buffer.Read(p) }
				s.injectTextChunk("\n\n[FreeLLM Proxy Error: Connection closed after multiple retries.]\n\n")
			}

			isTruncated := false
			if s.finishReason == "" && s.accumulatedText.Len() > 0 {
				isTruncated = true
				s.g.DemoteModel(s.model.ID)
			} else if s.finishReason == "length" || s.finishReason == "max_tokens" {
				isTruncated = true
			}

			fullText := s.accumulatedText.String()
			if strings.Contains(fullText, "<tool_call") && !strings.Contains(fullText, "</tool_call") && !strings.Contains(fullText, "</function>") {
				isTruncated = true
				s.isToolCall = true 
			}

			if isTruncated && s.continuationCount < 5 {
				s.lastAccumulatedLen = s.accumulatedText.Len()
				s.continuationCount++
				
				var currentPayload map[string]interface{}
				if json.Unmarshal(s.originalBody, &currentPayload) == nil {
					if originalMsgs, ok := currentPayload["messages"].([]interface{}); ok {
						newMsgs := make([]interface{}, len(originalMsgs))
						copy(newMsgs, originalMsgs)
						newMsgs = append(newMsgs, map[string]interface{}{
							"role":    "assistant",
							"content": s.accumulatedText.String(),
						})
						newMsgs = append(newMsgs, map[string]interface{}{
							"role":    "user",
							"content": "continue",
						})

						newPayload := make(map[string]interface{})
						for k, v := range currentPayload { newPayload[k] = v }
						newPayload["messages"] = newMsgs
						newPayload["max_tokens"] = 8192
						newPayload["stream"] = true

						if newBody, err := json.Marshal(newPayload); err == nil {
							s.originalBody = newBody
							fanOutSize := 3
							remainingAlts := s.alternatives[s.fallbackIdx:]
							if fanOutSize > len(remainingAlts) { fanOutSize = len(remainingAlts) }
							fanModels := remainingAlts
							if len(fanModels) > fanOutSize { fanModels = remainingAlts[:fanOutSize] }
							s.fallbackIdx += len(fanModels)
							
							type fanRes struct {
								model engine.ModelCandidate
								resp  *ProxyResponse
							}
							fanCh := make(chan fanRes, len(fanModels)+1)
							go func() {
								mc := tokdiet.NewClient(0)
								fanCh <- fanRes{model: s.model, resp: s.g.forwardRequestInternal(s.req.Context(), mc, s.req, s.model, newBody, true, nil)}
							}()
							for _, m := range fanModels {
								go func(candidate engine.ModelCandidate) {
									mc := tokdiet.NewClient(0)
									fanCh <- fanRes{model: candidate, resp: s.g.forwardRequestInternal(s.req.Context(), mc, s.req, candidate, newBody, true, nil)}
								}(m)
							}
							
							var winner *fanRes
							for j := 0; j < len(fanModels)+1; j++ {
								res := <-fanCh
								if res.resp.Err == nil && res.resp.Status == 200 && res.resp.Stream != nil {
									if winner == nil { winner = &res } else { res.resp.Stream.Close() }
								}
							}

							if winner != nil {
								s.model = winner.model
								s.currentStream = winner.resp.Stream
								s.finishReason = ""
								s.injectTextChunk(fmt.Sprintf("\n\n[Continued with Model: %s | Provider: %s]\n\n", s.model.ID, s.model.Provider))
								s.prefixSent = false
								s.firstChunkParsed = false
								s.isToolCall = false
								s.finishReasonSent = false
								s.resetReader()
								s.continuationCount++
								return s.buffer.Read(p)
							}
						}
					}
				}
			}

			if s.accumulatedText.Len() < 100 && s.continuationCount > 0 {
				s.g.DemoteModel(s.model.ID)
				success := false
				allModels := s.g.GetModels()
				validModels := s.g.filterCandidates(allModels)
				for batch := 0; batch < 5 && !success && len(validModels) > 0; batch++ {
					fanOutSize := s.g.FanOutSize
					if fanOutSize < 1 { fanOutSize = 1 }
					if fanOutSize > 50 { fanOutSize = 50 }
					var fanModels []engine.ModelCandidate
					indices := rand.Perm(len(validModels))
					for _, idx := range indices {
						candidate := validModels[idx]
						if candidate.ID == s.model.ID && candidate.Provider == s.model.Provider { continue }
						fanModels = append(fanModels, candidate)
						if len(fanModels) >= fanOutSize { break }
					}
					if len(fanModels) == 0 { break }
					type fanRes struct {
						model engine.ModelCandidate
						resp  *ProxyResponse
					}
					fanCh := make(chan fanRes, len(fanModels))
					for _, m := range fanModels {
						go func(cand engine.ModelCandidate) {
							mc := tokdiet.NewClient(0)
							fanCh <- fanRes{model: cand, resp: s.g.forwardRequestInternal(s.req.Context(), mc, s.req, cand, s.originalBody, true, nil)}
						}(m)
					}
					var winner *fanRes
					for j := 0; j < len(fanModels); j++ {
						res := <-fanCh
						if res.resp.Err == nil && res.resp.Status == 200 && res.resp.Stream != nil {
							if winner == nil { winner = &res } else { res.resp.Stream.Close() }
						}
					}
					if winner != nil {
						s.model = winner.model
						s.currentStream = winner.resp.Stream
						s.finishReason = ""
						s.prefixSent = false
						s.firstChunkParsed = false
						s.isToolCall = false
						s.finishReasonSent = false
						s.fallbackIdx = 0
						s.resetReader()
						s.injectTextChunk(fmt.Sprintf("\n\n[Continued with Model: %s | Provider: %s]\n\n", s.model.ID, s.model.Provider))
						s.continuationCount++
						success = true
						return s.buffer.Read(p)
					}
				}
				if !success {
					s.injectTextChunk(fmt.Sprintf("\n\n[FreeLLM Proxy Error: Response too short.]\n\n"))
					s.err = fmt.Errorf("response too short (%d chars)", s.accumulatedText.Len())
					s.eofReached = true
					return s.buffer.Read(p)
				}
			}

			s.eofReached = true
			s.err = err
			if err == io.EOF {
				if !s.isToolCall {
					// Check full text one last time for tool call markers
					fullText := s.accumulatedText.String()
					lowered := strings.ToLower(fullText)
					isTool := strings.Contains(lowered, "[tool_call]") ||
						strings.Contains(lowered, "<tool_call") ||
						strings.Contains(lowered, "<longcat_tool_call") ||
						strings.Contains(lowered, "```bash") ||
						strings.Contains(lowered, "```python")

					if !isTool {
						// Append prefix at the end of the stream
						s.injectTextChunk(fmt.Sprintf("\n\n[Model: %s | Provider: %s]", s.model.ID, s.model.Provider))
					}
				}
				s.g.MarkProven(s.model.ID, s.model.Provider)
				if s.g.ShuffleEnabled {
					s.g.SetModelPrimary(s.model.ID)
				}
			}
			if !s.finishReasonSent {
				s.injectFinishReasonChunk("stop")
				s.finishReasonSent = true
			}
			return s.buffer.Read(p)
		}
	}
	return s.buffer.Read(p)
}

func (s *continuationStream) Close() error {
	close(s.stopChan)
	if s.currentStream != nil {
		return s.currentStream.Close()
	}
	return nil
}

// recordProviderAuthFail tracks consecutive 401/402/403 failures per provider.
func (g *Gateway) recordProviderAuthFail(provider string) {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	if g.providerCooldown == nil {
		g.providerCooldown = make(map[string]time.Time)
	}
	g.providerCooldown[provider] = time.Now().Add(10 * time.Minute)
	log.Printf("[ROUTER] Provider %s cooling down due to auth failure", provider)
}

// Complete provider URL mapping
func (g *Gateway) getProviderURL(modelID, provider string) string {
	// Check for model-specific overrides (e.g. mapping deepseek models to siliconflow)
	if provider == "deepseek" && os.Getenv("DEEPSEEK_API_KEY") == "" {
		if os.Getenv("SILICONFLOW_API_KEY") != "" {
			// Change provider and potentially model ID
			provider = "siliconflow"
			if modelID == "deepseek-chat" {
				modelID = "deepseek-ai/DeepSeek-V3"
			} else if modelID == "deepseek-reasoner" {
				modelID = "deepseek-ai/DeepSeek-R1"
			}
		}
	}

	switch provider {
	case "openrouter":
		return "https://openrouter.ai/api/v1/chat/completions"
	case "groq":
		return "https://api.groq.com/openai/v1/chat/completions"
	case "github":
		return "https://models.inference.ai.azure.com/chat/completions"
	case "deepinfra":
		return "https://api.deepinfra.com/v1/openai/chat/completions"
	case "cerebras":
		return "https://api.cerebras.ai/v1/chat/completions"
	case "nvidia", "nvidia_nim":
		return "https://integrate.api.nvidia.com/v1/chat/completions"
	case "huggingface":
		return "https://api-inference.huggingface.co/models/" + modelID + "/v1/chat/completions"
	case "mistral":
		return "https://api.mistral.ai/v1/chat/completions"
	case "codestral":
		return "https://codestral.mistral.ai/v1/chat/completions"
	case "cohere":
		return "https://api.cohere.ai/v2/chat"
	case "sambanova":
		return "https://api.sambanova.ai/v1/chat/completions"
	case "fireworks":
		return "https://api.fireworks.ai/inference/v1/chat/completions"
	case "hyperbolic":
		return "https://api.hyperbolic.xyz/v1/chat/completions"
	case "cloudflare":
		aid := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
		if aid == "" {
			return ""
		}
		return "https://api.cloudflare.com/client/v4/accounts/" + aid + "/ai/v1/chat/completions"
	case "opencode_zen":
		return "https://opencode.ai/zen/v1/chat/completions"
	case "ollama":
		return "http://localhost:11434/v1/chat/completions"
	case "lm_studio":
		return "http://localhost:1234/v1/chat/completions"
	case "anthropic":
		return "https://api.anthropic.com/v1/messages"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/models/" + modelID + ":streamGenerateContent"
		case "openai":
		return "https://api.openai.com/v1/chat/completions"
	case "siliconflow":
		return "https://api.siliconflow.cn/v1/chat/completions"
	case "together":
		return "https://api.together.xyz/v1/chat/completions"
	case "novita":
		return "https://api.novita.ai/v3/chat/completions"
	case "nebius":
		return "https://api.studio.nebius.ai/v1/chat/completions"
	case "deepseek":
		return "https://api.deepseek.com/v1/chat/completions"
	case "ai21":
		return "https://api.ai21.com/v1/chat/completions"
	case "replicate":
		return "https://api.replicate.com/v1/chat/completions"
	case "dashscope":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	case "minimax":
		return "https://api.minimax.chat/v1/chat/completions"
	case "moonshot":
		return "https://api.moonshot.cn/v1/chat/completions"
	case "stepfun":
		return "https://api.stepfun.com/v1/chat/completions"
	case "zhipu":
		return "https://open.bigmodel.cn/api/paas/v4/chat/completions"
	case "internlm":
		return "https://internlm-chat.intern-ai.org.cn/v1/chat/completions"
	case "arcee":
		return "https://api.arcee.ai/v1/chat/completions"
	case "perplexity":
		return "https://api.perplexity.ai/v1/chat/completions"
	case "xai":
		return "https://api.x.ai/v1/chat/completions"
	case "hunyuan":
		return "https://api.hunyuan.cloud.tencent.com/v1/chat/completions"
	case "kluster":
		return "https://api.kluster.ai/v1/chat/completions"
	case "llm7":
		return "https://api.llm7.io/v1/chat/completions"
	case "lepton":
		return "https://api.lepton.ai/chat/completions"
	case "pollinations":
		return "https://text.pollinations.ai/openai/chat/completions"
	}
	return ""
}

// API key resolution for all providers (with NVIDIA fallback)
func (g *Gateway) getAPIKey(provider string) string {
	key := ""
	switch provider {
	case "openrouter":
		key = os.Getenv("OPENROUTER_API_KEY")
	case "groq":
		key = os.Getenv("GROQ_API_KEY")
	case "github":
		key = os.Getenv("GITHUB_TOKEN")
	case "deepinfra":
		key = os.Getenv("DEEPINFRA_API_KEY")
	case "cerebras":
		key = os.Getenv("CEREBRAS_API_KEY")
	case "huggingface":
		key = os.Getenv("HUGGINGFACE_API_KEY")
	case "nvidia", "nvidia_nim":
		key = os.Getenv("NVIDIA_NIM_API_KEY")
		if key == "" {
			key = os.Getenv("NVIDIA_API_KEY")
		}
	case "mistral":
		key = os.Getenv("MISTRAL_API_KEY")
	case "codestral":
		key = os.Getenv("CODESTRAL_API_KEY")
	case "cohere":
		key = os.Getenv("COHERE_API_KEY")
	case "sambanova":
		key = os.Getenv("SAMBANOVA_API_KEY")
	case "fireworks":
		key = os.Getenv("FIREWORKS_API_KEY")
	case "hyperbolic":
		key = os.Getenv("HYPERBOLIC_API_KEY")
	case "cloudflare":
		key = os.Getenv("CLOUDFLARE_API_KEY")
	case "opencode_zen":
		key = os.Getenv("OPENCODE_ZEN_API_KEY")
	case "anthropic":
		key = os.Getenv("ANTHROPIC_API_KEY")
	case "gemini":
		key = os.Getenv("GEMINI_API_KEY")
	case "siliconflow":
		key = os.Getenv("SILICONFLOW_API_KEY")
	case "together":
		key = os.Getenv("TOGETHER_API_KEY")
	case "novita":
		key = os.Getenv("NOVITA_API_KEY")
	case "nebius":
		key = os.Getenv("NEBIUS_API_KEY")
	case "deepseek":
		key = os.Getenv("DEEPSEEK_API_KEY")
	case "ai21":
		key = os.Getenv("AI21_API_KEY")
	case "replicate":
		key = os.Getenv("REPLICATE_API_TOKEN")
	case "dashscope":
		key = os.Getenv("DASHSCOPE_API_KEY")
	case "minimax":
		key = os.Getenv("MINIMAX_API_KEY")
	case "moonshot":
		key = os.Getenv("MOONSHOT_API_KEY")
	case "stepfun":
		key = os.Getenv("STEPFUN_API_KEY")
	case "zhipu":
		key = os.Getenv("ZHIPU_API_KEY")
	case "internlm":
		key = os.Getenv("INTERNLM_API_KEY")
	case "arcee":
		key = os.Getenv("ARCEE_API_KEY")
	case "perplexity":
		key = os.Getenv("PERPLEXITY_API_KEY")
	case "xai":
		key = os.Getenv("XAI_API_KEY")
	case "hunyuan":
		key = os.Getenv("HUNYUAN_API_KEY")
	case "kluster":
		key = os.Getenv("KLUSTER_API_KEY")
	case "llm7":
		key = os.Getenv("LLM7_API_KEY")
	case "lepton":
		key = os.Getenv("LEPTON_API_KEY")
	case "pollinations":
		key = os.Getenv("POLLINATIONS_API_KEY")
	}

	// Dynamic provider fallback:
	// If deepseek key is missing, try siliconflow key for deepseek models.
	if key == "" && provider == "deepseek" {
		sfKey := os.Getenv("SILICONFLOW_API_KEY")
		if sfKey != "" {
			return sfKey
		}
	}

	return key
}

func (g *Gateway) transformRequest(req *http.Request, provider string, stream bool) {
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", "FreeLLM/1.0")
	apiKey := g.getAPIKey(provider)
	if apiKey == "" {
		return
	}
	if provider == "anthropic" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	type ME struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	resp := struct {
		Object string `json:"object"`
		Data   []ME   `json:"data"`
	}{
		Object: "list",
		Data: []ME{
			{"free-llm", "model", time.Now().Unix(), "freellm"},
			{"free-llm-fallback", "model", time.Now().Unix(), "freellm"},
			{"free-llm-plain", "model", time.Now().Unix(), "freellm"},
			{"gpt-4o", "model", time.Now().Unix(), "freellm"},
			{"gpt-4o-mini", "model", time.Now().Unix(), "freellm"},
			{"gpt-4-turbo", "model", time.Now().Unix(), "freellm"},
			{"gpt-3.5-turbo", "model", time.Now().Unix(), "freellm"},
			{"o1", "model", time.Now().Unix(), "freellm"},
			{"o1-mini", "model", time.Now().Unix(), "freellm"},
			{"o3-mini", "model", time.Now().Unix(), "freellm"},
			{"claude-3-5-sonnet-20241022", "model", time.Now().Unix(), "freellm"},
			{"claude-3-7-sonnet-20250219", "model", time.Now().Unix(), "freellm"},
			{"claude-sonnet-4-20250514", "model", time.Now().Unix(), "freellm"},
			{"claude-opus-4-20250514", "model", time.Now().Unix(), "freellm"},
			{"claude-haiku-4-20250514", "model", time.Now().Unix(), "freellm"},
			{"claude-3-5-haiku-20241022", "model", time.Now().Unix(), "freellm"},
			{"gemini-3-flash", "model", time.Now().Unix(), "freellm"},
			{"gemini-3.5-flash", "model", time.Now().Unix(), "freellm"},
			{"gemini-3.1-pro", "model", time.Now().Unix(), "freellm"},
				},
	}
	for _, m := range g.GetModels() {
		resp.Data = append(resp.Data, ME{m.ID, "model", time.Now().Unix(), m.Provider})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) RestoreQueue() {
	if g.DB == nil {
		return
	}
	pending, err := db.GetPendingRequests(g.DB)
	if err != nil || len(pending) == 0 {
		return
	}
	log.Printf("Restoring %d pending requests...", len(pending))
	for _, p := range pending {
		req, _ := http.NewRequest(p.Method, p.URL, bytes.NewBuffer(p.Body))
		job := &RequestJob{
			Request:  req,
			Response: make(chan *ProxyResponse, 1),
			Ctx:      context.Background(),
			DBID:     p.ID,
		}
		select {
		case g.Queue <- job:
		default:
		}
	}
}

func (g *Gateway) logUsage(modelID string, reqBody, respBody []byte) {
	if g.DB == nil {
		return
	}
	pt, ct := len(reqBody)/4, len(respBody)/4
	if respBody != nil {
		var r struct {
			Usage struct {
				P int `json:"prompt_tokens"`
				C int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(respBody, &r) == nil && r.Usage.P > 0 {
			pt, ct = r.Usage.P, r.Usage.C
		}
	} else {
		pt, ct = 0, 0
	}
	db.LogUsage(g.DB, modelID, pt, ct)
}

// writeJSONError sends an OpenAI-compatible error response
func writeJSONError(w http.ResponseWriter, status int, message, code, param string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "server_error",
			"param":   param,
			"code":    code,
		},
	})
}

// SetModelPrimary moves a model to position 0 (top of rankings)
func (g *Gateway) SetModelPrimary(modelID string) {
	g.mu.Lock()
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			if i == 0 {
				g.mu.Unlock()
				return
			}
			model := g.RankedModels[i]
			copy(g.RankedModels[1:i+1], g.RankedModels[0:i])
			g.RankedModels[0] = model
			g.mu.Unlock()
			g.persistRankings()
			return
			}
		}
	g.mu.Unlock()
}

// PromoteModel moves a model from fallback into the primary group
func (g *Gateway) PromoteModel(modelID string) {
	g.mu.Lock()
	idx := -1
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			idx = i
			break
		}
	}
	if idx < 0 || idx < g.PrimaryCount {
		g.mu.Unlock()
		return // already primary or not found
	}
	// Swap with the last primary model
	lastPrimary := g.PrimaryCount - 1
	g.RankedModels[idx], g.RankedModels[lastPrimary] = g.RankedModels[lastPrimary], g.RankedModels[idx]
	g.mu.Unlock()
	g.persistRankings()
}

// DemoteModel moves a model to the end of the rankings
func (g *Gateway) DemoteModel(modelID string) {
	g.mu.Lock()
	idx := -1
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			idx = i
			break
		}
	}
	if idx < 0 {
		g.mu.Unlock()
		return
	}
	model := g.RankedModels[idx]
	// Remove from current position
	g.RankedModels = append(g.RankedModels[:idx], g.RankedModels[idx+1:]...)
	// Append to end
	g.RankedModels = append(g.RankedModels, model)
	g.mu.Unlock()
	log.Printf("[ROUTER] Demoted model %s to end of rankings", modelID)
	g.persistRankings()
}

// SetAsFallback moves a model to the first position in the fallback group (position = PrimaryCount)
func (g *Gateway) SetAsFallback(modelID string) {
	g.mu.Lock()
	idx := -1
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			idx = i
			break
		}
	}
	if idx < 0 {
		g.mu.Unlock()
		return // not found
	}
	target := g.PrimaryCount
	if target >= len(g.RankedModels) {
		target = len(g.RankedModels) - 1
	}
	if idx == target {
		g.mu.Unlock()
		return // already at target position
	}
	model := g.RankedModels[idx]
	// Shift items between idx and target
	if idx < target {
		// Moving down: shift items up
		copy(g.RankedModels[idx:target], g.RankedModels[idx+1:target+1])
	} else {
		// Moving up: shift items down
		copy(g.RankedModels[target+1:idx+1], g.RankedModels[target:idx])
	}
	g.RankedModels[target] = model
	g.mu.Unlock()
	g.persistRankings()
}

// MoveModelUp moves a model one position higher in the rankings
func (g *Gateway) MoveModelUp(modelID string) {
	g.mu.Lock()
	for i, m := range g.RankedModels {
		if m.ID == modelID && i > 0 {
			g.RankedModels[i], g.RankedModels[i-1] = g.RankedModels[i-1], g.RankedModels[i]
			g.mu.Unlock()
			g.persistRankings()
			return
		}
	}
	g.mu.Unlock()
}

// MoveModelDown moves a model one position lower in the rankings
func (g *Gateway) MoveModelDown(modelID string) {
	g.mu.Lock()
	for i, m := range g.RankedModels {
		if m.ID == modelID && i < len(g.RankedModels)-1 {
			g.RankedModels[i], g.RankedModels[i+1] = g.RankedModels[i+1], g.RankedModels[i]
			g.mu.Unlock()
			g.persistRankings()
			return
		}
	}
	g.mu.Unlock()
}


// RouteMessage routes a text message through the FreeLLM gateway.
// Used by the A2A server to process agent tasks via the LLM routing pipeline.
func (g *Gateway) RouteMessage(ctx context.Context, message string, model string) (string, error) {
	// Build an internal chat completion request
	payload := map[string]interface{}{
		"model":      model,
		"messages":   []map[string]interface{}{{"role": "user", "content": message}},
		"max_tokens": 1024,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// Create internal HTTP request to our own server
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", g.Port)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-FreeLLM-Priority", "high")
	req.Header.Set("Authorization", "Bearer sk-freellm")

	resp, err := g.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("internal request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("internal request returned status %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	// Parse the OpenAI response to extract content
	var oaiResp struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(oaiResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	var content string
	if err := json.Unmarshal(oaiResp.Choices[0].Message.Content, &content); err != nil {
		// Content might be an array of content blocks
		return string(oaiResp.Choices[0].Message.Content), nil
	}

	return content, nil
}

// transformPlaintextToolCalls parses common plaintext tool call patterns and converts them
// into formal OpenAI tool_calls objects if found in the assistant message content.
func (g *Gateway) transformPlaintextToolCalls(respBody []byte) []byte {
	var resp map[string]interface{}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return respBody
	}
	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return respBody
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return respBody
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return respBody
	}

	content, ok := msg["content"].(string)
	if !ok || content == "" {
		return respBody
	}

	var toolCalls []interface{}

	// 1. Standard [TOOL_CALL] ... [/TOOL_CALL]
	reStd := regexp.MustCompile(`(?s)\[TOOL_CALL\]\s*(.*?)\s*\[/TOOL_CALL\]`)
	matchesStd := reStd.FindAllStringSubmatch(content, -1)
	for _, m := range matchesStd {
		inner := m[1]
		// Try JSON first
		var tool struct {
			Tool string                 `json:"tool"`
			Args map[string]interface{} `json:"args"`
		}
		if err := json.Unmarshal([]byte(inner), &tool); err == nil {
			argsJson, _ := json.Marshal(tool.Args)
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   fmt.Sprintf("call_%d", rand.Intn(1000000)),
				"type": "function",
				"function": map[string]interface{}{
					"name":      tool.Tool,
					"arguments": string(argsJson),
				},
			})
		} else {
			// Try pseudo-code format: {tool => "name", args => { --k "v" }}
			reName := regexp.MustCompile(`tool\s*=>\s*"(.*?)"`)
			nameMatch := reName.FindStringSubmatch(inner)
			if nameMatch != nil {
				name := nameMatch[1]
				args := make(map[string]interface{})
				reArgsBlock := regexp.MustCompile(`(?s)args\s*=>\s*\{(.*?)\}`)
				argsBlockMatch := reArgsBlock.FindStringSubmatch(inner)
				if argsBlockMatch != nil {
					// Parse --key "value"
					reKV := regexp.MustCompile(`--([\w-]+)\s+"(.*?)"`)
					kvMatches := reKV.FindAllStringSubmatch(argsBlockMatch[1], -1)
					for _, kv := range kvMatches {
						args[kv[1]] = kv[2]
					}
				}
				argsJson, _ := json.Marshal(args)
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   fmt.Sprintf("call_%d", rand.Intn(1000000)),
					"type": "function",
					"function": map[string]interface{}{
						"name":      name,
						"arguments": string(argsJson),
					},
				})
			}
		}
	}

	// 2. XML <tool_call> { ... } </tool_call>
	reXml := regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)
	matchesXml := reXml.FindAllStringSubmatch(content, -1)
	for _, m := range matchesXml {
		var tool struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &tool); err == nil {
			argsJson, _ := json.Marshal(tool.Arguments)
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   fmt.Sprintf("call_%d", rand.Intn(1000000)),
				"type": "function",
				"function": map[string]interface{}{
					"name":      tool.Name,
					"arguments": string(argsJson),
				},
			})
		}
	}

	// 3. Longcat <tool_call>=toolname <arg_key>...</arg_key> <arg_value>...</arg_value> ... </tool_call>
	reLongcat := regexp.MustCompile(`(?s)<tool_call>=(\w+)\s*(.*?)\s*</tool_call>`)
	matchesLongcat := reLongcat.FindAllStringSubmatch(content, -1)
	for _, m := range matchesLongcat {
		name := m[1]
		args := make(map[string]interface{})
		reArgs := regexp.MustCompile(`(?s)<arg_key>(.*?)</arg_key>\s*<arg_value>(.*?)</arg_value>`)
		argMatches := reArgs.FindAllStringSubmatch(m[2], -1)
		for _, am := range argMatches {
			args[am[1]] = am[2]
		}
		argsJson, _ := json.Marshal(args)
		toolCalls = append(toolCalls, map[string]interface{}{
			"id":   fmt.Sprintf("call_%d", rand.Intn(1000000)),
			"type": "function",
			"function": map[string]interface{}{
				"name":      name,
				"arguments": string(argsJson),
			},
		})
		// Remove the matched text from the content
		content = strings.Replace(content, m[0], "", 1)
	}

	// 3b. Alternate Longcat <function=toolname> <parameter=key>value</parameter> </function>
	reFunction := regexp.MustCompile(`(?s)<function=([^>]+)>\s*(.*?)\s*</function>`)
	matchesFunction := reFunction.FindAllStringSubmatch(content, -1)
	for _, m := range matchesFunction {
		name := m[1]
		args := make(map[string]interface{})
		reArgs := regexp.MustCompile(`(?s)<parameter=([^>]+)>(.*?)</parameter>`)
		argMatches := reArgs.FindAllStringSubmatch(m[2], -1)
		for _, am := range argMatches {
			args[am[1]] = am[2]
		}
		argsJson, _ := json.Marshal(args)
		toolCalls = append(toolCalls, map[string]interface{}{
			"id":   fmt.Sprintf("call_%d", rand.Intn(1000000)),
			"type": "function",
			"function": map[string]interface{}{
				"name":      name,
				"arguments": string(argsJson),
			},
		})
		// Remove the matched text from the content
		content = strings.Replace(content, m[0], "", 1)
	}

	// 4. Minimax minimax:tool_call <invoke name="name"> <parameter name="p">v</parameter> </invoke> </minimax:tool_call>
	// Handle both minimax:tool_call and <minimax:tool_call> variants
	reMinimax := regexp.MustCompile(`(?s)(?:<)?minimax:tool_call(?:>)?\s*(.*?)\s*(?:</)?minimax:tool_call(?:>)?`)
	matchesMinimax := reMinimax.FindAllStringSubmatch(content, -1)
	for _, m := range matchesMinimax {
		reInvoke := regexp.MustCompile(`(?s)<invoke\s+name="(.*?)">(.*?)</invoke>`)
		invokeMatches := reInvoke.FindAllStringSubmatch(m[1], -1)
		for _, im := range invokeMatches {
			name := im[1]
			args := make(map[string]interface{})
			reParam := regexp.MustCompile(`(?s)<parameter\s+name="(.*?)">(.*?)</parameter>`)
			paramMatches := reParam.FindAllStringSubmatch(im[2], -1)
			for _, pm := range paramMatches {
				args[pm[1]] = pm[2]
			}
			argsJson, _ := json.Marshal(args)
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   fmt.Sprintf("call_%d", rand.Intn(1000000)),
				"type": "function",
				"function": map[string]interface{}{
					"name":      name,
					"arguments": string(argsJson),
				},
			})
		}
	}

	// 5. Triple-backtick bash/python blocks
	reCode := regexp.MustCompile("(?s)```(bash|python)\n(.*?)\n```")
	matchesCode := reCode.FindAllStringSubmatch(content, -1)
	for _, m := range matchesCode {
		lang := m[1]
		code := m[2]
		toolName := "bash"
		if lang == "python" {
			toolName = "python"
		}
		args := map[string]interface{}{
			"command": code,
		}
		argsJson, _ := json.Marshal(args)
		toolCalls = append(toolCalls, map[string]interface{}{
			"id":   fmt.Sprintf("call_%d", rand.Intn(1000000)),
			"type": "function",
			"function": map[string]interface{}{
				"name":      toolName,
				"arguments": string(argsJson),
			},
		})
	}

	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		choice["finish_reason"] = "tool_calls"
		msg["content"] = content // Update content with the tool calls stripped
	}

	if merged, err := json.Marshal(resp); err == nil {
		return merged
	}
	return respBody
}
