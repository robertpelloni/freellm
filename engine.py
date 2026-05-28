import asyncio
import httpx
import re
import time
from typing import List, Dict, Any, Optional
import database
import known_models

# Constants
OPENROUTER_MODELS_URL = "https://openrouter.ai/api/v1/models"
GROQ_MODELS_URL = "https://api.groq.com/openai/v1/models"
TOGETHER_MODELS_URL = "https://api.together.xyz/v1/models"
DEEPINFRA_MODELS_URL = "https://api.deepinfra.com/v1/openai/models"
CEREBRAS_MODELS_URL = "https://api.cerebras.ai/v1/models"
OLLAMA_MODELS_URL = "http://localhost:11434/api/tags"
LM_STUDIO_MODELS_URL = "http://localhost:1234/v1/models"
GITHUB_MODELS_URL = "https://models.inference.ai.azure.com/models"
HF_MODELS_URL = "https://api-inference.huggingface.co/models"
NVIDIA_MODELS_URL = "https://integrate.api.nvidia.com/v1/models"

MIN_PARAMETERS_BILLIONS = 100

# These are defaults, will be overridden by settings
SIZE_WEIGHT = 0.6
CONTEXT_WEIGHT = 0.2
LATENCY_WEIGHT = 0.2

# Regex to extract parameter size (e.g., 405b, 70B, 120b-instruct)
SIZE_PATTERN = re.compile(r'(\d+)[bB]')

# Global exclusions — set by main.py from settings at runtime.
# Default is deliberately minimal; -preview is NOT excluded by default.
GLOBAL_EXCLUSIONS = ["-base", "dummy"]


