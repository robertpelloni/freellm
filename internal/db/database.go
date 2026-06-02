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

func LogUsage(db *sql.DB, modelID string, promptTokens, completionTokens int) error {
	_, err := db.Exec(
		"INSERT INTO usage (model_id, timestamp, prompt_tokens, completion_tokens) VALUES (?, ?, ?, ?)",
		modelID, time.Now(), promptTokens, completionTokens,
	)
	return err
}
