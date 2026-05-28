import unittest
import os
import database
import engine
import asyncio

class TestLiteLLMControlPanel(unittest.TestCase):
    def setUp(self):
        # Use a test database
        database.DB_NAME = "test_metrics.db"
        if os.path.exists(database.DB_NAME):
            os.remove(database.DB_NAME)
        database.init_db()

    def tearDown(self):
        if os.path.exists(database.DB_NAME):
            os.remove(database.DB_NAME)

    def test_database_init(self):
        self.assertTrue(os.path.exists(database.DB_NAME))
        candidates = database.get_candidate_models()
        self.assertEqual(len(candidates), 0)

    def test_model_latency_update(self):
        database.update_model_latency("test-model", "test-provider", 0.5)
        candidates = database.get_candidate_models()
        self.assertEqual(len(candidates), 1)
        self.assertEqual(candidates[0][0], "test-model")

    def test_circuit_breaker_temporal(self):
        database.update_model_latency("fail-model", "fail-provider", 0.5)
        # Fail 3 times
        database.handle_test_failure("fail-model", "fail-provider")
        database.handle_test_failure("fail-model", "fail-provider")
        database.handle_test_failure("fail-model", "fail-provider")

        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT retry_after FROM model_history WHERE model_id = 'fail-model'")
        retry_after = cursor.fetchone()[0]
        conn.close()
        self.assertIsNotNone(retry_after)

    def test_manual_skip_temporal(self):
        database.update_model_latency("skip-model", "skip-provider", 0.5)
        database.set_model_skip_status("skip-model", True, duration_hours=24)

        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT skip_expiry FROM model_history WHERE model_id = 'skip-model'")
        skip_expiry = cursor.fetchone()[0]
        conn.close()
        self.assertIsNotNone(skip_expiry)

    def test_provider_cycle_logic(self):
        # Initial
        database.update_provider_cycle("p1", found_models=False)
        database.update_provider_cycle("p1", found_models=False)
        database.update_provider_cycle("p1", found_models=False)

        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT is_free_provider FROM providers WHERE provider_name = 'p1'")
        is_free = cursor.fetchone()[0]
        conn.close()
        self.assertEqual(is_free, 0)

    def test_scoring_logic(self):
        e = engine.ModelEngine(api_keys={})
        # Higher params, lower latency = better score
        score1 = e.calculate_score(405, 0.5)
        score2 = e.calculate_score(70, 0.5)
        score3 = e.calculate_score(405, 2.0)

        self.assertGreater(score1, score2)
        self.assertGreater(score1, score3)

    def test_parameter_extraction(self):
        e = engine.ModelEngine(api_keys={})
        self.assertEqual(e.extract_parameters({"id": "llama-3.1-405b-instruct"}), 405)
        self.assertEqual(e.extract_parameters({"name": "Gemma 2 132B"}), 132)
        self.assertEqual(e.extract_parameters({"description": "Massive 100b model"}), 100)

    def test_global_exclusions(self):
        e = engine.ModelEngine(api_keys={})
        # Mocking candidates
        candidates = [
            {"id": "llama-3.1-405b", "provider": "openrouter", "parameters": 405},
            {"id": "llama-3.1-405b-preview", "provider": "openrouter", "parameters": 405}
        ]
        # We need to mock get_ranked_models parts or just test the logic inside
        # Since GLOBAL_EXCLUSIONS is used in get_ranked_models loop

        # Testing logic directly
        filtered = [m for m in candidates if not any(exc in m['id'].lower() for exc in engine.GLOBAL_EXCLUSIONS)]
        self.assertEqual(len(filtered), 1)
        self.assertEqual(filtered[0]['id'], "llama-3.1-405b")

    def test_blacklist(self):
        database.update_model_latency("bad-model", "p1", 0.5)
        database.set_model_blacklist_status("bad-model", True)

        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT is_blacklisted FROM model_history WHERE model_id = 'bad-model'")
        blacklisted = cursor.fetchone()[0]
        conn.close()
        self.assertEqual(blacklisted, 1)

if __name__ == "__main__":
    unittest.main()