class ModelEngine:
    def __init__(self, api_keys: Dict[str, str], min_params: int = MIN_PARAMETERS_BILLIONS,
                 weights: Dict[str, float] = None, base_urls: Dict[str, str] = None):
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
                if float(pricing.get("prompt", 0)) == 0 and float(pricing.get("completion", 0)) == 0:
                    model_id = m.get("id")
                    params, context = self._resolve_model_metadata(m, "openrouter")
                    if params >= self.min_params:
                        free_models.append({
                            "id": model_id,
                            "provider": "openrouter",
                            "parameters": params,
                            "context_length": context,
                        })
            return free_models
        except Exception as e:
            print(f"Exception fetching OpenRouter models: {e}")
            return []

    def _resolve_model_metadata(self, model_data: Dict[str, Any],
                                 provider: str) -> tuple:
        """Resolve parameters and context_length for a model.
        Uses known good models list first, then falls back to regex extraction.
        Returns (parameters, context_length).
        """
        model_id = model_data.get("id", "")
        context_length = model_data.get("context_length", 0)

        # 1. Check known good models
        known = known_models.lookup(model_id)
        if known:
            params = known["params"]
            ctx = known.get("ctx", 0)
            # If API provided context_length and it's larger, trust it
            if context_length and context_length > ctx:
                ctx = context_length
            return params, ctx

        # 2. Regex extraction from ID, name, description
        params = self._extract_parameters_from_text(model_data)
        return params, context_length

    def _extract_parameters_from_text(self, model_data: Dict[str, Any]) -> int:
        """Extract parameter count from model ID, name, or description via regex."""
        model_id = model_data.get("id", "")
        name = model_data.get("name", "")
        description = model_data.get("description", "")
        for text in [model_id, name, description]:
            match = SIZE_PATTERN.search(text)
            if match:
                return int(match.group(1))
        return 0

    def calculate_score(self, params: int, latency: float, context_length: int = 4096) -> float:
        """Calculates the model score based on parameters, context length, and latency."""
        size_score = (params / 100.0) * self.weights.get("size", 0.6)
        context_score = (min(context_length, 128000) / 128000.0) * self.weights.get("context", 0.2)
        latency_penalty = latency * self.weights.get("latency", 0.2)
        return size_score + context_score - latency_penalty

    async def fetch_groq_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Groq."""
        api_key = self.api_keys.get("groq")
        if not api_key:
            return []
        url = self.base_urls.get("groq", GROQ_MODELS_URL.replace("/models", "")) + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    params, context = self._resolve_model_metadata(m, "groq")
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "groq",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
        return []

    async def fetch_together_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Together AI."""
        api_key = self.api_keys.get("together")
        if not api_key:
            return []
        url = self.base_urls.get("together", TOGETHER_MODELS_URL.replace("/v1/models", "")) + "/v1/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json()
                models = []
                for m in data:
                    params, context = self._resolve_model_metadata(m, "together")
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "together",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
        return []

    async def fetch_deepinfra_models(self) -> List[Dict[str, Any]]:
        """Fetches models from DeepInfra."""
        api_key = self.api_keys.get("deepinfra")
        if not api_key:
            return []
        url = self.base_urls.get("deepinfra", DEEPINFRA_MODELS_URL.replace("/openai/models", "")) + "/openai/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    params, context = self._resolve_model_metadata(m, "deepinfra")
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "deepinfra",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
        return []

    async def fetch_cerebras_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Cerebras."""
        api_key = self.api_keys.get("cerebras")
        if not api_key:
            return []
        url = self.base_urls.get("cerebras", CEREBRAS_MODELS_URL.replace("/models", "")) + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    params, context = self._resolve_model_metadata(m, "cerebras")
                    if params >= self.min_params:
                        models.append({
                            "id": m.get("id"),
                            "provider": "cerebras",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
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
                    params, context = self._resolve_model_metadata(m, "ollama")
                    if params >= self.min_params or params == 0:
                        models.append({
                            "id": name,
                            "provider": "ollama",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
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
                    params, context = self._resolve_model_metadata(m, "lm_studio")
                    if params >= self.min_params or params == 0:
                        models.append({
                            "id": name,
                            "provider": "lm_studio",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
        return []

    async def fetch_github_models(self) -> List[Dict[str, Any]]:
        """Fetches models from GitHub Models (Azure Inference)."""
        api_key = self.api_keys.get("github")
        if not api_key:
            return []
        url = self.base_urls.get("github", GITHUB_MODELS_URL)
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json()
                models = []
                for m in data:
                    name = m.get("name")
                    params, context = self._resolve_model_metadata(m, "github")
                    if params >= self.min_params:
                        models.append({
                            "id": name,
                            "provider": "github",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
        return []

    async def fetch_huggingface_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Hugging Face Inference API."""
        api_key = self.api_keys.get("huggingface")
        if not api_key:
            return []
        return [
            {"id": "meta-llama/Llama-3.1-405B-Instruct", "provider": "huggingface",
             "parameters": 405, "context_length": 131072},
            {"id": "meta-llama/Llama-3.1-70B-Instruct", "provider": "huggingface",
             "parameters": 70, "context_length": 131072},
        ]

    async def fetch_nvidia_models(self) -> List[Dict[str, Any]]:
        """Fetches models from NVIDIA NIM."""
        api_key = self.api_keys.get("nvidia")
        if not api_key:
            return []
        url = self.base_urls.get("nvidia", NVIDIA_MODELS_URL)
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    name = m.get("id")
                    params, context = self._resolve_model_metadata(m, "nvidia")
                    if params >= self.min_params:
                        models.append({
                            "id": name,
                            "provider": "nvidia",
                            "parameters": params,
                            "context_length": context,
                        })
                return models
        except:
            pass
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

        if "github" not in blacklisted_providers:
            github_models = await self.fetch_github_models()
            candidates.extend(github_models)
            database.update_provider_cycle("github", len(github_models) > 0)

        if "huggingface" not in blacklisted_providers:
            hf_models = await self.fetch_huggingface_models()
            candidates.extend(hf_models)
            database.update_provider_cycle("huggingface", len(hf_models) > 0)

        if "nvidia" not in blacklisted_providers:
            nvidia_models = await self.fetch_nvidia_models()
            candidates.extend(nvidia_models)
            database.update_provider_cycle("nvidia", len(nvidia_models) > 0)

        # 2. Filter using database skip/failure status
        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT model_id, manually_skipped, skip_expiry, failure_count, retry_after, is_blacklisted, last_success, avg_latency FROM model_history")
        db_status = {row[0]: row[1:] for row in cursor.fetchall()}
        conn.close()

        valid_candidates = []
        now = database.datetime.datetime.now()

        for m in candidates:
            # Global keyword exclusion check (configurable from settings)
            if any(exc in m['id'].lower() for exc in GLOBAL_EXCLUSIONS):
                continue

            status = db_status.get(m['id'])
            if status:
                skipped, skip_expiry, failures, retry_after, blacklisted = status[:5]
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

        # 2b. Force-in known good models that the provider fetches may have missed
        # (e.g. models with no parameter count in their ID, or provider API gaps)
        known_to_force = []
        for litellm_id, spec in known_models.all_models().items():
            # Skip if already in candidates
            if any(c['id'] == litellm_id or c['id'] == litellm_id.split('/', 1)[-1] for c in valid_candidates):
                continue
            # Skip if blacklisted in DB
            db_stat = db_status.get(litellm_id) or db_status.get(litellm_id.split('/', 1)[-1])
            if db_stat and db_stat[4]:  # is_blacklisted
                continue
            # Only force models from providers we have API keys for
            prov = spec.get("provider", "")
            if prov and prov not in self.api_keys:
                continue
            known_to_force.append({
                "id": litellm_id,
                "provider": prov,
                "parameters": spec["params"],
                "context_length": spec["ctx"],
                "score": 0,
                "latency": 0,
            })

        if known_to_force:
            print(f"Force-including {len(known_to_force)} known good models not found in provider fetches.")
            valid_candidates.extend(known_to_force)

        # 3. Benchmark in parallel (limited concurrency)
        tasks = []
        benchmarking_models = []
        cached_results = []

        for m in valid_candidates:
            is_local = m['provider'] in ['ollama', 'lm_studio']
            status = db_status.get(m['id'])

            # Smart Cache: If local and benchmarked in the last 15 minutes, reuse latency
            if is_local and status:
                last_success, avg_latency = status[5], status[6]
                if last_success and avg_latency and avg_latency > 0:
                    last_success_dt = database.datetime.datetime.fromisoformat(last_success) if isinstance(last_success, str) else last_success
                    if now - last_success_dt < database.datetime.timedelta(minutes=15):
                        cached_results.append((m, avg_latency))
                        continue

            tasks.append(self.measure_latency(m['id'], m['provider']))
            benchmarking_models.append(m)

        latencies = await asyncio.gather(*tasks)

        ranked_list = []

        # Add benchmarking results
        for m, latency in zip(benchmarking_models, latencies):
            if latency is not None:
                score = self.calculate_score(m['parameters'], latency, m.get('context_length', 4096))
                m['latency'] = latency
                m['score'] = score
                ranked_list.append(m)
                database.record_probe(
                    m['id'], m['provider'], latency, success=True,
                    score=m.get('score', 0),
                    context_length=m.get('context_length', 0),
                    parameters=m.get('parameters', 0),
                )

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
        """Check if the internet is accessible by hitting reliable endpoints."""
        endpoints = [
            "https://api.github.com/zen",
            "https://httpbin.org/status/200",
            "https://openrouter.ai/api/v1/models",
        ]
        for url in endpoints:
            try:
                response = await self.client.get(url, timeout=10.0)
                if response.status_code < 500:
                    return True
            except Exception:
                continue
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
            url = (base or "http://localhost:11434") + "/v1/chat/completions"
        elif provider == "lm_studio":
            url = (base or "http://localhost:1234") + "/v1/chat/completions"
        elif provider == "github":
            url = (base or "https://models.inference.ai.azure.com") + "/chat/completions"
        elif provider == "huggingface":
            url = f"https://api-inference.huggingface.co/models/{model_id}/v1/chat/completions"
        elif provider == "nvidia":
            url = (base or "https://integrate.api.nvidia.com/v1") + "/chat/completions"
        else:
            return None

        api_key = self.api_keys.get(provider)

        headers = {}
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"
        headers["Content-Type"] = "application/json"

        payload = {
            "model": model_id,
            "messages": [{"role": "user", "content": "hi"}],
            "max_tokens": 1,
            "stream": True
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
                    database.record_probe(model_id, provider, None, success=False,
                                         error_code=response.status_code)
                    return None
                else:
                    err_body = await response.aread()
                    print(f"Error {response.status_code} for {model_id}: {err_body}")
                    database.record_probe(model_id, provider, None, success=False,
                                         error_code=response.status_code)
                    return None
        except Exception as e:
            print(f"Exception measuring latency for {model_id}: {e}")
            database.record_probe(model_id, provider, None, success=False,
                                 error_message=str(e)[:200])
            return None
        return None

    async def close(self):
        await self.client.aclose()


async def main():
    engine = ModelEngine(api_keys={})
    print("Fetching models...")
    models = await engine.fetch_openrouter_models()
    print(f"Found {len(models)} free models >= 100B:")
    for m in models:
        print(f" - {m['id']} ({m['parameters']}B, {m.get('context_length', 0)} ctx)")
    await engine.close()


if __name__ == "__main__":
    asyncio.run(main())
