package ui

import (
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/robertpelloni/litellm_control_panel/internal/db"
	"github.com/robertpelloni/litellm_control_panel/internal/engine"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

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
}

func NewUIServer(database *sql.DB, logger *engine.EventLogger, proxy ProxyHandler) *UIServer {
	return &UIServer{DB: database, Logger: logger, Proxy: proxy}
}

func (s *UIServer) UpdateModels(models engine.RankedModels) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RankedModels = models
}

func (s *UIServer) Start(addr string) error {
	staticFS, _ := fs.Sub(staticAssets, "static")
	fileServer := http.FileServer(http.FS(staticFS))

	http.HandleFunc("/api/rankings", s.handleRankings)
	http.HandleFunc("/api/metrics", s.handleMetrics)
	http.HandleFunc("/api/logs", s.handleLogs)
	http.HandleFunc("/ws/logs", s.handleLogWS)
	http.HandleFunc("/api/savings", s.handleSavings)
	http.HandleFunc("/api/proxy/", s.handleProxy)
	http.HandleFunc("/api/maintenance/clear-skips", s.handleClearSkips)
	http.HandleFunc("/api/maintenance/clear-blacklist", s.handleClearBlacklist)
	http.HandleFunc("/api/maintenance/reset-stats", s.handleResetStats)
	http.Handle("/static/", http.StripPrefix("/static/", fileServer))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		index, _ := staticAssets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(index)
	})
	return http.ListenAndServe(addr, nil)
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
	if s.DB == nil { http.Error(w, "DB not connected", 500); return }
	total, _ := db.GetTotalSavings(s.DB)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]float64{"total": total})
}

func (s *UIServer) handleClearSkips(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { http.Error(w, "Method not allowed", 405); return }
	if err := db.ClearSkips(s.DB); err != nil { http.Error(w, err.Error(), 500); return }
	w.WriteHeader(200)
}

func (s *UIServer) handleClearBlacklist(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { http.Error(w, "Method not allowed", 405); return }
	if err := db.ClearBlacklist(s.DB); err != nil { http.Error(w, err.Error(), 500); return }
	w.WriteHeader(200)
}

func (s *UIServer) handleResetStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { http.Error(w, "Method not allowed", 405); return }
	if err := db.ResetStats(s.DB); err != nil { http.Error(w, err.Error(), 500); return }
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

func (s *UIServer) handleLogWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS upgrade error: %v", err)
		return
	}
	defer conn.Close()

	lastCount := 0
	for {
		if s.Logger == nil { break }
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

func (s *UIServer) handleRankings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.RankedModels)
}
