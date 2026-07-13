package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

const (
	SQLiteDriver = "sqlite"
	PostgresDriver = "postgres"
	DefaultSQLitePath = "provider_metrics.db"
)

var (
	ActiveDriver = SQLiteDriver
)

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
	Disabled             bool
	DisabledReason       string
}

func InitDB() (*sql.DB, error) {
	driver := SQLiteDriver
	dsn := DefaultSQLitePath

	if url := os.Getenv("DATABASE_URL"); url != "" {
		driver = PostgresDriver
		dsn = url
	} else if host := os.Getenv("POSTGRES_HOST"); host != "" {
		driver = PostgresDriver
		user := os.Getenv("POSTGRES_USER")
		pass := os.Getenv("POSTGRES_PASSWORD")
		dbName := os.Getenv("POSTGRES_DB")
		port := os.Getenv("POSTGRES_PORT")
		if port == "" {
			port = "5432"
		}
		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, dbName)
	}

	ActiveDriver = driver
	log.Printf("[DB] Initializing with driver: %s", driver)

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	// Set connection pool limits
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("database connection failed: %v", err)
	}

	// Create tables with driver-specific syntax
	queries := []string{
		createTableQuery(`CREATE TABLE IF NOT EXISTS providers (
            provider_name TEXT PRIMARY KEY,
            is_free_provider BOOLEAN DEFAULT TRUE,
            consecutive_empty_cycles INTEGER DEFAULT 0,
            last_checked TIMESTAMP
        )`),
		createTableQuery(`CREATE TABLE IF NOT EXISTS model_history (
            model_id TEXT,
            provider_name TEXT,
            manually_skipped BOOLEAN DEFAULT FALSE,
            is_blacklisted BOOLEAN DEFAULT FALSE,
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
            disabled BOOLEAN DEFAULT FALSE,
            disabled_reason TEXT,
            PRIMARY KEY (model_id, provider_name)
        )`),
		createTableQuery(`CREATE TABLE IF NOT EXISTS probe_history (
            id %s,
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
        )`, primaryKeyType()),
		createTableQuery(`CREATE TABLE IF NOT EXISTS usage (
            id %s,
            model_id TEXT,
            timestamp TIMESTAMP,
            prompt_tokens INTEGER,
            completion_tokens INTEGER,
            cost_saved REAL DEFAULT 0
        )`, primaryKeyType()),
		createTableQuery(`CREATE TABLE IF NOT EXISTS model_pricing (
            model_id TEXT PRIMARY KEY,
            provider TEXT,
            prompt_price REAL,
            completion_price REAL,
            last_updated TIMESTAMP
        )`),
		createTableQuery(`CREATE TABLE IF NOT EXISTS activity_log (
            id %s,
            timestamp TIMESTAMP NOT NULL,
            event_type TEXT NOT NULL,
            model_id TEXT,
            details TEXT
        )`, primaryKeyType()),
		createTableQuery(`CREATE TABLE IF NOT EXISTS stability_metrics (
            id %s,
            timestamp TIMESTAMP NOT NULL,
            qpm REAL,
            tps REAL
        )`, primaryKeyType()),
		createTableQuery(`CREATE TABLE IF NOT EXISTS pending_requests (
            id %s,
            timestamp TIMESTAMP NOT NULL,
            method TEXT,
            url TEXT,
            headers TEXT,
            body %s
        )`, primaryKeyType(), blobType()),
		createTableQuery(`CREATE TABLE IF NOT EXISTS persistent_logs (
            id %s,
            timestamp TIMESTAMP NOT NULL,
            message TEXT
        )`, primaryKeyType()),
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return nil, fmt.Errorf("failed to execute query %s: %v", q, err)
		}
	}

	// Migration for existing tables: add disabled and disabled_reason if they don't exist
	if !columnExists(db, "model_history", "disabled") {
		log.Printf("[DB] Migration: adding 'disabled' column to model_history")
		_, _ = db.Exec("ALTER TABLE model_history ADD COLUMN disabled BOOLEAN DEFAULT FALSE")
	}
	if !columnExists(db, "model_history", "disabled_reason") {
		log.Printf("[DB] Migration: adding 'disabled_reason' column to model_history")
		_, _ = db.Exec("ALTER TABLE model_history ADD COLUMN disabled_reason TEXT")
	}

	// Create indexes
	indexQueries := []string{
		"CREATE INDEX IF NOT EXISTS idx_probe_model ON probe_history(model_id)",
		"CREATE INDEX IF NOT EXISTS idx_probe_time ON probe_history(timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_probe_success ON probe_history(success)",
	}
	for _, q := range indexQueries {
		db.Exec(q) // Ignore errors if index already exists
	}

	return db, nil
}

