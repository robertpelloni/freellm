package ui

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/robertpelloni/litellm_control_panel/internal/engine"
)

type UIServer struct {
	mu           sync.RWMutex
	RankedModels engine.RankedModels
}

func NewUIServer() *UIServer {
	return &UIServer{}
}

func (s *UIServer) UpdateModels(models engine.RankedModels) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RankedModels = models
}

func (s *UIServer) Start(addr string) error {
	http.HandleFunc("/api/rankings", s.handleRankings)
	http.HandleFunc("/", s.handleIndex)
	return http.ListenAndServe(addr, nil)
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
    </style>
</head>
<body>
    <h1>LiteLLM Rankings</h1>
    <div id="status">Loading...</div>
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
        }
        setInterval(refresh, 5000);
        refresh();
    </script>
</body>
</html>
	`))
}
