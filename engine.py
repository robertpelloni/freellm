import asyncio
import httpx
import re
import time
from typing import List, Dict, Any, Optional
import database

# Constants
OPENROUTER_MODELS_URL = "https://openrouter.ai/api/v1/models"
GROQ_MODELS_URL = "https://api.groq.com/openai/v1/models"
TOGETHER_MODELS_URL = "https://api.together.xyz/v1/models"
DEEPINFRA_MODELS_URL = "https://api.deepinfra.com/v1/openai/models"
CEREBRAS_MODELS_URL = "https://api.cerebras.ai/v1/models"
OLLAMA_MODELS_URL = "http://localhost:11434/api/tags"
LM_STUDIO_MODELS_URL = "http://localhost:1234/v1/models"
MIN_PARAMETERS_BILLIONS = 100
# These are defaults, will be overridden by settings
SIZE_WEIGHT = 0.6
CONTEXT_WEIGHT = 0.2
LATENCY_WEIGHT = 0.2

# Regex to extract parameter size (e.g., 405b, 70B, 120b-instruct)
SIZE_PATTERN = re.compile(r'(\d+)[bB]')

# Global exclusions for models we don't want
GLOBAL_EXCLUSIONS = ["-preview", "-base", "vision", "dummy"]

class ModelEngine:
    def __init__(self, api_keys: Dict[str, str], min_params: int = MIN_PARAMETERS_BILLIONS, weights: Dict[str, float] = None, base_urls: Dict[str, str] = None):
        self.api_keys = api_keys
        self.min_params = min_params
        self.weights = weights or {"size": 0.6, "context": 0.2, "latency": 0.2}
        self.base_urls = base_urls or {}
        self.client = httpx.AsyncClient(timeout=10.0)

    async def fetch_openrouter_models(self) -> List[Dict[str, Any]]:
        """Fetches free models from OpenRouter."""
        url = self.base_urls.get("openrouter", OPENROUTER_MODELS_URL.replace("/models", "")) + "/models"
        try:
            response = await self.client.get(url)
            if response.status_code != 200:
                print(f"Error fetching OpenRouter models: {response.status_code}")
                return []

            data = response.json()
            all_models = data.get("data", [])

            free_models = []
            for m in all_models:
                pricing = m.get("pricing", {})
                # Check if free
                if float(pricing.get("prompt", 0)) == 0 and float(pricing.get("completion", 0)) == 0:
                    model_id = m.get("id")
                    params = self.extract_parameters(m)

                    if params >= MIN_PARAMETERS_BILLIONS:
                        free_models.append({
                            "id": model_id,
                            "provider": "openrouter",
                            "parameters": params,
                            "context_length": m.get("context_length", 0)
                        })
            return free_models
        except Exception as e:
            print(f"Exception fetching OpenRouter models: {e}")
            return []

    def extract_parameters(self, model_data: Dict[str, Any]) -> int:
        """Extracts parameter count from model ID or description."""
        model_id = model_data.get("id", "")
        name = model_data.get("name", "")
        description = model_data.get("description", "")

        # Try metadata if available (some APIs provide it)
        # OpenRouter doesn't always have it in a structured way in the list view

        # Search in ID, name, and description
        for text in [model_id, name, description]:
            match = SIZE_PATTERN.search(text)
            if match:
                return int(match.group(1))

        # Hardcoded fallback for known large models if regex fails
        if "llama-3.1-405b" in model_id:
            return 405
        if "llama-3-70b" in model_id:
            return 70 # Below threshold but just for example

        return 0

    def calculate_score(self, params: int, latency: float, context_length: int = 4096) -> float:
        """
        Calculates the model score based on parameters, context length, and latency.
        """
        size_score = (params / 100.0) * self.weights.get("size", 0.6)
        context_score = (min(context_length, 128000) / 128000.0) * self.weights.get("context", 0.2)
        latency_penalty = latency * self.weights.get("latency", 0.2)
        return size_score + context_score - latency_penalty

    async def fetch_groq_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Groq."""
        api_key = self.api_keys.get("groq")
        if not api_key: return []
        url = self.base_urls.get("groq", GROQ_MODELS_URL.replace("/models", "")) + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    # Groq doesn't easily flag 'free' in the models list, but many are free-tier.
                    # For this tool, we assume the user knows their tier or we just test them.
                    params = self.extract_parameters(m)
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "groq",
                            "parameters": params
                        })
                return models
        except: pass
        return []

    async def fetch_together_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Together AI."""
        api_key = self.api_keys.get("together")
        if not api_key: return []
        url = self.base_urls.get("together", TOGETHER_MODELS_URL.replace("/v1/models", "")) + "/v1/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json()
                models = []
                for m in data:
                    params = self.extract_parameters(m)
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "together",
                            "parameters": params
                        })
                return models
        except: pass
        return []

    async def fetch_deepinfra_models(self) -> List[Dict[str, Any]]:
        """Fetches models from DeepInfra."""
        api_key = self.api_keys.get("deepinfra")
        if not api_key: return []
        url = self.base_urls.get("deepinfra", DEEPINFRA_MODELS_URL.replace("/openai/models", "")) + "/openai/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    params = self.extract_parameters(m)
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "deepinfra",
                            "parameters": params
                        })
                return models
        except: pass
        return []

    async def fetch_cerebras_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Cerebras."""
        api_key = self.api_keys.get("cerebras")
        if not api_key: return []
        url = self.base_urls.get("cerebras", CEREBRAS_MODELS_URL.replace("/models", "")) + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    params = self.extract_parameters(m)
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "cerebras",
                            "parameters": params
                        })
                return models
        except: pass
        return []

    async def fetch_ollama_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Ollama."""
        url = self.base_urls.get("ollama", OLLAMA_MODELS_URL.replace("/api/tags", "")) + "/api/tags"
        try:
            response = await self.client.get(url)
            if response.status_code == 200:
                data = response.json().get("models", [])
                models = []
                for m in data:
                    name = m.get("name")
                    params = self.extract_parameters(m)
                    # For local models, we might be more lenient or user wants to see them
                    if params >= self.min_params or params == 0:
                        models.append({
                            "id": name,
                            "provider": "ollama",
                            "parameters": params
                        })
                return models
        except: pass
        return []

    async def fetch_lm_studio_models(self) -> List[Dict[str, Any]]:
        """Fetches models from LM Studio."""
        url = self.base_urls.get("lm_studio", LM_STUDIO_MODELS_URL.replace("/v1/models", "")) + "/v1/models"
        try:
            response = await self.client.get(url)
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    name = m.get("id")
                    params = self.extract_parameters(m)
                    if params >= self.min_params or params == 0:
                        models.append({
                            "id": name,
                            "provider": "lm_studio",
                            "parameters": params
                        })
                return models
        except: pass
        return []

    async def get_ranked_models(self) -> List[Dict[str, Any]]:
        """Main loop to fetch, test, and rank models."""
        # 0. Check which providers are still considered "free"
        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT provider_name FROM providers WHERE is_free_provider = 0")
        blacklisted_providers = {row[0] for row in cursor.fetchall()}
        conn.close()

        # 1. Fetch from providers
        candidates = []
        if "openrouter" not in blacklisted_providers:
            or_models = await self.fetch_openrouter_models()
            candidates.extend(or_models)
            database.update_provider_cycle("openrouter", len(or_models) > 0)

        if "groq" not in blacklisted_providers:
            groq_models = await self.fetch_groq_models()
            candidates.extend(groq_models)
            database.update_provider_cycle("groq", len(groq_models) > 0)

        if "together" not in blacklisted_providers:
            together_models = await self.fetch_together_models()
            candidates.extend(together_models)
            database.update_provider_cycle("together", len(together_models) > 0)

        if "deepinfra" not in blacklisted_providers:
            deepinfra_models = await self.fetch_deepinfra_models()
            candidates.extend(deepinfra_models)
            database.update_provider_cycle("deepinfra", len(deepinfra_models) > 0)

        if "cerebras" not in blacklisted_providers:
            cerebras_models = await self.fetch_cerebras_models()
            candidates.extend(cerebras_models)
            database.update_provider_cycle("cerebras", len(cerebras_models) > 0)

        # Local providers
        ollama_models = await self.fetch_ollama_models()
        candidates.extend(ollama_models)

        lms_models = await self.fetch_lm_studio_models()
        candidates.extend(lms_models)

        # 2. Filter using database skip/failure status
        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT model_id, manually_skipped, skip_expiry, failure_count, retry_after, is_blacklisted, last_success, avg_latency FROM model_history")
        db_status = {row[0]: row[1:] for row in cursor.fetchall()}
        conn.close()

        valid_candidates = []
        now = database.datetime.datetime.now()
        for m in candidates:
            # Global keyword exclusion check
            if any(exc in m['id'].lower() for exc in GLOBAL_EXCLUSIONS):
                continue

            status = db_status.get(m['id'])
            if status:
                skipped, skip_expiry, failures, retry_after, blacklisted = status

                if blacklisted:
                    continue

                # Manual skip check
                if skipped:
                    if skip_expiry:
                        expiry_dt = database.datetime.datetime.fromisoformat(skip_expiry) if isinstance(skip_expiry, str) else skip_expiry
                        if now < expiry_dt:
                            continue

                # Circuit breaker check
                if failures >= 3:
                    if retry_after:
                        retry_dt = database.datetime.datetime.fromisoformat(retry_after) if isinstance(retry_after, str) else retry_after
                        if now < retry_dt:
                            continue

            valid_candidates.append(m)

        # 3. Benchmark in parallel (limited concurrency)
        # Smart Cache: identify local models and check if they need benchmarking
        tasks = []
        benchmarking_models = []

        now = database.datetime.datetime.now()
        cached_results = []

        for m in valid_candidates:
            is_local = m['provider'] in ['ollama', 'lm_studio']
            status = db_status.get(m['id'])

            # Smart Cache: If local and benchmarked in the last 15 minutes, reuse latency
            if is_local and status:
                last_success, avg_latency = status[5], status[6]
                if last_success and avg_latency > 0:
                    last_success_dt = database.datetime.datetime.fromisoformat(last_success) if isinstance(last_success, str) else last_success
                    if now - last_success_dt < database.datetime.timedelta(minutes=15):
                        cached_results.append((m, avg_latency))
                        continue

            tasks.append(self.measure_latency(m['id'], m['provider']))
            benchmarking_models.append(m)

        latencies = await asyncio.gather(*tasks)

        ranked_list = []
        # Add benchmarking results
        for m, latency in zip(valid_candidates, latencies):
            if latency is not None:
                score = self.calculate_score(m['parameters'], latency, m.get('context_length', 4096))
                m['latency'] = latency
                m['score'] = score
                ranked_list.append(m)
                # Update DB
                database.update_model_latency(m['id'], m['provider'], latency)

        # Add cached results
        for m, latency in cached_results:
            score = self.calculate_score(m['parameters'], latency, m.get('context_length', 4096))
            m['latency'] = latency
            m['score'] = score
            ranked_list.append(m)

        # 4. Sort by score
        ranked_list.sort(key=lambda x: x['score'], reverse=True)
        return ranked_list[:10]

    async def check_connectivity(self) -> bool:
        """Simple check to see if the internet is accessible."""
        try:
            # Ping a very reliable endpoint
            response = await self.client.get("https://1.1.1.1", timeout=2.0)
            return response.status_code == 200
        except:
            return False

    async def measure_latency(self, model_id: str, provider: str) -> Optional[float]:
        """Measures Time-To-First-Token (TTFT) for a given model."""
        base = self.base_urls.get(provider)

        if provider == "openrouter":
            url = (base or "https://openrouter.ai/api/v1") + "/chat/completions"
        elif provider == "groq":
            url = (base or "https://api.groq.com/openai/v1") + "/chat/completions"
        elif provider == "together":
            url = (base or "https://api.together.xyz/v1") + "/chat/completions"
        elif provider == "deepinfra":
            url = (base or "https://api.deepinfra.com/v1/openai") + "/chat/completions"
        elif provider == "cerebras":
            url = (base or "https://api.cerebras.ai/v1") + "/chat/completions"
        elif provider == "ollama":
            # Ollama /v1/chat/completions is usually at /v1/chat/completions or /api/chat
            url = (base or "http://localhost:11434") + "/v1/chat/completions"
        elif provider == "lm_studio":
            url = (base or "http://localhost:1234") + "/v1/chat/completions"
        else:
            return None

        api_key = self.api_keys.get(provider)

        if not api_key and provider == "openrouter":
            # Some models on OpenRouter are free without an API key?
            # Actually, usually a key is required even for free models to track limits.
            # But let's assume one is provided in the actual app.
            pass

        headers = {
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json"
        }

        payload = {
            "model": model_id,
            "messages": [{"role": "user", "content": "hi"}],
            "max_tokens": 1,
            "stream": True # Streaming helps measure TTFT precisely
        }

        try:
            start_time = time.perf_counter()
            async with self.client.stream("POST", url, headers=headers, json=payload) as response:
                if response.status_code == 200:
                    async for line in response.aiter_lines():
                        if line:
                            ttft = time.perf_counter() - start_time
                            return ttft
                elif response.status_code in [429, 504]:
                    print(f"Rate limited or timeout for {model_id} ({response.status_code})")
                    database.handle_test_failure(model_id, provider)
                    return None
                else:
                    print(f"Error {response.status_code} for {model_id}: {await response.aread()}")
                    database.handle_test_failure(model_id, provider)
                    return None
        except Exception as e:
            print(f"Exception measuring latency for {model_id}: {e}")
            database.handle_test_failure(model_id, provider)
            return None

        return None

    async def close(self):
        await self.client.aclose()

async def main():
    # Simple test run
    engine = ModelEngine(api_keys={})
    print("Fetching models...")
    models = await engine.fetch_openrouter_models()
    print(f"Found {len(models)} free models >= 100B:")
    for m in models:
        print(f" - {m['id']} ({m['parameters']}B)")
    await engine.close()

if __name__ == "__main__":
    asyncio.run(main())
