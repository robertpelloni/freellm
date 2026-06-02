package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/robertpelloni/litellm_control_panel/internal/engine"
)

type Gateway struct {
	RankedModels engine.RankedModels
	mu           sync.RWMutex
	Queue        chan *RequestJob
	ActiveJobs   int
	MaxActive    int
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

func NewGateway(maxActive int) *Gateway {
	g := &Gateway{
		Queue:     make(chan *RequestJob, 1000), // Buffer 1000 requests
		MaxActive: maxActive,
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
	if len(g.RankedModels) == 0 {
		g.mu.RUnlock()
		job.Response <- &ProxyResponse{Err: fmt.Errorf("no models available")}
		return
	}
	// Try primary models, then fallbacks
	model := g.RankedModels[0] // Pick best
	g.mu.RUnlock()

	client := &http.Client{Timeout: 60 * time.Second}

	// Read body once so we can retry if needed
	body, _ := io.ReadAll(job.Request.Body)

	proxyResp := g.forwardRequest(client, job.Request, model, body)
	job.Response <- proxyResp
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
	}
	return ""
}

func (g *Gateway) getAPIKey(provider string) string {
	// In a real app, this would be injected from config or env
	// For now, we'll try to get it from environment variables
	switch provider {
	case "openrouter":
		return "OPENROUTER_API_KEY" // Placeholder
	case "groq":
		return "GROQ_API_KEY"
	case "github":
		return "GITHUB_TOKEN"
	}
	return ""
}
