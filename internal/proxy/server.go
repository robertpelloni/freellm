package proxy

import (
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
	"github.com/robertpelloni/litellm_control_panel/internal/db"
	"github.com/robertpelloni/litellm_control_panel/internal/engine"
)

type Gateway struct {
	RankedModels engine.RankedModels
	mu           sync.RWMutex
	Queue        chan *RequestJob
	HighPriQueue chan *RequestJob
	ActiveJobs   int
	MaxActive    int
	DB           *sql.DB
	PrimaryCount int
	Cache        map[string][]byte
	cacheMu      sync.RWMutex
	Redis        *redis.Client
}

type RequestJob struct {
	Request  *http.Request
	Response chan *ProxyResponse
	Ctx      context.Context
	DBID     int64
}

type ProxyResponse struct {
	Status  int
	Body    []byte
	Header  http.Header
	Err     error
	Stream  io.ReadCloser
	ModelID string
}

func NewGateway(maxActive int, database *sql.DB) *Gateway {
	g := &Gateway{
		Queue:        make(chan *RequestJob, 1000),
		HighPriQueue: make(chan *RequestJob, 100),
		MaxActive:    maxActive,
		DB:           database,
		PrimaryCount: 5,
		Cache:        make(map[string][]byte),
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

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	priority := r.Header.Get("X-LiteLLM-Priority") == "high"

	// 1. Simple Proxy Auth (Virtual Keys)
	authKey := os.Getenv("LITELLM_MASTER_KEY")
	if authKey != "" {
		token := r.Header.Get("Authorization")
		if token != "Bearer "+authKey {
			http.Error(w, "Unauthorized", 401)
			return
		}
	}

	// Health Checks
	if r.URL.Path == "/health" || r.URL.Path == "/health/liveness" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}

	if r.URL.Path == "/health/readiness" {
		g.mu.RLock()
		modelCount := len(g.RankedModels)
		g.mu.RUnlock()

		if modelCount > 0 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("READY"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("NOT READY - No models available"))
		}
		return
	}

	if r.URL.Path == "/v1/models" {
		g.handleModels(w, r)
		return
	}

	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}

	// Buffer body to persist if needed
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	// JSON Schema Validation
	schemaLoader := gojsonschema.NewStringLoader(`{
		"type": "object",
		"properties": {
			"model": {"type": "string"},
			"messages": {"type": "array"}
		},
		"required": ["model", "messages"]
	}`)
	documentLoader := gojsonschema.NewBytesLoader(body)
	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err == nil && !result.Valid() {
		http.Error(w, "Invalid OpenAI Request Schema", 400)
		return
	}

	// 2. Cache Lookup
	cacheKey := string(body)
	if g.Redis != nil {
		val, err := g.Redis.Get(ctx, cacheKey).Bytes()
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-LiteLLM-Cache", "HIT (Redis)")
			w.Write(val)
			return
		}
	} else {
		g.cacheMu.RLock()
		cached, ok := g.Cache[cacheKey]
		g.cacheMu.RUnlock()
		if ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-LiteLLM-Cache", "HIT")
			w.Write(cached)
			return
		}
	}

	dbID := int64(0)
	if g.DB != nil {
		var headers strings.Builder
		for k, v := range r.Header {
			fmt.Fprintf(&headers, "%s: %s\n", k, strings.Join(v, ","))
		}
		dbID, _ = db.EnqueueRequest(g.DB, r.Method, r.URL.String(), headers.String(), body)
	}

	job := &RequestJob{
		Request:  r,
		Response: make(chan *ProxyResponse, 1),
		Ctx:      r.Context(),
		DBID:     dbID,
	}

	queue := g.Queue
	if priority { queue = g.HighPriQueue }

	select {
	case queue <- job:
	case <-r.Context().Done():
		if dbID > 0 { db.DequeueRequest(g.DB, dbID) }
		return
	}

	resp := <-job.Response
	if resp.Err != nil {
		http.Error(w, resp.Err.Error(), http.StatusBadGateway)
		return
	}

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.Status)

	if resp.Stream != nil {
		defer resp.Stream.Close()
		flusher, ok := w.(http.Flusher)

		// For token tracking in streams
		var totalStreamed int

		if ok {
			buf := make([]byte, 4096)
			for {
				n, err := resp.Stream.Read(buf)
				if n > 0 {
					w.Write(buf[:n])
					flusher.Flush()
					totalStreamed += n
				}
				if err != nil {
					break
				}
			}
		} else {
			n, _ := io.Copy(w, resp.Stream)
			totalStreamed = int(n)
		}

		// Log usage for stream
		// We can only estimate tokens from raw bytes for now
		g.logUsage(resp.ModelID, nil, nil)
	} else {
		w.Write(resp.Body)
	}
}

func (g *Gateway) workerLoop() {
	semaphore := make(chan struct{}, g.MaxActive)
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

		semaphore <- struct{}{}
		go func(j *RequestJob) {
			defer func() { <-semaphore }()
			g.processJob(j)
		}(job)
	}
}


