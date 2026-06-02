package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

var SizePattern = regexp.MustCompile(`(\d+)[bB]`)

type ModelCandidate struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	Parameters    int     `json:"parameters"`
	ContextLength int     `json:"context_length"`
	Latency       float64 `json:"latency"`
	Score         float64 `json:"score"`
}

type Benchmarker struct {
	APIKeys   map[string]string
	BaseURLs  map[string]string
	MinParams int
	Weights   map[string]float64
	Client    *http.Client
}

func NewBenchmarker(apiKeys map[string]string, minParams int) *Benchmarker {
	return &Benchmarker{
		APIKeys:  apiKeys,
		BaseURLs: make(map[string]string),
		Weights: map[string]float64{
			"size":    0.6,
			"context": 0.2,
			"latency": 0.2,
		},
		MinParams: minParams,
		Client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (b *Benchmarker) ExtractParameters(modelID, name, description string) int {
	for _, text := range []string{modelID, name, description} {
		match := SizePattern.FindStringSubmatch(text)
		if len(match) > 1 {
			params, _ := strconv.Atoi(match[1])
			return params
		}
	}
	return 0
}

func (b *Benchmarker) CalculateScore(params int, latency float64, contextLength int) float64 {
	sizeScore := (float64(min(params, 405)) / 100.0) * b.Weights["size"]
	contextScore := (float64(min(contextLength, 128000)) / 128000.0) * b.Weights["context"]
	latencyPenalty := minF(latency, 5.0) * b.Weights["latency"]
	return sizeScore + contextScore - latencyPenalty
}

func (b *Benchmarker) FetchModels(ctx context.Context) []ModelCandidate {
	var candidates []ModelCandidate
	var mu sync.Mutex
	var wg sync.WaitGroup

	providers := []string{"openrouter", "groq", "deepinfra", "cerebras", "github", "huggingface", "nvidia"}

	for _, p := range providers {
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()
			models := b.fetchProviderModels(ctx, provider)
			mu.Lock()
			candidates = append(candidates, models...)
			mu.Unlock()
		}(p)
	}

	wg.Wait()
	return candidates
}

func (b *Benchmarker) fetchProviderModels(ctx context.Context, provider string) []ModelCandidate {
	url := b.getModelsURL(provider)
	if url == "" {
		return nil
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if apiKey := b.APIKeys[provider]; apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := b.Client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Data []map[string]interface{} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&data)

	var models []ModelCandidate
	for _, m := range data.Data {
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}

		params := b.ExtractParameters(id, "", "")
		if params < b.MinParams && params != 0 {
			continue
		}

		if IsExcluded(id) {
			continue
		}

		ctxLength := 4096
		if spec, ok := LookupKnownModel(id); ok {
			params = spec.Params
			ctxLength = spec.Ctx
		}

		models = append(models, ModelCandidate{
			ID:            id,
			Provider:      provider,
			Parameters:    params,
			ContextLength: ctxLength,
		})
	}
	return models
}

func (b *Benchmarker) MeasureLatency(ctx context.Context, modelID, provider string) (float64, error) {
	url := b.getCompletionsURL(provider)
	if url == "" {
		return 0, fmt.Errorf("unsupported provider: %s", provider)
	}

	apiKey := b.APIKeys[provider]
	// NVIDIA uses same key
	if provider == "nvidia" && apiKey == "" {
		apiKey = b.APIKeys["nvidia_nim"]
	}

	payload := map[string]interface{}{
		"model":    modelID,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
		"stream":   true,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	startTime := time.Now()
	resp, err := b.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("error %d: %s", resp.StatusCode, string(respBody))
	}

	// Wait for the first line of streaming response
	reader := io.Reader(resp.Body)
	buf := make([]byte, 1024)
	_, err = reader.Read(buf)
	if err != nil && err != io.EOF {
		return 0, err
	}

	ttft := time.Since(startTime).Seconds()
	return ttft, nil
}

func (b *Benchmarker) getModelsURL(provider string) string {
	base := b.BaseURLs[provider]
	switch provider {
	case "openrouter":
		if base == "" { return "https://openrouter.ai/api/v1/models" }
		return base + "/models"
	case "groq":
		if base == "" { return "https://api.groq.com/openai/v1/models" }
		return base + "/models"
	case "deepinfra":
		if base == "" { return "https://api.deepinfra.com/v1/openai/models" }
		return base + "/openai/models"
	case "cerebras":
		if base == "" { return "https://api.cerebras.ai/v1/models" }
		return base + "/models"
	case "github":
		if base == "" { return "https://models.inference.ai.azure.com/models" }
		return base
	case "nvidia":
		if base == "" { return "https://integrate.api.nvidia.com/v1/models" }
		return base + "/models"
	}
	return ""
}

func (b *Benchmarker) getCompletionsURL(provider string) string {
	base := b.BaseURLs[provider]
	switch provider {
	case "openrouter":
		if base == "" { return "https://openrouter.ai/api/v1/chat/completions" }
		return base + "/chat/completions"
	case "groq":
		if base == "" { return "https://api.groq.com/openai/v1/chat/completions" }
		return base + "/chat/completions"
	case "deepinfra":
		if base == "" { return "https://api.deepinfra.com/v1/openai/chat/completions" }
		return base + "/openai/chat/completions"
	case "cerebras":
		if base == "" { return "https://api.cerebras.ai/v1/chat/completions" }
		return base + "/chat/completions"
	case "github":
		if base == "" { return "https://models.inference.ai.azure.com/chat/completions" }
		return base + "/chat/completions"
	case "nvidia":
		if base == "" { return "https://integrate.api.nvidia.com/v1/chat/completions" }
		return base + "/chat/completions"
	case "huggingface":
		// Hugging Face uses per-model endpoints
		return "https://api-inference.huggingface.co/models/"
	}
	return ""
}

func min(a, b int) int {
	if a < b { return a }
	return b
}

func minF(a, b float64) float64 {
	if a < b { return a }
	return b
}

type RankedModels []ModelCandidate

func (b *Benchmarker) RunBenchmark(ctx context.Context, candidates []ModelCandidate) RankedModels {
	var wg sync.WaitGroup
	results := make(chan ModelCandidate, len(candidates))
	semaphore := make(chan struct{}, 5) // Limit concurrency

	for _, m := range candidates {
		wg.Add(1)
		go func(m ModelCandidate) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			lat, err := b.MeasureLatency(ctx, m.ID, m.Provider)
			if err == nil {
				m.Latency = lat
				m.Score = b.CalculateScore(m.Parameters, lat, m.ContextLength)
				results <- m
			} else {
				fmt.Printf("Benchmark failed for %s/%s: %v\n", m.Provider, m.ID, err)
			}
		}(m)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var ranked RankedModels
	for r := range results {
		ranked = append(ranked, r)
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	return ranked
}
