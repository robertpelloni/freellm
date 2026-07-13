import sqlite3

def enable_406():
    try:
        conn = sqlite3.connect('provider_metrics.db')
        c = conn.cursor()
        
        # Check current disabled models
        c.execute("SELECT model_id, provider_name, disabled_reason FROM model_history WHERE disabled=1")
        disabled_models = c.fetchall()
        print(f"Total disabled models before update: {len(disabled_models)}")
        for model in disabled_models:
            print(f" - {model[0]} ({model[1]}): {model[2]}")
        
        c.execute("UPDATE model_history SET disabled = 0, disabled_reason = NULL WHERE disabled_reason LIKE '%406%'")
        conn.commit()
        print(f"\nUpdated {c.rowcount} models.")
        
        c.execute("SELECT COUNT(*) FROM model_history WHERE disabled=1")
        print(f"Total disabled models after update: {c.fetchone()[0]}")
    except Exception as e:
        print(f"Error: {e}")
    finally:
        if 'conn' in locals():
            conn.close()

if __name__ == '__main__':
    enable_406()