// PreFlightCheck verifies a provider endpoint is reachable before sending a request.
func (g *Gateway) PreFlightCheck(model engine.ModelCandidate) bool {
	targetURL := g.getProviderURL(model.ID, model.Provider)
	if targetURL == "" { return false }
	parsed, err := url.Parse(targetURL)
	if err != nil { return false }
	baseURL := parsed.Scheme + "://" + parsed.Host
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Head(baseURL)
	if err != nil {
		log.Printf("[PREFLIGHT] %s/%s: provider %s unreachable: %v", model.Provider, model.ID, baseURL, err)
		return false
	}
	resp.Body.Close()
	return true
}

// VerifyModelList filters models by pre-flight connectivity check.
func (g *Gateway) VerifyModelList(models engine.RankedModels) engine.RankedModels {
	type result struct { idx int; ok bool }
	results := make(chan result, len(models))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func(idx int, model engine.ModelCandidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results <- result{idx, g.PreFlightCheck(model)}
		}(i, m)
	}
	go func() { wg.Wait(); close(results) }()
	rm := make(map[int]bool)
	for r := range results { rm[r.idx] = r.ok }
	var verified engine.RankedModels
	for i, m := range models {
		if rm[i] { verified = append(verified, m) }
	}
	if len(verified) < len(models) {
		log.Printf("[PREFLIGHT] %d/%d models passed connectivity check", len(verified), len(models))
	}
	return verified
}

func (g *Gateway) processJob(job *RequestJob) {
	g.mu.RLock()
	models := g.RankedModels
	g.mu.RUnlock()

	// Pre-flight connectivity check
	models = g.VerifyModelList(models)
	if len(models) == 0 {
		job.Response <- &ProxyResponse{Err: fmt.Errorf("no models available after connectivity check")}
		return
	}

	// Circuit Breaker Integration
	if g.DB != nil {
		blocked, _ := db.GetCircuitBreakerList(g.DB)
		if len(blocked) > 0 {
			var activeModels engine.RankedModels
			for _, m := range models {
				if !blocked[m.ID] {
					activeModels = append(activeModels, m)
				}
			}
			models = activeModels
		}
	}

	if len(models) == 0 {
		job.Response <- &ProxyResponse{Err: fmt.Errorf("no models available")}
		return
	}

	client := &http.Client{Timeout: 60 * time.Second}

	// Read body once so we can retry if needed
	body, err := io.ReadAll(job.Request.Body)
	if err != nil {
		job.Response <- &ProxyResponse{Err: fmt.Errorf("failed to read request body: %v", err)}
		return
	}

	// Advanced Routing: Two-Group Strategy
	var lastErr error

	// 1. Try Primary Group first
	primaryGroup := models
	if len(models) > g.PrimaryCount {
		primaryGroup = models[:g.PrimaryCount]
	}

	for i, model := range primaryGroup {
		proxyResp := g.forwardRequest(client, job.Request, model, body)

		if proxyResp.Err == nil && proxyResp.Status < 400 {
			if job.DBID > 0 { db.DequeueRequest(g.DB, job.DBID) }
			if g.DB != nil { db.RecordSuccess(g.DB, model.ID) }
			g.logUsage(model.ID, body, proxyResp.Body)

			// Store in Cache if not streaming
			if proxyResp.Stream == nil {
				if g.Redis != nil {
					g.Redis.Set(job.Ctx, string(body), proxyResp.Body, 1*time.Hour)
				} else {
					g.cacheMu.Lock()
					g.Cache[string(body)] = proxyResp.Body
					g.cacheMu.Unlock()
				}
			}

			job.Response <- proxyResp
			return
		}
		if g.DB != nil { db.RecordFailure(g.DB, model.ID) }
		lastErr = proxyResp.Err
		if proxyResp.Err == nil { lastErr = fmt.Errorf("primary status %d", proxyResp.Status) }
		if i < len(primaryGroup)-1 { time.Sleep(500 * time.Millisecond) }
	}

	// 2. Try Fallback Group if Primary fails
	if len(models) > g.PrimaryCount {
		fallbackGroup := models[g.PrimaryCount:]
		// Try up to 3 models from fallback to avoid infinite retries
		maxFallbacks := min(len(fallbackGroup), 3)
		for i := 0; i < maxFallbacks; i++ {
			model := fallbackGroup[i]
			proxyResp := g.forwardRequest(client, job.Request, model, body)
			if proxyResp.Err == nil && proxyResp.Status < 400 {
				g.logUsage(model.ID, body, proxyResp.Body)
				job.Response <- proxyResp
				return
			}
			if g.DB != nil { db.RecordFailure(g.DB, model.ID) }
			lastErr = proxyResp.Err
			if proxyResp.Err == nil { lastErr = fmt.Errorf("fallback status %d", proxyResp.Status) }
			time.Sleep(1 * time.Second)
		}
	}

	if job.DBID > 0 { db.DequeueRequest(g.DB, job.DBID) }
	job.Response <- &ProxyResponse{Err: fmt.Errorf("all primary and fallback retries failed, last error: %v", lastErr)}
}

