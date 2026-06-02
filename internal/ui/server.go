package ui

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/robertpelloni/litellm_control_panel/internal/db"
	"github.com/robertpelloni/litellm_control_panel/internal/engine"
)

type UIServer struct {
	mu           sync.RWMutex
	RankedModels engine.RankedModels
	DB           *sql.DB
	Logger       *engine.EventLogger
}

func NewUIServer(database *sql.DB, logger *engine.EventLogger) *UIServer {
	return &UIServer{DB: database, Logger: logger}
}

func (s *UIServer) UpdateModels(models engine.RankedModels) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RankedModels = models
}

func (s *UIServer) Start(addr string) error {
	http.HandleFunc("/api/rankings", s.handleRankings)
	http.HandleFunc("/api/metrics", s.handleMetrics)
	http.HandleFunc("/api/logs", s.handleLogs)
	http.HandleFunc("/", s.handleIndex)
	return http.ListenAndServe(addr, nil)
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

func (s *UIServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
    <title>LiteLLM Control Panel</title>
    <style>
        body { font-family: sans-serif; background: #1a1a1a; color: #eee; padding: 20px; }
        table { width: 100%; border-collapse: collapse; margin-top: 20px; }
        th, td { padding: 12px; text-align: left; border-bottom: 1px solid #444; }
        th { background: #333; }
        .latency { color: #aaa; }
        .score { font-weight: bold; color: #4caf50; }
        #logs { background: #000; color: #0f0; font-family: monospace; padding: 10px; height: 200px; overflow-y: scroll; border: 1px solid #444; }
    </style>
</head>
<body>
    <h1>LiteLLM Control Panel</h1>
    <div id="status">Loading...</div>

    <h2>Model Rankings</h2>
    <table id="rankings">
        <thead>
            <tr>
                <th>Model</th>
                <th>Provider</th>
                <th>Score</th>
                <th>Latency (TTFT)</th>
            </tr>
        </thead>
        <tbody></tbody>
    </table>

    <h2>Stability Metrics</h2>
    <table id="metrics">
        <thead>
            <tr>
                <th>Time</th>
                <th>Queries/Min (QPM)</th>
                <th>Tokens/Sec (TPS)</th>
            </tr>
        </thead>
        <tbody></tbody>
    </table>

    <h2>Engine Logs</h2>
    <div id="logs"></div>

    <script>
        async function refresh() {
            try {
                const resp = await fetch('/api/rankings');
                const data = await resp.json();
                const tbody = document.querySelector('#rankings tbody');
                tbody.innerHTML = '';
                data.forEach((m, i) => {
                    const row = document.createElement('tr');
                    row.innerHTML = ` + "`" + `
                        <td>${i === 0 ? '★ ' : ''}${m.id}</td>
                        <td>${m.provider}</td>
                        <td class="score">${Math.round(m.score)}</td>
                        <td class="latency">${m.latency.toFixed(3)}s</td>
                    ` + "`" + `;
                    tbody.appendChild(row);
                });
                document.getElementById('status').innerText = 'Last updated: ' + new Date().toLocaleTimeString();
            } catch (e) {
                document.getElementById('status').innerText = 'Error loading rankings: ' + e;
            }

            try {
                const resp = await fetch('/api/metrics');
                const data = await resp.json();
                const tbody = document.querySelector('#metrics tbody');
                tbody.innerHTML = '';
                if (data && data.length > 0) {
                    data.forEach(m => {
                        const row = document.createElement('tr');
                        row.innerHTML = ` + "`" + `
                            <td>${new Date(m.timestamp).toLocaleTimeString()}</td>
                            <td>${m.qpm.toFixed(1)}</td>
                            <td>${m.tps.toFixed(1)}</td>
                        ` + "`" + `;
                        tbody.appendChild(row);
                    });
                } else {
                    tbody.innerHTML = '<tr><td colspan="3">No metrics yet</td></tr>';
                }
            } catch (e) {
                console.error('Error loading metrics:', e);
            }

            try {
                const resp = await fetch('/api/logs');
                const data = await resp.json();
                const logDiv = document.getElementById('logs');
                logDiv.innerHTML = data.map(l => "<div>[" + new Date(l.timestamp).toLocaleTimeString() + "] " + l.message + "</div>").join('');
                logDiv.scrollTop = logDiv.scrollHeight;
            } catch (e) {
                console.error('Error loading logs:', e);
            }
        }
        setInterval(refresh, 5000);
        refresh();
    </script>
</body>
</html>
	`))
}
