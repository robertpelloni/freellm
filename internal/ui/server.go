package ui

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
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

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *UIServer) Start(addr string) error {
	http.HandleFunc("/api/rankings", s.handleRankings)
	http.HandleFunc("/api/metrics", s.handleMetrics)
	http.HandleFunc("/api/logs", s.handleLogs)
	http.HandleFunc("/ws/logs", s.handleLogWS)
	http.HandleFunc("/api/maintenance/clear-skips", s.handleClearSkips)
	http.HandleFunc("/api/maintenance/clear-blacklist", s.handleClearBlacklist)
	http.HandleFunc("/api/maintenance/reset-stats", s.handleResetStats)
	http.HandleFunc("/", s.handleIndex)
	return http.ListenAndServe(addr, nil)
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

func (s *UIServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
    <title>LiteLLM Control Panel</title>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        body { font-family: sans-serif; background: #1a1a1a; color: #eee; padding: 20px; }
        .tab-btn { background: #333; color: #fff; border: none; padding: 10px 20px; cursor: pointer; margin-right: 5px; }
        .tab-btn.active { background: #4caf50; }
        .tab-content { display: none; margin-top: 20px; }
        .tab-content.active { display: block; }
        button.action { background: #f44336; color: white; border: none; padding: 10px 15px; cursor: pointer; border-radius: 4px; margin-right: 10px; }
        button.action:hover { background: #d32f2f; }
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

    <div style="margin-top: 20px;">
        <button class="tab-btn active" onclick="showTab('rankings-tab')">Rankings</button>
        <button class="tab-btn" onclick="showTab('metrics-tab')">Metrics</button>
        <button class="tab-btn" onclick="showTab('logs-tab')">Logs</button>
        <button class="tab-btn" onclick="showTab('maintenance-tab')">Maintenance</button>
    </div>

    <div id="rankings-tab" class="tab-content active">
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
    </div>

    <div id="metrics-tab" class="tab-content">
        <h2>Stability Metrics</h2>
        <div style="height: 300px; margin-bottom: 20px;">
            <canvas id="stabilityChart"></canvas>
        </div>
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
    </div>

    <div id="logs-tab" class="tab-content">
        <h2>Engine Logs</h2>
        <div id="logs"></div>
    </div>

    <div id="maintenance-tab" class="tab-content">
        <h2>System Maintenance</h2>
        <div style="background: #333; padding: 20px; border-radius: 8px;">
            <p>Clear manual model skips (24h duration):</p>
            <button class="action" onclick="doAction('/api/maintenance/clear-skips')">Clear Skip List</button>

            <p style="margin-top: 20px;">Reset all model and provider statistics (latency history, failure counts):</p>
            <button class="action" onclick="doAction('/api/maintenance/reset-stats')">Reset All Stats</button>

            <p style="margin-top: 20px;">Clear model blacklist:</p>
            <button class="action" onclick="doAction('/api/maintenance/clear-blacklist')">Clear Blacklist</button>
        </div>
    </div>

    <script>
        let stabilityChart = null;

        function showTab(id) {
            document.querySelectorAll('.tab-content').forEach(t => t.classList.remove('active'));
            document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
            document.getElementById(id).classList.add('active');
            event.target.classList.add('active');
        }

        async function doAction(url) {
            if (!confirm('Are you sure?')) return;
            try {
                const resp = await fetch(url, { method: 'POST' });
                if (resp.ok) alert('Action successful');
                else alert('Action failed: ' + await resp.text());
            } catch (e) {
                alert('Error: ' + e);
            }
        }

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
                    // Update Chart
                    const labels = data.map(m => new Date(m.timestamp).toLocaleTimeString()).reverse();
                    const qpmData = data.map(m => m.qpm).reverse();
                    const tpsData = data.map(m => m.tps).reverse();

                    if (!stabilityChart) {
                        const ctx = document.getElementById('stabilityChart').getContext('2d');
                        stabilityChart = new Chart(ctx, {
                            type: 'line',
                            data: {
                                labels: labels,
                                datasets: [{
                                    label: 'QPM',
                                    data: qpmData,
                                    borderColor: '#4caf50',
                                    tension: 0.1
                                }, {
                                    label: 'TPS',
                                    data: tpsData,
                                    borderColor: '#2196f3',
                                    tension: 0.1
                                }]
                            },
                            options: { responsive: true, maintainAspectRatio: false }
                        });
                    } else {
                        stabilityChart.data.labels = labels;
                        stabilityChart.data.datasets[0].data = qpmData;
                        stabilityChart.data.datasets[1].data = tpsData;
                        stabilityChart.update('none');
                    }

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

            // Logs are now handled by WebSocket (see below)
        }
        setInterval(refresh, 5000);
        refresh();

        // WebSocket Logs
        const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const ws = new WebSocket(wsProtocol + '//' + window.location.host + '/ws/logs');
        ws.onmessage = (event) => {
            const l = JSON.parse(event.data);
            const logDiv = document.getElementById('logs');
            const entry = document.createElement('div');
            entry.innerText = "[" + new Date(l.timestamp).toLocaleTimeString() + "] " + l.message;
            logDiv.appendChild(entry);
            logDiv.scrollTop = logDiv.scrollHeight;
            if (logDiv.childNodes.length > 200) logDiv.removeChild(logDiv.firstChild);
        };
        ws.onerror = (e) => console.error('WS Error:', e);
    </script>
</body>
</html>
	`))
}
