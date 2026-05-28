import sqlite3
import datetime
import os

DB_NAME = "provider_metrics.db"

def init_db():
    """Initializes the SQLite database with the required schema."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()

    # Providers table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS providers (
            provider_name TEXT PRIMARY KEY,
            is_free_provider BOOLEAN DEFAULT 1,
            consecutive_empty_cycles INTEGER DEFAULT 0,
            last_checked TIMESTAMP
        )
    """)

    # Model history table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS model_history (
            model_id TEXT PRIMARY KEY,
            provider_name TEXT,
            manually_skipped BOOLEAN DEFAULT 0,
            is_blacklisted BOOLEAN DEFAULT 0,
            skip_expiry TIMESTAMP,
            failure_count INTEGER DEFAULT 0,
            retry_after TIMESTAMP,
            avg_latency REAL,
            last_success TIMESTAMP,
            FOREIGN KEY (provider_name) REFERENCES providers(provider_name)
        )
    """)

    # Usage table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS usage (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            model_id TEXT,
            timestamp TIMESTAMP,
            prompt_tokens INTEGER,
            completion_tokens INTEGER
        )
    """)

    conn.commit()
    conn.close()

def get_candidate_models():
    """
    Fetches models that are not manually skipped, haven't crashed repeatedly,
    and belong to active free providers.
    """
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()

    query = """
        SELECT model_id, provider_name FROM model_history
        WHERE manually_skipped = 0
          AND failure_count < 3
          AND provider_name IN (SELECT provider_name FROM providers WHERE is_free_provider = 1)
    """
    cursor.execute(query)
    candidates = cursor.fetchall()
    conn.close()
    return candidates

def update_model_latency(model_id, provider_name, latency):
    """Updates the average latency and resets failure count for a successful model check."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()

    # Ensure provider exists
    cursor.execute("INSERT OR IGNORE INTO providers (provider_name) VALUES (?)", (provider_name,))

    # Update model stats
    cursor.execute("""
        INSERT INTO model_history (model_id, provider_name, avg_latency, failure_count, last_success)
        VALUES (?, ?, ?, 0, ?)
        ON CONFLICT(model_id) DO UPDATE SET
            avg_latency = (avg_latency * 0.7 + ? * 0.3),
            failure_count = 0,
            last_success = ?
    """, (model_id, provider_name, latency, datetime.datetime.now(), latency, datetime.datetime.now()))

    conn.commit()
    conn.close()

def handle_test_failure(model_id, provider_name):
    """Increments failure count for a model and sets retry_after if threshold hit."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()

    retry_after = None
    # Check current failure count
    cursor.execute("SELECT failure_count FROM model_history WHERE model_id = ?", (model_id,))
    row = cursor.fetchone()
    count = (row[0] if row else 0) + 1

    if count >= 3:
        # Isolate for 2 hours
        retry_after = datetime.datetime.now() + datetime.timedelta(hours=2)

    cursor.execute("""
        UPDATE model_history
        SET failure_count = ?, retry_after = ?
        WHERE model_id = ?
    """, (count, retry_after, model_id))

    conn.commit()
    conn.close()

def set_model_skip_status(model_id, skipped=True, duration_hours=24):
    """Manually skip a model for a specified duration."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()

    expiry = None
    if skipped:
        expiry = datetime.datetime.now() + datetime.timedelta(hours=duration_hours)

    cursor.execute("""
        UPDATE model_history
        SET manually_skipped = ?, skip_expiry = ?
        WHERE model_id = ?
    """, (1 if skipped else 0, expiry, model_id))
    conn.commit()
    conn.close()

def set_model_blacklist_status(model_id, blacklisted=True):
    """Permanently blacklist a model."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("UPDATE model_history SET is_blacklisted = ? WHERE model_id = ?", (1 if blacklisted else 0, model_id))
    conn.commit()
    conn.close()

def clear_skip_list():
    """Resets all manual skips."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("UPDATE model_history SET manually_skipped = 0, skip_expiry = NULL")
    conn.commit()
    conn.close()

def clear_blacklist():
    """Resets all blacklisted models."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("UPDATE model_history SET is_blacklisted = 0")
    conn.commit()
    conn.close()

def log_usage(model_id, prompt_tokens=0, completion_tokens=0):
    """Logs model usage."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("INSERT INTO usage (model_id, timestamp, prompt_tokens, completion_tokens) VALUES (?, ?, ?, ?)",
                   (model_id, datetime.datetime.now(), prompt_tokens, completion_tokens))
    conn.commit()
    conn.close()

def get_total_usage():
    """Returns total query count and token counts."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("SELECT COUNT(*), SUM(prompt_tokens), SUM(completion_tokens) FROM usage")
    row = cursor.fetchone()
    conn.close()
    return row if row else (0, 0, 0)

def reset_all_stats():
    """Resets provider failures and model histories."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("UPDATE providers SET consecutive_empty_cycles = 0, is_free_provider = 1")
    cursor.execute("UPDATE model_history SET failure_count = 0, retry_after = NULL, avg_latency = 0")
    conn.commit()
    conn.close()

def get_provider_status():
    """Fetches all provider health info."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("SELECT provider_name, is_free_provider, consecutive_empty_cycles, last_checked FROM providers")
    stats = cursor.fetchall()
    conn.close()
    return stats

def get_last_rankings():
    """Fetches top 10 models from history based on last success and latency."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    query = """
        SELECT model_id, provider_name, avg_latency, last_success FROM model_history
        WHERE manually_skipped = 0 AND is_blacklisted = 0 AND avg_latency > 0
        ORDER BY last_success DESC, avg_latency ASC LIMIT 10
    """
    cursor.execute(query)
    rows = cursor.fetchall()
    conn.close()

    results = []
    for r in rows:
        results.append({
            "id": r[0],
            "provider": r[1],
            "parameters": 100, # Placeholder as we don't store it in DB yet
            "latency": r[2],
            "score": 0 # Recalculated later
        })
    return results

def update_provider_cycle(provider_name, found_models: bool):
    """Updates consecutive empty cycles and flags if provider seems to have no free models."""
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()

    if found_models:
        cursor.execute("""
            UPDATE providers SET consecutive_empty_cycles = 0, is_free_provider = 1, last_checked = ?
            WHERE provider_name = ?
        """, (datetime.datetime.now(), provider_name))
    else:
        cursor.execute("SELECT consecutive_empty_cycles FROM providers WHERE provider_name = ?", (provider_name,))
        row = cursor.fetchone()
        count = (row[0] if row else 0) + 1

        is_free = 1 if count < 3 else 0
        cursor.execute("""
            INSERT INTO providers (provider_name, consecutive_empty_cycles, is_free_provider, last_checked)
            VALUES (?, ?, ?, ?)
            ON CONFLICT(provider_name) DO UPDATE SET
                consecutive_empty_cycles = ?,
                is_free_provider = ?,
                last_checked = ?
        """, (provider_name, count, is_free, datetime.datetime.now(), count, is_free, datetime.datetime.now()))

    conn.commit()
    conn.close()

if __name__ == "__main__":
    init_db()
    print("Database initialized successfully.")
