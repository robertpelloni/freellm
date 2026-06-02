package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/robertpelloni/litellm_control_panel/internal/db"
	"github.com/robertpelloni/litellm_control_panel/internal/engine"
)

type Gateway struct {
	RankedModels engine.RankedModels
	mu           sync.RWMutex
	Queue        chan *RequestJob
	ActiveJobs   int
	MaxActive    int
	DB           *sql.DB
}

type RequestJob struct {
	Request  *http.Request
	Response chan *ProxyResponse
	Ctx      context.Context
}

type ProxyResponse struct {
	Status int
	Body   []byte
	Header http.Header
	Err    error
}

func NewGateway(maxActive int, database *sql.DB) *Gateway {
	g := &Gateway{
		Queue:     make(chan *RequestJob, 1000), // Buffer 1000 requests
		MaxActive: maxActive,
		DB:        database,
	}
	go g.workerLoop()
	return g
}

func (g *Gateway) UpdateModels(models engine.RankedModels) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.RankedModels = models
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}

	job := &RequestJob{
		Request:  r,
		Response: make(chan *ProxyResponse, 1),
		Ctx:      r.Context(),
	}

	// Highly Stable Network: Queue the request instead of rejecting if busy
	select {
	case g.Queue <- job:
		// Wait for worker to process
	case <-r.Context().Done():
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
	w.Write(resp.Body)
}

func (g *Gateway) workerLoop() {
	semaphore := make(chan struct{}, g.MaxActive)
	for job := range g.Queue {
		semaphore <- struct{}{}
		go func(j *RequestJob) {
			defer func() { <-semaphore }()
			g.processJob(j)
		}(job)
	}
}

func (g *Gateway) processJob(job *RequestJob) {
	g.mu.RLock()
	models := g.RankedModels
	g.mu.RUnlock()

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

	// Advanced Routing: Retry with rotation
	var lastErr error
	maxRetries := min(len(models), 3) // Try up to 3 best models

	for i := 0; i < maxRetries; i++ {
		model := models[i]
		proxyResp := g.forwardRequest(client, job.Request, model, body)

		if proxyResp.Err == nil && proxyResp.Status < 400 {
			// Log usage if successful
			g.logUsage(model.ID, body, proxyResp.Body)
			job.Response <- proxyResp
			return
		}

		lastErr = proxyResp.Err
		if proxyResp.Err == nil {
			lastErr = fmt.Errorf("status %d", proxyResp.Status)
		}

		// Exponential backoff before retry
		if i < maxRetries-1 {
			time.Sleep(time.Duration(1<<i) * 500 * time.Millisecond)
		}
	}

	job.Response <- &ProxyResponse{Err: fmt.Errorf("all retries failed, last error: %v", lastErr)}
}

func (g *Gateway) forwardRequest(client *http.Client, r *http.Request, model engine.ModelCandidate, body []byte) *ProxyResponse {
	// Re-encode body with the selected model
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &ProxyResponse{Err: fmt.Errorf("failed to unmarshal request body: %v", err)}
	}
	payload["model"] = model.ID

	newBody, _ := json.Marshal(payload)

	targetURL := g.getProviderURL(model.ID, model.Provider)
	if targetURL == "" {
		return &ProxyResponse{Err: fmt.Errorf("unsupported provider: %s", model.Provider)}
	}

	req, err := http.NewRequestWithContext(r.Context(), "POST", targetURL, bytes.NewBuffer(newBody))
	if err != nil {
		return &ProxyResponse{Err: err}
	}

	// Copy essential headers
	req.Header.Set("Content-Type", "application/json")
	if apiKey := g.getAPIKey(model.Provider); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return &ProxyResponse{Err: err}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
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

	db.LogUsage(g.DB, modelID, promptTokens, completionTokens)
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
	}
	return ""
}