func (g *Gateway) forwardRequest(client *http.Client, r *http.Request, model engine.ModelCandidate, body []byte) *ProxyResponse {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &ProxyResponse{Err: fmt.Errorf("failed to unmarshal request body: %v", err)}
	}
	payload["model"] = model.ID
	stream, _ := payload["stream"].(bool)

	newBody, _ := json.Marshal(payload)

	// Robust Mapping Layer
	mappedBody, err := TransformRequestBody(model.Provider, newBody)
	if err == nil {
		newBody = mappedBody
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
		return &ProxyResponse{Err: err}
	}

	if stream && resp.StatusCode == http.StatusOK {
		return &ProxyResponse{
			Status:  resp.StatusCode,
			Header:  resp.Header,
			Stream:  resp.Body,
			ModelID: model.ID,
		}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Map response back to OpenAI format
	if !stream && resp.StatusCode == 200 {
		mappedBody, err := TransformResponseBody(model.Provider, respBody)
		if err == nil {
			respBody = mappedBody
		}

		var respData map[string]interface{}
		if err := json.Unmarshal(respBody, &respData); err == nil {
			if _, ok := respData["usage"]; !ok {
				respData["usage"] = map[string]int{
					"prompt_tokens":     len(body) / 4,
					"completion_tokens": len(respBody) / 4,
					"total_tokens":      (len(body) + len(respBody)) / 4,
				}
				respBody, _ = json.Marshal(respData)
			}
		}
	}

	return &ProxyResponse{
		Status: resp.StatusCode,
		Body:   respBody,
		Header: resp.Header,
	}
}

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
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/models/" + modelID + ":streamGenerateContent"
	case "anthropic":
		return "https://api.anthropic.com/v1/messages"
	case "opencode_zen":
		return "https://opencode.ai/zen/v1/chat/completions"
	case "bedrock":
		return "https://bedrock-runtime.us-east-1.amazonaws.com/model/" + modelID + "/invoke-with-response-stream"
	case "vertex_ai":
		return "https://us-central1-aiplatform.googleapis.com/v1/projects/PROJECT_ID/locations/us-central1/publishers/google/models/" + modelID + ":streamGenerateContent"
	case "mistral":
		return "https://api.mistral.ai/v1/chat/completions"
	case "huggingface":
		// Hugging Face uses per-model endpoints
		return "https://api-inference.huggingface.co/models/" + modelID + "/v1/chat/completions"
	}
	return ""
}

func (g *Gateway) logUsage(modelID string, requestBody, responseBody []byte) {
	if g.DB == nil {
		return
	}

	// Basic token estimation
	promptTokens := len(requestBody) / 4
	completionTokens := len(responseBody) / 4

	if responseBody != nil {
		// In a real app, parse actual token counts from response JSON
		var resp struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(responseBody, &resp); err == nil && resp.Usage.PromptTokens > 0 {
			promptTokens = resp.Usage.PromptTokens
			completionTokens = resp.Usage.CompletionTokens
		}
	} else if requestBody == nil && responseBody == nil {
		// Token tracking for stream - simplified
		promptTokens = 0
		completionTokens = 0
	}

	db.LogUsage(g.DB, modelID, promptTokens, completionTokens)
}

func (g *Gateway) transformRequest(req *http.Request, provider string) {
	req.Header.Set("Content-Type", "application/json")
	apiKey := g.getAPIKey(provider)
	if apiKey == "" {
		return
	}

	switch provider {
	case "huggingface":
		req.Header.Set("Authorization", "Bearer "+apiKey)
	case "github":
		req.Header.Set("Authorization", "Bearer "+apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	models := g.GetModels()

	type ModelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	resp := struct {
		Object string       `json:"object"`
		Data   []ModelEntry `json:"data"`
	}{
		Object: "list",
		Data:   make([]ModelEntry, 0),
	}

	now := time.Now().Unix()
	for _, m := range models {
		resp.Data = append(resp.Data, ModelEntry{
			ID:      m.ID,
			Object:  "model",
			Created: now,
			OwnedBy: m.Provider,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) RestoreQueue() {
	if g.DB == nil { return }
	pending, err := db.GetPendingRequests(g.DB)
	if err != nil || len(pending) == 0 { return }

	log.Printf("Restoring %d pending requests from disk...", len(pending))
	for _, p := range pending {
		req, _ := http.NewRequest(p.Method, p.URL, bytes.NewBuffer(p.Body))
		// (Simplified header restore)

		job := &RequestJob{
			Request:  req,
			Response: make(chan *ProxyResponse, 1),
			Ctx:      context.Background(),
			DBID:     p.ID,
		}

		// Drop on floor if queue full, but worker loop will process
		select {
		case g.Queue <- job:
		default:
		}
	}
}

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
		return os.Getenv("NVIDIA_NIM_API_KEY")
	case "gemini":
		return os.Getenv("GEMINI_API_KEY")
	case "anthropic":
		return os.Getenv("ANTHROPIC_API_KEY")
	case "bedrock":
		return os.Getenv("AWS_SECRET_ACCESS_KEY")
	case "vertex_ai":
		return os.Getenv("VERTEX_AI_KEY")
	case "mistral":
		return os.Getenv("MISTRAL_API_KEY")
	}
	return ""
}
