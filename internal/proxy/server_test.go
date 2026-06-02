package proxy

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/robertpelloni/litellm_control_panel/internal/engine"
)

func TestGatewayQueueing(t *testing.T) {
	// Create a gateway with 0 workers to force queueing
	g := &Gateway{
		Queue:     make(chan *RequestJob, 10),
		MaxActive: 0,
	}

	// Mock ranked models
	g.UpdateModels(engine.RankedModels{
		{ID: "test-model", Provider: "test-provider"},
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"any", "messages":[]}`))

	// Use a context with timeout to avoid blocking forever
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	// Since there are no workers, this should eventually time out or fill the queue
	go g.ServeHTTP(httptest.NewRecorder(), req)

	// Check if the job reached the queue
	select {
	case job := <-g.Queue:
		if job.Request.URL.Path != "/v1/chat/completions" {
			t.Errorf("Unexpected path in queued job: %s", job.Request.URL.Path)
		}
	case <-time.After(200 * time.Millisecond):
		t.Errorf("Job did not reach the queue in time")
	}
}

func TestHealthChecks(t *testing.T) {
	g := &Gateway{
		RankedModels: engine.RankedModels{}, // No models initially
	}

	// Test Liveness
	req := httptest.NewRequest("GET", "/health/liveness", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("Liveness check failed, got status %d", w.Code)
	}

	// Test Readiness (should fail because no models)
	req = httptest.NewRequest("GET", "/health/readiness", nil)
	w = httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("Readiness check should fail with 503 when no models, got %d", w.Code)
	}

	// Add a model and test readiness again
	g.UpdateModels(engine.RankedModels{
		{ID: "test-model", Provider: "test-provider"},
	})
	w = httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("Readiness check should pass with 200 when models available, got %d", w.Code)
	}
}
