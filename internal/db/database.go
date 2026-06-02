package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const DBName = "provider_metrics.db"

type Provider struct {
	Name                   string
	IsFreeProvider         bool
	ConsecutiveEmptyCycles int
	LastChecked            time.Time
}

type StabilityMetric struct {
	Timestamp time.Time `json:"timestamp"`
	QPM       float64   `json:"qpm"`
	TPS       float64   `json:"tps"`
}

type ModelHistory struct {
	ModelID              string
	ProviderName         string
	ManuallySkipped      bool
	IsBlacklisted        bool
	SkipExpiry           sql.NullTime
	FailureCount         int
	RetryAfter           sql.NullTime
	AvgLatency           float64
	MinLatency           float64
	MaxLatency           float64
	P50Latency           float64
	P95Latency           float64
	LastSuccess          sql.NullTime
	LastFailure          sql.NullTime
	TotalProbes          int
	TotalSuccesses       int
	TotalFailures        int
	ConsecutiveSuccesses int
	ConsecutiveFailures  int
	UptimePct            float64
	ScoreAvg             float64
	ScoreBest            float64
	ContextLength        int
	Parameters           int
	FirstSeen            time.Time
}

func InitDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", DBName)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	// Create tables
	queries := []string{
		`CREATE TABLE IF NOT EXISTS providers (
            provider_name TEXT PRIMARY KEY,
            is_free_provider BOOLEAN DEFAULT 1,
            consecutive_empty_cycles INTEGER DEFAULT 0,
            last_checked TIMESTAMP
        )`,
		`CREATE TABLE IF NOT EXISTS model_history (
            model_id TEXT PRIMARY KEY,
            provider_name TEXT,
            manually_skipped BOOLEAN DEFAULT 0,
            is_blacklisted BOOLEAN DEFAULT 0,
            skip_expiry TIMESTAMP,
            failure_count INTEGER DEFAULT 0,
            retry_after TIMESTAMP,
            avg_latency REAL DEFAULT 0,
            min_latency REAL DEFAULT 999,
            max_latency REAL DEFAULT 0,
            p50_latency REAL DEFAULT 0,
            p95_latency REAL DEFAULT 0,
            last_success TIMESTAMP,
            last_failure TIMESTAMP,
            total_probes INTEGER DEFAULT 0,
            total_successes INTEGER DEFAULT 0,
            total_failures INTEGER DEFAULT 0,
            consecutive_successes INTEGER DEFAULT 0,
            consecutive_failures INTEGER DEFAULT 0,
            uptime_pct REAL DEFAULT 0,
            score_avg REAL DEFAULT 0,
            score_best REAL DEFAULT 0,
            context_length INTEGER DEFAULT 0,
            parameters INTEGER DEFAULT 0,
            first_seen TIMESTAMP,
            FOREIGN KEY (provider_name) REFERENCES providers(provider_name)
        )`,
		`CREATE TABLE IF NOT EXISTS probe_history (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            model_id TEXT NOT NULL,
            provider_name TEXT NOT NULL,
            timestamp TIMESTAMP NOT NULL,
            latency REAL,
            success BOOLEAN NOT NULL,
            error_code INTEGER,
            error_message TEXT,
            score REAL DEFAULT 0,
            context_length INTEGER DEFAULT 0,
            parameters INTEGER DEFAULT 0
        )`,
		`CREATE TABLE IF NOT EXISTS usage (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            model_id TEXT,
            timestamp TIMESTAMP,
            prompt_tokens INTEGER,
            completion_tokens INTEGER,
            cost_saved REAL DEFAULT 0
        )`,
		`CREATE TABLE IF NOT EXISTS model_pricing (
            model_id TEXT PRIMARY KEY,
            provider TEXT,
            prompt_price REAL,
            completion_price REAL,
            last_updated TIMESTAMP
        )`,
		`CREATE TABLE IF NOT EXISTS activity_log (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timestamp TIMESTAMP NOT NULL,
            event_type TEXT NOT NULL,
            model_id TEXT,
            details TEXT
        )`,
		`CREATE TABLE IF NOT EXISTS stability_metrics (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timestamp TIMESTAMP NOT NULL,
            qpm REAL,
            tps REAL
        )`,
		`CREATE TABLE IF NOT EXISTS pending_requests (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timestamp TIMESTAMP NOT NULL,
            method TEXT,
            url TEXT,
            headers TEXT,
            body BLOB
        )`,
		`CREATE INDEX IF NOT EXISTS idx_probe_model ON probe_history(model_id)`,
		`CREATE INDEX IF NOT EXISTS idx_probe_time ON probe_history(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_probe_success ON probe_history(success)`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return nil, fmt.Errorf("failed to execute query %s: %v", q, err)
		}
	}

	// Schema Migration: Ensure cost_saved exists in usage table
	if !columnExists(db, "usage", "cost_saved") {
		_, err = db.Exec("ALTER TABLE usage ADD COLUMN cost_saved REAL DEFAULT 0")
		if err != nil {
			return nil, fmt.Errorf("failed to add cost_saved column: %v", err)
		}
	}

	return db, nil
}

