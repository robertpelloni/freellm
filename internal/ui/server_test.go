package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertpelloni/freellm/internal/engine"
)

func TestHandleRankings(t *testing.T) {
	s := NewUIServer(nil, nil, nil)
	s.UpdateModels(engine.RankedModels{
		{ID: "test-model", Provider: "test-provider", Score: 100},
	})

	req, _ := http.NewRequest("GET", "/api/rankings", nil)
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(s.handleRankings)

	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	expected := `[{"id":"test-model","provider":"test-provider","parameters":0,"context_length":0,"latency":0,"score":100,"disabled":false,"last_benchmark":"0001-01-01T00:00:00Z","prompt_price":0,"completion_price":0}]`
	if rr.Body.String() != expected+"\n" && rr.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got %v want %v", rr.Body.String(), expected)
	}
}

func TestHandleConfigRejectsInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "freellm-config.yaml")
	original := []byte("port: 4000\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	s := NewUIServer(nil, nil, nil)
	s.ConfigPath = path

	req := httptest.NewRequest("POST", "/api/config", strings.NewReader("port: ["))
	rr := httptest.NewRecorder()
	s.handleConfig(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("config changed after invalid post: got %q want %q", got, original)
	}
}

func TestHandleConfigRequiresMasterKeyWhenConfigured(t *testing.T) {
	t.Setenv("FREELLM_MASTER_KEY", "secret")
	dir := t.TempDir()
	path := filepath.Join(dir, "freellm-config.yaml")
	if err := os.WriteFile(path, []byte("port: 4000\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	s := NewUIServer(nil, nil, nil)
	s.ConfigPath = path

	req := httptest.NewRequest("POST", "/api/config", strings.NewReader("port: 4001\n"))
	rr := httptest.NewRecorder()
	s.handleConfig(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest("POST", "/api/config", strings.NewReader("port: 4001\n"))
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	s.handleConfig(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}
