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
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xeipuuv/gojsonschema"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
)

type Gateway struct {
	Port int
	RankedModels engine.RankedModels
	mu sync.RWMutex
	Queue chan *RequestJob
	HighPriQueue chan *RequestJob
	MaxActive int
	DB *sql.DB
	PrimaryCount int
	Cache map[string][]byte
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
	Sessions *SessionTracker
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
}

type ProxyResponse struct {
	Status int
	Body []byte
	Header http.Header
	Err error
	Stream io.ReadCloser
	ModelID string
	Provider string
}

func NewGateway(maxActive int, database *sql.DB, port int) *Gateway {
	g := &Gateway{
		Port: port,
		Queue: make(chan *RequestJob, 20),
		HighPriQueue: make(chan *RequestJob, 200),
		MaxActive: 10,
		DB: database,
		PrimaryCount: 10,
		Cache: make(map[string][]byte),
		providerCooldown: make(map[string]time.Time),
		Client: &http.Client{Timeout: 30 * time.Second},
		preflightCache: make(map[string]preflightEntry),
		Sessions:   NewSessionTracker(),
		}
	go g.workerLoop()
	return g
}

func (g *Gateway) UpdateModels(models engine.RankedModels) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.RankedModels = models
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
	cacheKey := string(body)

	if g.Redis != nil {
		if val, err := g.Redis.Get(ctx, cacheKey).Bytes(); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-FreeLLM-Cache", "HIT (Redis)")
			w.Write(val)
			return
		}
	} else {
		g.cacheMu.RLock()
		if cached, ok := g.Cache[cacheKey]; ok {
			g.cacheMu.RUnlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-FreeLLM-Cache", "HIT")
			w.Write(cached)
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
	job := &RequestJob{Request: r, Response: make(chan *ProxyResponse, 1), Ctx: ctx, DBID: dbID, IsStream: isStream}
	// Bypass queue: call processJob directly for immediate processing
	// This avoids queue congestion from background agent traffic
	go g.processJob(job)

	wroteHeader := false
	log.Printf("[PROXY] Waiting for job response (stream=%v)...", isStream)
	var resp *ProxyResponse
	// Keepalive loop: wait for response while sending periodic pings
	// to prevent client or intermediary timeouts
	keepaliveInterval := 15 * time.Second
	if isStream {
		keepaliveInterval = 10 * time.Second
	}
	keepaliveTicker := time.NewTicker(keepaliveInterval)
	defer keepaliveTicker.Stop()
WaitLoop:
	for {
		select {
		case resp = <-job.Response:
			break WaitLoop
		case <-keepaliveTicker.C:
			if isStream {
				// Start SSE and send keepalive comment
				if !wroteHeader {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Connection", "keep-alive")
					w.Header().Set("X-Accel-Buffering", "no")
					w.WriteHeader(http.StatusOK)
					wroteHeader = true
				}
				fmt.Fprintf(w, ": keepalive\n\n")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				log.Printf("[PROXY] Sent SSE keepalive ping")
			} else {
				log.Printf("[PROXY] Still waiting for model response...")
			}
		case <-ctx.Done():
			log.Printf("[PROXY] Client disconnected while waiting")
			return
		}
	}
	log.Printf("[PROXY] 'Got response, err=%v status=%d", resp.Err, resp.Status)
if resp.Err != nil {
		if wroteHeader {
			// Headers already sent via SSE keepalive - send error as SSE event
			errJSON, _ := json.Marshal(map[string]interface{}{
				"error": map[string]interface{}{
					"message": resp.Err.Error(),
					"type":   "server_error",
					"code":   "proxy_error",
				},
			})
			fmt.Fprintf(w, "data: %s\n\n", string(errJSON))
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
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
	} else {
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

// streamSSE reads an SSE stream from upstream, sanitizes each chunk,
// and forwards it to the client. It ensures proper [DONE] sentinel
// and finish_reason even if the upstream drops unexpectedly.
func (g *Gateway) streamSSE(w http.ResponseWriter, flusher http.Flusher, body io.ReadCloser, modelID string, provider string) {
	// Strip Content-Length since we may modify chunks
	w.Header().Del("Content-Length")

	bufReader := bufio.NewReader(body)
	sentFinishReason := false
	sentDone := false

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
			sentDone = true
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			continue
		}

		// Parse the JSON chunk to sanitize it
		var chunk map[string]interface{}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			// Not valid JSON — forward as-is
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
			continue
		}

		// Strip reasoning/reasoning_content from delta messages
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

	// If the stream ended without [DONE] or without a finish_reason,
	// synthesize them so the client doesn't hang or error.
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

	if !sentDone {
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
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


func (g *Gateway) onSuccess(job *RequestJob, model engine.ModelCandidate, proxyResp *ProxyResponse, body []byte) {
	log.Printf("[PROXY] Routed to: %s (%s) score=%.1f", model.ID, model.Provider, model.Score)
	g.mu.Lock()
	g.LastUsedModel = model.ID
	g.LastUsedProvider = model.Provider
	g.mu.Unlock()
	if job.DBID > 0 {
		db.DequeueRequest(g.DB, job.DBID)
	}
	if g.DB != nil {
		db.RecordSuccess(g.DB, model.ID)
	}
	g.logUsage(model.ID, body, proxyResp.Body)
	if proxyResp.Stream == nil {
		if g.Redis != nil {
			g.Redis.Set(job.Ctx, string(body), proxyResp.Body, 1*time.Hour)
		} else {
			g.cacheMu.Lock()
			g.Cache[string(body)] = proxyResp.Body
			g.cacheMu.Unlock()
		}
	}
	// Set model/provider metadata on the response so ServeHTTP can expose them
	proxyResp.ModelID = model.ID
	proxyResp.Provider = model.Provider
	job.Response <- proxyResp
}

// classifyRequest detects tool-call requests and splits models by tool compatibility
func (g *Gateway) classifyRequest(body []byte) (hasTools bool, toolModels []engine.ModelCandidate, plainModels []engine.ModelCandidate) {
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
	}
	g.mu.RLock()
	for _, m := range g.RankedModels {
		if noTool[m.Provider] {
			plainModels = append(plainModels, m)
		} else {
			toolModels = append(toolModels, m)
		}
	}
	g.mu.RUnlock()
	return
}

// filterCandidates removes circuit-broken models
func (g *Gateway) filterCandidates(all engine.RankedModels) []engine.ModelCandidate {
	skipProvider := map[string]bool{"ollama": true, "lm_studio": true}
	valid := make([]engine.ModelCandidate, 0, len(all))
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

	// Apply score floor: models with large parameters should never have negative scores
	// This prevents benchmark failures (429/timeout) from permanently sinking good models
	for i := range all {
		if all[i].Score < 0 && all[i].Parameters > 0 {
			minScore := (float64(min(all[i].Parameters, 405)) / 100.0) * 0.2
			all[i].Score = minScore
		}
	}

	for _, m := range all {
		if m.Score < 0 {
			skipped++
			continue
		}
		if skipProvider[m.Provider] {
			skipped++
			continue
		}
		if activeCooldowns[m.Provider] {
			skipped++
			continue
		}
		if g.getAPIKey(m.Provider) == "" {
			skipped++
			continue
		}
		if g.DB != nil {
			blocked, _ := db.GetCircuitBreakerList(g.DB)
			if blocked[m.ID] || blocked[m.Provider+"/"+m.ID] {
				skipped++
				continue
			}
		}
		valid = append(valid, m)
	}
	nvidiaCount := 0
	for _, m := range all {
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
	if g.DB == nil {
		return
	}
	g.cbRecoveryMu.Lock()
	defer g.cbRecoveryMu.Unlock()
	g.DB.Exec("UPDATE model_history SET failure_count = 0, retry_after = NULL WHERE failure_count >= 3")
	log.Println("[ROUTER] Circuit breakers auto-recovered")
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

			// Fix null assistant content -> ""
			if r, ok := m["role"].(string); ok && r == "assistant" {
				if content, exists := m["content"]; exists && content == nil {
					m["content"] = ""
				}
			}

			// For no-tool providers: strip tool-related fields
			if noTool[provider] && hasTools {
				if r, ok := m["role"].(string); ok && r == "tool" {
					continue // skip tool result messages entirely
				}
				delete(m, "tool_calls")
				delete(m, "tool_call_id")
			}
			clean = append(clean, m)
		}
		payload["messages"] = clean
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
	targetURL := g.getProviderURL(model.ID, model.Provider)
	if targetURL == "" {
		return false
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	host := parsed.Host

	g.preflightCacheMu.RLock()
	if e, ok := g.preflightCache[host]; ok && time.Since(e.checkedAt) < 5*time.Minute {
		g.preflightCacheMu.RUnlock()
		return e.ok
	}
	g.preflightCacheMu.RUnlock()

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Head(parsed.Scheme + "://" + host)
	ok := err == nil
	if ok {
		resp.Body.Close()
	}
	g.preflightCacheMu.Lock()
	g.preflightCache[host] = preflightEntry{ok: ok, checkedAt: time.Now()}
	g.preflightCacheMu.Unlock()
	return ok
}

// forwardRequest sends the request to a specific provider
func (g *Gateway) forwardRequest(client *http.Client, r *http.Request, model engine.ModelCandidate, body []byte) *ProxyResponse {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &ProxyResponse{Err: fmt.Errorf("unmarshal: %v", err)}
	}

	// Set model ID, stripping provider prefixes where needed
	modelForAPI := model.ID
	if model.Provider == "nvidia_nim" {
		modelForAPI = strings.TrimPrefix(modelForAPI, "nvidia_nim/")
	}
	payload["model"] = modelForAPI

	stream, _ := payload["stream"].(bool)
	newBody, _ := json.Marshal(payload)
	if mapped, err := TransformRequestBody(model.Provider, newBody); err == nil {
		newBody = mapped
	}

	targetURL := g.getProviderURL(model.ID, model.Provider)
	if targetURL == "" {
		return &ProxyResponse{Err: fmt.Errorf("unsupported provider: %s", model.Provider)}
	}

	req, err := http.NewRequestWithContext(r.Context(), "POST", targetURL, bytes.NewBuffer(newBody))
	if err != nil {
		return &ProxyResponse{Err: err}
	}
	g.transformRequest(req, model.Provider)

	resp, err := client.Do(req)
	if err != nil {
		return &ProxyResponse{Err: fmt.Errorf("%s: %v", model.Provider, err)}
	}

	if stream && resp.StatusCode == http.StatusOK {
		return &ProxyResponse{
			Status:   resp.StatusCode,
			Header:   resp.Header,
			Stream:   resp.Body,
			ModelID:  model.ID,
			Provider: model.Provider,
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

	return &ProxyResponse{
		Status: resp.StatusCode,
		Body:   respBody,
		Header: resp.Header,
	}
}

// Complete provider URL mapping
func (g *Gateway) getProviderURL(modelID, provider string) string {
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
		return "https://open.bigmodel.cn/api/paas/v1/chat/completions"
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
	}
	return ""
}

// API key resolution for all providers (with NVIDIA fallback)
func (g *Gateway) getAPIKey(provider string) string {
	switch provider {
	case "openrouter":
		return os.Getenv("OPENROUTER_API_KEY")
	case "groq":
		return os.Getenv("GROQ_API_KEY")
	case "github":
		return os.Getenv("GITHUB_TOKEN")
	case "deepinfra":
		return os.Getenv("DEEPINFRA_API_KEY")
	case "cerebras":
		return os.Getenv("CEREBRAS_API_KEY")
	case "huggingface":
		return os.Getenv("HUGGINGFACE_API_KEY")
	case "nvidia", "nvidia_nim":
		key := os.Getenv("NVIDIA_NIM_API_KEY")
		if key == "" {
			key = os.Getenv("NVIDIA_API_KEY")
		}
		return key
	case "mistral":
		return os.Getenv("MISTRAL_API_KEY")
	case "codestral":
		return os.Getenv("CODESTRAL_API_KEY")
	case "cohere":
		return os.Getenv("COHERE_API_KEY")
	case "sambanova":
		return os.Getenv("SAMBANOVA_API_KEY")
	case "fireworks":
		return os.Getenv("FIREWORKS_API_KEY")
	case "hyperbolic":
		return os.Getenv("HYPERBOLIC_API_KEY")
	case "cloudflare":
		return os.Getenv("CLOUDFLARE_API_KEY")
	case "opencode_zen":
		return os.Getenv("OPENCODE_ZEN_API_KEY")
	case "anthropic":
		return os.Getenv("ANTHROPIC_API_KEY")
	case "gemini":
		return os.Getenv("GEMINI_API_KEY")
	case "siliconflow":
		return os.Getenv("SILICONFLOW_API_KEY")
	case "together":
		return os.Getenv("TOGETHER_API_KEY")
	case "novita":
		return os.Getenv("NOVITA_API_KEY")
	case "nebius":
		return os.Getenv("NEBIUS_API_KEY")
	case "deepseek":
		return os.Getenv("DEEPSEEK_API_KEY")
	case "ai21":
		return os.Getenv("AI21_API_KEY")
	case "replicate":
		return os.Getenv("REPLICATE_API_TOKEN")
	case "dashscope":
		return os.Getenv("DASHSCOPE_API_KEY")
	case "minimax":
		return os.Getenv("MINIMAX_API_KEY")
	case "moonshot":
		return os.Getenv("MOONSHOT_API_KEY")
	case "stepfun":
		return os.Getenv("STEPFUN_API_KEY")
	case "zhipu":
		return os.Getenv("ZHIPU_API_KEY")
	case "internlm":
		return os.Getenv("INTERNLM_API_KEY")
	case "arcee":
		return os.Getenv("ARCEE_API_KEY")
	case "perplexity":
		return os.Getenv("PERPLEXITY_API_KEY")
	case "xai":
		return os.Getenv("XAI_API_KEY")
	case "hunyuan":
		return os.Getenv("HUNYUAN_API_KEY")
		}
	return ""
}

func (g *Gateway) transformRequest(req *http.Request, provider string) {
	req.Header.Set("Content-Type", "application/json")
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SetModelPrimary moves a model to position 0 (top of rankings)
func (g *Gateway) SetModelPrimary(modelID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			if i == 0 {
				return
			}
			model := g.RankedModels[i]
			copy(g.RankedModels[1:i+1], g.RankedModels[0:i])
			g.RankedModels[0] = model
			return
			}
		}
}

// PromoteModel moves a model from fallback into the primary group
func (g *Gateway) PromoteModel(modelID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	idx := -1
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			idx = i
			break
		}
	}
	if idx < 0 || idx < g.PrimaryCount {
		return // already primary or not found
	}
	// Swap with the last primary model
	lastPrimary := g.PrimaryCount - 1
	g.RankedModels[idx], g.RankedModels[lastPrimary] = g.RankedModels[lastPrimary], g.RankedModels[idx]
}

// DemoteModel moves a model from primary into the fallback group
func (g *Gateway) DemoteModel(modelID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	idx := -1
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= g.PrimaryCount {
		return // already fallback or not found
	}
	// Swap with the first fallback model
	firstFallback := g.PrimaryCount
	if firstFallback < len(g.RankedModels) {
		g.RankedModels[idx], g.RankedModels[firstFallback] = g.RankedModels[firstFallback], g.RankedModels[idx]
	}
}

// SetAsFallback moves a model to the first position in the fallback group (position = PrimaryCount)
func (g *Gateway) SetAsFallback(modelID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	idx := -1
	for i, m := range g.RankedModels {
		if m.ID == modelID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return // not found
	}
	target := g.PrimaryCount
	if target >= len(g.RankedModels) {
		target = len(g.RankedModels) - 1
	}
	if idx == target {
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
}

// MoveModelUp moves a model one position higher in the rankings
func (g *Gateway) MoveModelUp(modelID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, m := range g.RankedModels {
		if m.ID == modelID && i > 0 {
			g.RankedModels[i], g.RankedModels[i-1] = g.RankedModels[i-1], g.RankedModels[i]
			return
		}
	}
}

// MoveModelDown moves a model one position lower in the rankings
func (g *Gateway) MoveModelDown(modelID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, m := range g.RankedModels {
		if m.ID == modelID && i < len(g.RankedModels)-1 {
			g.RankedModels[i], g.RankedModels[i+1] = g.RankedModels[i+1], g.RankedModels[i]
			return
		}
	}
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