func columnExists(db *sql.DB, tableName, columnName string) bool {
	query := fmt.Sprintf("PRAGMA table_info(%s)", tableName)
	rows, err := db.Query(query)
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, dataType string
		var notnull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notnull, &dfltValue, &pk); err != nil {
			continue
		}
		if name == columnName {
			return true
		}
	}
	return false
}

func LogActivity(db *sql.DB, eventType, modelID, details string) error {
	_, err := db.Exec(
		"INSERT INTO activity_log (timestamp, event_type, model_id, details) VALUES (?, ?, ?, ?)",
		time.Now(), eventType, modelID, details,
	)
	return err
}

func ClearSkips(db *sql.DB) error {
	_, err := db.Exec("UPDATE model_history SET manually_skipped = 0, skip_expiry = NULL")
	return err
}

func ClearBlacklist(db *sql.DB) error {
	_, err := db.Exec("UPDATE model_history SET is_blacklisted = 0")
	return err
}

func ResetStats(db *sql.DB) error {
	_, err := db.Exec("UPDATE providers SET consecutive_empty_cycles = 0, is_free_provider = 1")
	if err != nil { return err }
	_, err = db.Exec(`UPDATE model_history SET
        failure_count = 0, retry_after = NULL,
        avg_latency = 0, min_latency = 999, max_latency = 0, p50_latency = 0,
        total_probes = 0, total_successes = 0, total_failures = 0,
        consecutive_successes = 0, consecutive_failures = 0, uptime_pct = 0,
        score_avg = 0, score_best = 0`)
	return err
}

func GetStabilityMetrics(db *sql.DB, limit int) ([]StabilityMetric, error) {
	rows, err := db.Query("SELECT timestamp, qpm, tps FROM stability_metrics ORDER BY timestamp DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []StabilityMetric
	for rows.Next() {
		var m StabilityMetric
		if err := rows.Scan(&m.Timestamp, &m.QPM, &m.TPS); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, nil
}

type QueuedRequest struct {
	ID      int64
	Method  string
	URL     string
	Headers string
	Body    []byte
}

func EnqueueRequest(db *sql.DB, method, url, headers string, body []byte) (int64, error) {
	res, err := db.Exec(
		"INSERT INTO pending_requests (timestamp, method, url, headers, body) VALUES (?, ?, ?, ?, ?)",
		time.Now(), method, url, headers, body,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func DequeueRequest(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM pending_requests WHERE id = ?", id)
	return err
}

func GetPendingRequests(db *sql.DB) ([]QueuedRequest, error) {
	rows, err := db.Query("SELECT id, method, url, headers, body FROM pending_requests ORDER BY timestamp ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []QueuedRequest
	for rows.Next() {
		var r QueuedRequest
		if err := rows.Scan(&r.ID, &r.Method, &r.URL, &r.Headers, &r.Body); err != nil {
			return nil, err
		}
		requests = append(requests, r)
	}
	return requests, nil
}

func LogUsage(db *sql.DB, modelID string, promptTokens, completionTokens int) error {
	var promptPrice, completionPrice float64
	db.QueryRow("SELECT prompt_price, completion_price FROM model_pricing WHERE model_id = ?", modelID).Scan(&promptPrice, &completionPrice)

	costSaved := (float64(promptTokens) * promptPrice) + (float64(completionTokens) * completionPrice)

	_, err := db.Exec(
		"INSERT INTO usage (model_id, timestamp, prompt_tokens, completion_tokens, cost_saved) VALUES (?, ?, ?, ?, ?)",
		modelID, time.Now(), promptTokens, completionTokens, costSaved,
	)
	return err
}

func UpdateModelPricing(db *sql.DB, modelID, provider string, promptPrice, completionPrice float64) error {
	_, err := db.Exec(`
        INSERT INTO model_pricing (model_id, provider, prompt_price, completion_price, last_updated)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(model_id) DO UPDATE SET
            prompt_price = EXCLUDED.prompt_price,
            completion_price = EXCLUDED.completion_price,
            last_updated = EXCLUDED.last_updated`,
		modelID, provider, promptPrice, completionPrice, time.Now())
	return err
}

func GetTotalSavings(db *sql.DB) (float64, error) {
	var total float64
	err := db.QueryRow("SELECT SUM(cost_saved) FROM usage").Scan(&total)
	return total, err
}
