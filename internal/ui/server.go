package ui

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robertpelloni/freellm/internal/config"
	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
	"gopkg.in/yaml.v3"
)

//go:embed static/*
var staticAssets embed.FS

type ProxyHandler interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

type UIServer struct {
	mu           sync.RWMutex
	RankedModels engine.RankedModels
	DB           *sql.DB
	Logger       *engine.EventLogger
	Proxy        ProxyHandler
	ConfigPath   string
}

func NewUIServer(database *sql.DB, logger *engine.EventLogger, proxy ProxyHandler) *UIServer {
	return &UIServer{
		DB:           database,
		Logger:       logger,
		Proxy:        proxy,
		RankedModels: engine.RankedModels{}, // Initialize to empty slice, not nil
		ConfigPath:   "freellm-config.yaml",
	}
}

func (s *UIServer) UpdateModels(models engine.RankedModels) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RankedModels = models
}

func (s *UIServer) Start(addr string) error {
	staticFS, _ := fs.Sub(staticAssets, "static")
	fileServer := http.FileServer(http.FS(staticFS))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/rankings", s.handleRankings)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/ws/logs", s.handleLogWS)
	mux.HandleFunc("/api/providers/health", s.handleProviderHealth)
	mux.HandleFunc("/api/savings", s.handleSavings)
	mux.HandleFunc("/api/proxy/", s.handleProxy)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/providers/toggle", s.handleProviderToggle)
	mux.HandleFunc("/api/maintenance/clear-skips", s.handleClearSkips)
	mux.HandleFunc("/api/maintenance/clear-blacklist", s.handleClearBlacklist)
	mux.HandleFunc("/api/maintenance/reset-stats", s.handleResetStats)
	mux.HandleFunc("/api/models/skip", s.handleModelSkip)
	mux.HandleFunc("/api/models/blacklist", s.handleModelBlacklist)
	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		index, _ := staticAssets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(index)
	})
	return http.ListenAndServe(addr, mux)
}

func (s *UIServer) requireWriteAccess(w http.ResponseWriter, r *http.Request) bool {
	if !sameOriginOrNoOrigin(r) {
		http.Error(w, "Forbidden origin", http.StatusForbidden)
		return false
	}
	if key := os.Getenv("FREELLM_MASTER_KEY"); key != "" && r.Header.Get("Authorization") != "Bearer "+key {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func sameOriginOrNoOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}

func (s *UIServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	if s.Proxy == nil {
		http.Error(w, "Proxy not connected", 500)
		return
	}
	r.URL.Path = r.URL.Path[10:]
	s.Proxy.ServeHTTP(w, r)
}

func (s *UIServer) handleSavings(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		http.Error(w, "DB not connected", 500)
		return
	}
	total, _ := db.GetTotalSavings(s.DB)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]float64{"total": total})
}

func (s *UIServer) handleClearSkips(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !s.requireWriteAccess(w, r) {
		return
	}
	if err := db.ClearSkips(s.DB); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

func (s *UIServer) handleClearBlacklist(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !s.requireWriteAccess(w, r) {
		return
	}
	if err := db.ClearBlacklist(s.DB); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

func (s *UIServer) handleResetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !s.requireWriteAccess(w, r) {
		return
	}
	if err := db.ResetStats(s.DB); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

func (s *UIServer) handleModelSkip(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !s.requireWriteAccess(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	if err := db.SkipModel(s.DB, id, 24); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

func (s *UIServer) handleModelBlacklist(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !s.requireWriteAccess(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	if err := db.BlacklistModel(s.DB, id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

func (s *UIServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.Logger == nil {
		http.Error(w, "Logger not connected", 500)
		return
	}
	logs := s.Logger.GetLogs()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

func (s *UIServer) handleProviderToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if !s.requireWriteAccess(w, r) {
		return
	}
	name := r.URL.Query().Get("name")
	enabled := r.URL.Query().Get("enabled") == "true"
	if err := db.SetProviderStatus(s.DB, name, enabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
}

func (s *UIServer) handleLogWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: sameOriginOrNoOrigin,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS upgrade error: %v", err)
		return
	}
	defer conn.Close()

	lastCount := 0
	for {
		if s.Logger == nil {
			break
		}
		logs := s.Logger.GetLogs()
		if len(logs) > lastCount {
			for i := lastCount; i < len(logs); i++ {
				if err := conn.WriteJSON(logs[i]); err != nil {
					return
				}
			}
			lastCount = len(logs)
		}
		time.Sleep(1 * time.Second)
	}
}

func (s *UIServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		http.Error(w, "DB not connected", 500)
		return
	}
	metrics, err := db.GetStabilityMetrics(s.DB, 20)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func (s *UIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		data, err := os.ReadFile(s.ConfigPath)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		w.Write(data)
	} else if r.Method == "POST" {
		if !s.requireWriteAccess(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := validateConfig(body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeFileAtomic(s.ConfigPath, body, 0644); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(200)
	}
}

func validateConfig(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("config cannot be empty")
	}
	var cfg config.Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return fmt.Errorf("invalid yaml: %w", err)
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535")
	}
	if cfg.ProxySettings.RequestTimeout < 0 ||
		cfg.ProxySettings.StreamTimeout < 0 ||
		cfg.ProxySettings.ConnectTimeout < 0 ||
		cfg.ProxySettings.WatchdogTimeout < 0 ||
		cfg.ProxySettings.ProvenWatchdogTimeout < 0 ||
		cfg.ProxySettings.ReasoningWatchdogTimeout < 0 ||
		cfg.ProxySettings.LockDuration < 0 ||
		cfg.ProxySettings.SmartSwitchDelay < 0 ||
		cfg.ProxySettings.FanOutSize < 0 ||
		cfg.ProxySettings.AllowedFails < 0 ||
		cfg.ProxySettings.CooldownTime < 0 {
		return fmt.Errorf("proxy settings cannot contain negative durations or counts")
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".freellm-config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err == nil {
		return nil
	}

	backupPath := path + ".bak"
	_ = os.Remove(backupPath)
	if err := os.Rename(path, backupPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}

func (s *UIServer) handleRankings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.RankedModels)
}

func (s *UIServer) handleProviderHealth(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		http.Error(w, "DB not connected", 500)
		return
	}

	health, err := db.GetProviderHealth(s.DB)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	results := make(map[string]interface{})
	for _, h := range health {
		status := "healthy"
		if h.SuccessRate < 50 && h.SuccessRate > 0 {
			status = "unstable"
		} else if h.SuccessRate == 0 {
			status = "offline"
		}
		results[h.Name] = map[string]interface{}{
			"status":       status,
			"avg_latency":  h.AvgLatency,
			"success_rate": h.SuccessRate,
			"enabled":      h.Enabled,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}