func primaryKeyType() string {
	if ActiveDriver == PostgresDriver {
		return "SERIAL PRIMARY KEY"
	}
	return "INTEGER PRIMARY KEY AUTOINCREMENT"
}

func blobType() string {
	if ActiveDriver == PostgresDriver {
		return "BYTEA"
	}
	return "BLOB"
}

func createTableQuery(q string, args ...interface{}) string {
	return fmt.Sprintf(q, args...)
}

func p(query string) string {
	if ActiveDriver != PostgresDriver {
		return query
	}
	// Simple converter for ? to $n
	var result strings.Builder
	paramIdx := 1
	for _, char := range query {
		if char == '?' {
			fmt.Fprintf(&result, "$%d", paramIdx)
			paramIdx++
		} else {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func columnExists(db *sql.DB, tableName, columnName string) bool {
	if ActiveDriver == PostgresDriver {
		var exists bool
		query := `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name=$1 AND column_name=$2)`
		err := db.QueryRow(query, tableName, columnName).Scan(&exists)
		return err == nil && exists
	}

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
		if err := rows.Scan(&cid, &name, &dataType, &notnull, &dfltValue, &pk); err == nil && name == columnName {
			return true
		}
	}
	return false
}

func LogActivity(db *sql.DB, eventType, modelID, details string) error {
	_, err := db.Exec(
		p("INSERT INTO activity_log (timestamp, event_type, model_id, details) VALUES (?, ?, ?, ?)"),
		time.Now(), eventType, modelID, details,
	)
	return err
}

func ClearSkips(db *sql.DB) error {
	_, err := db.Exec(p("UPDATE model_history SET manually_skipped = FALSE, skip_expiry = NULL"))
	return err
}

func ClearBlacklist(db *sql.DB) error {
	_, err := db.Exec(p("UPDATE model_history SET is_blacklisted = FALSE"))
	return err
}

func SkipModel(db *sql.DB, modelID string, hours int) error {
	expiry := time.Now().Add(time.Duration(hours) * time.Hour)
	_, err := db.Exec(p("UPDATE model_history SET manually_skipped = TRUE, skip_expiry = ? WHERE model_id = ?"), expiry, modelID)
	return err
}

func BlacklistModel(db *sql.DB, modelID string) error {
	_, err := db.Exec(p("UPDATE model_history SET is_blacklisted = TRUE WHERE model_id = ?"), modelID)
	return err
}

func AutoBlacklistPermanentErrors(db *sql.DB, modelID, provider, errMsg string) error {
	lower := strings.ToLower(errMsg)
	isPermanent := strings.Contains(lower, "model_not_found") ||
		strings.Contains(lower, "model not found") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "not found for account")

	if !isPermanent {
		return nil
	}

	if ActiveDriver == SQLiteDriver {
		db.Exec(`INSERT OR IGNORE INTO model_history (model_id, provider_name, is_blacklisted, failure_count)
            VALUES (?, ?, 1, 0)`, modelID, provider)
	} else {
		db.Exec(`INSERT INTO model_history (model_id, provider_name, is_blacklisted, failure_count)
            VALUES ($1, $2, TRUE, 0) ON CONFLICT (model_id) DO NOTHING`, modelID, provider)
	}

	_, err := db.Exec(p("UPDATE model_history SET is_blacklisted = TRUE WHERE model_id = ?"), modelID)
	return err
}

func ResetStats(db *sql.DB) error {
	_, err := db.Exec(p("UPDATE providers SET consecutive_empty_cycles = 0, is_free_provider = TRUE"))
	if err != nil {
		return err
	}
	_, err = db.Exec(p(`UPDATE model_history SET
        failure_count = 0, retry_after = NULL,
        avg_latency = 0, min_latency = 999, max_latency = 0, p50_latency = 0,
        total_probes = 0, total_successes = 0, total_failures = 0,
        consecutive_successes = 0, consecutive_failures = 0, uptime_pct = 0,
        score_avg = 0, score_best = 0`))
	return err
}

func GetStabilityMetrics(db *sql.DB, limit int) ([]StabilityMetric, error) {
	rows, err := db.Query(p("SELECT timestamp, qpm, tps FROM stability_metrics ORDER BY timestamp DESC LIMIT ?"), limit)
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
	query := p("INSERT INTO pending_requests (timestamp, method, url, headers, body) VALUES (?, ?, ?, ?, ?)")
	if ActiveDriver == PostgresDriver {
		var id int64
		err := db.QueryRow(query+" RETURNING id", time.Now(), method, url, headers, body).Scan(&id)
		return id, err
	}

	res, err := db.Exec(query, time.Now(), method, url, headers, body)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func DequeueRequest(db *sql.DB, id int64) error {
	_, err := db.Exec(p("DELETE FROM pending_requests WHERE id = ?"), id)
	return err
}

func GetPendingRequests(db *sql.DB) ([]QueuedRequest, error) {
	rows, err := db.Query(p("SELECT id, method, url, headers, body FROM pending_requests ORDER BY timestamp ASC"))
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

func ClearPendingRequests(db *sql.DB) error {
	_, err := db.Exec(p("DELETE FROM pending_requests"))
	return err
}

func GetCircuitBreakerList(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(p("SELECT model_id FROM model_history WHERE failure_count >= 10 AND (retry_after IS NULL OR retry_after > ?)"), time.Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	blocked := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			blocked[id] = true
		}
	}
	return blocked, nil
}

type ProviderHealth struct {
	Name        string  `json:"name"`
	AvgLatency  float64 `json:"avg_latency"`
	SuccessRate float64 `json:"success_rate"`
	Enabled     bool    `json:"enabled"`
}

func GetProviderHealth(db *sql.DB) ([]ProviderHealth, error) {
	var query string
	var cutoff = time.Now().Add(-24 * time.Hour)

	if ActiveDriver == PostgresDriver {
		query = `
            SELECT p.provider_name, COALESCE(AVG(ph.latency), 0),
                   COALESCE(SUM(CASE WHEN ph.success = TRUE THEN 1 ELSE 0 END) * 100.0 / NULLIF(COUNT(ph.id), 0), 0),
                   p.is_free_provider
            FROM providers p
            LEFT JOIN probe_history ph ON p.provider_name = ph.provider_name AND ph.timestamp > $1
            GROUP BY p.provider_name`
	} else {
		query = `
            SELECT p.provider_name, COALESCE(AVG(ph.latency), 0),
                   COALESCE(SUM(CASE WHEN ph.success = 1 THEN 1 ELSE 0 END) * 100.0 / NULLIF(COUNT(ph.id), 0), 0),
                   p.is_free_provider
            FROM providers p
            LEFT JOIN probe_history ph ON p.provider_name = ph.provider_name AND ph.timestamp > ?
            GROUP BY p.provider_name`
	}

	rows, err := db.Query(query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var health []ProviderHealth
	for rows.Next() {
		var h ProviderHealth
		if err := rows.Scan(&h.Name, &h.AvgLatency, &h.SuccessRate, &h.Enabled); err != nil {
			return nil, err
		}
		health = append(health, h)
	}
	return health, nil
}

func SetProviderStatus(db *sql.DB, name string, enabled bool) error {
	_, err := db.Exec(p("UPDATE providers SET is_free_provider = ? WHERE provider_name = ?"), enabled, name)
	return err
}

func LogStabilityMetric(db *sql.DB, qpm, tps float64) error {
	_, err := db.Exec(
		p("INSERT INTO stability_metrics (timestamp, qpm, tps) VALUES (?, ?, ?)"),
		time.Now(), qpm, tps,
	)
	return err
}

func LogPersistent(db *sql.DB, message string) error {
	_, err := db.Exec(p("INSERT INTO persistent_logs (timestamp, message) VALUES (?, ?)"), time.Now(), message)
	return err
}

type LogRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

func GetPersistentLogs(db *sql.DB, limit int) ([]LogRecord, error) {
	rows, err := db.Query(p("SELECT timestamp, message FROM persistent_logs ORDER BY timestamp DESC LIMIT ?"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []LogRecord
	for rows.Next() {
		var l LogRecord
		if err := rows.Scan(&l.Timestamp, &l.Message); err == nil {
			logs = append(logs, l)
		}
	}
	return logs, nil
}

func RecordFailure(db *sql.DB, modelID string) error {
	var currentFailures int
	db.QueryRow(p("SELECT failure_count FROM model_history WHERE model_id = ?"), modelID).Scan(&currentFailures)

	newFailures := currentFailures + 1
	maxF := newFailures
	if maxF > 4 {
		maxF = 4
	}
	cooldownMinutes := (1 << maxF) * 3
	if cooldownMinutes > 48 {
		cooldownMinutes = 48
	}
	retryAfter := time.Now().Add(time.Duration(cooldownMinutes) * time.Minute)

	_, err := db.Exec(p("UPDATE model_history SET failure_count = ?, retry_after = ?, last_failure = ? WHERE model_id = ?"),
		newFailures, retryAfter, time.Now(), modelID)
	return err
}

func RecordSuccess(db *sql.DB, modelID string) error {
	_, err := db.Exec(p("UPDATE model_history SET failure_count = 0, retry_after = NULL, last_success = ? WHERE model_id = ?"),
		time.Now(), modelID)
	return err
}

func LogUsage(db *sql.DB, modelID string, promptTokens, completionTokens int) error {
	var promptPrice, completionPrice float64
	db.QueryRow(p("SELECT prompt_price, completion_price FROM model_pricing WHERE model_id = ?"), modelID).Scan(&promptPrice, &completionPrice)

	costSaved := (float64(promptTokens) * promptPrice) + (float64(completionTokens) * completionPrice)

	_, err := db.Exec(
		p("INSERT INTO usage (model_id, timestamp, prompt_tokens, completion_tokens, cost_saved) VALUES (?, ?, ?, ?, ?)"),
		modelID, time.Now(), promptTokens, completionTokens, costSaved,
	)
	return err
}

func RecordProbe(db *sql.DB, modelID, provider string, latency float64, success bool, errBody string, score float64, ctxLen, params int) error {
	_, err := db.Exec(p(`
        INSERT INTO probe_history (model_id, provider_name, timestamp, latency, success, error_message, score, context_length, parameters)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		modelID, provider, time.Now(), latency, success, errBody, score, ctxLen, params)
	return err
}

func UpdateModelPricing(db *sql.DB, modelID, provider string, promptPrice, completionPrice float64) error {
	if ActiveDriver == SQLiteDriver {
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

	_, err := db.Exec(`
        INSERT INTO model_pricing (model_id, provider, prompt_price, completion_price, last_updated)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT(model_id) DO UPDATE SET
            prompt_price = EXCLUDED.prompt_price,
            completion_price = EXCLUDED.completion_price,
            last_updated = EXCLUDED.last_updated`,
		modelID, provider, promptPrice, completionPrice, time.Now())
	return err
}

func PruneOldData(db *sql.DB, days int) (int64, error) {
	cutoff := time.Now().Add(time.Duration(-days*24) * time.Hour)

	res1, _ := db.Exec(p("DELETE FROM probe_history WHERE timestamp < ?"), cutoff)
	res2, _ := db.Exec(p("DELETE FROM persistent_logs WHERE timestamp < ?"), cutoff)
	res3, _ := db.Exec(p("DELETE FROM stability_metrics WHERE timestamp < ?"), cutoff)

	n1, _ := res1.RowsAffected()
	n2, _ := res2.RowsAffected()
	n3, _ := res3.RowsAffected()

	return n1 + n2 + n3, nil
}

func GetTotalSavings(db *sql.DB) (float64, error) {
	var total float64
	err := db.QueryRow(p("SELECT SUM(cost_saved) FROM usage")).Scan(&total)
	return total, err
}

func GetLastGoodModels(db *sql.DB) ([]struct {
	ModelID     string
	Provider    string
	AvgLatency  float64
	SuccessRate float64
	Probes      int
}, error) {
	var query string
	if ActiveDriver == PostgresDriver {
		query = `
            SELECT model_id, provider_name,
                   COUNT(*) as probes,
                   SUM(CASE WHEN success=TRUE THEN 1 ELSE 0 END) as successes,
                   ROUND(CAST(AVG(CASE WHEN success=TRUE THEN latency ELSE NULL END) AS NUMERIC), 3) as avg_latency
            FROM probe_history 
            WHERE timestamp > NOW() - INTERVAL '3 days'
            GROUP BY model_id, provider_name
            HAVING SUM(CASE WHEN success=TRUE THEN 1 ELSE 0 END) >= 2
            ORDER BY 
                ROUND(CAST(SUM(CASE WHEN success=TRUE THEN 1 ELSE 0 END) * 1.0 / COUNT(*) AS NUMERIC), 2) DESC,
                avg_latency ASC
            LIMIT 50`
	} else {
		query = `
            SELECT model_id, provider_name,
                   COUNT(*) as probes,
                   SUM(CASE WHEN success=1 THEN 1 ELSE 0 END) as successes,
                   ROUND(AVG(CASE WHEN success=1 THEN latency ELSE NULL END), 3) as avg_latency
            FROM probe_history 
            WHERE timestamp > datetime('now', '-3 days')
            GROUP BY model_id, provider_name
            HAVING successes >= 2
            ORDER BY 
                ROUND(successes * 1.0 / probes, 2) DESC,
                avg_latency ASC
            LIMIT 50`
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []struct {
		ModelID     string
		Provider    string
		AvgLatency  float64
		SuccessRate float64
		Probes      int
	}

	for rows.Next() {
		var modelID, provider string
		var probes, successes int
		var avgLatency sql.NullFloat64
		if err := rows.Scan(&modelID, &provider, &probes, &successes, &avgLatency); err != nil {
			continue
		}
		lat := 5.0
		if avgLatency.Valid {
			lat = avgLatency.Float64
		}
		results = append(results, struct {
			ModelID     string
			Provider    string
			AvgLatency  float64
			SuccessRate float64
			Probes      int
		}{
			ModelID:     modelID,
			Provider:    provider,
			AvgLatency:  lat,
			SuccessRate: float64(successes) / float64(probes),
			Probes:      probes,
		})
	}
	return results, nil
}

func DisableModel(db *sql.DB, modelID, provider, reason string) error {
	log.Printf("[DB] Permanently disabling model %s(%s): %s", modelID, provider, reason)
	if ActiveDriver == SQLiteDriver {
		_, err := db.Exec(`
            INSERT INTO model_history (model_id, provider_name, disabled, disabled_reason)
            VALUES (?, ?, 1, ?)
            ON CONFLICT(model_id, provider_name) DO UPDATE SET
                disabled = 1,
                disabled_reason = EXCLUDED.disabled_reason`,
			modelID, provider, reason)
		return err
	}

	_, err := db.Exec(`
        INSERT INTO model_history (model_id, provider_name, disabled, disabled_reason)
        VALUES ($1, $2, TRUE, $3)
        ON CONFLICT(model_id, provider_name) DO UPDATE SET
            disabled = TRUE,
            disabled_reason = EXCLUDED.disabled_reason`,
		modelID, provider, reason)
	return err
}

func GetDisabledModels(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(p("SELECT model_id, provider_name, disabled_reason FROM model_history WHERE disabled = TRUE"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	disabled := make(map[string]string)
	for rows.Next() {
		var id, provider, reason string
		if err := rows.Scan(&id, &provider, &reason); err == nil {
			disabled[id+"|"+provider] = reason
		}
	}
	return disabled, nil
}
