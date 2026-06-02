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

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"any"}`))

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
