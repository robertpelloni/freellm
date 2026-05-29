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
MISTRAL_MODELS_URL = "https://api.mistral.ai/v1/models"
CODESTRAL_MODELS_URL = "https://codestral.mistral.ai/v1/models"
COHERE_MODELS_URL = "https://api.cohere.ai/v1/models"
SAMBANOVA_MODELS_URL = "https://api.sambanova.ai/v1/models"
FIREWORKS_MODELS_URL = "https://api.fireworks.ai/inference/v1/models"
HYPERBOLIC_MODELS_URL = "https://api.hyperbolic.xyz/v1/models"
NEBIUS_MODELS_URL = "https://api.studio.nebius.ai/v1/models"

MIN_PARAMETERS_BILLIONS = 0  # Changed from 100 -- known_models fills in real values

# These are defaults, will be overridden by settings
SIZE_WEIGHT = 0.6
CONTEXT_WEIGHT = 0.2
LATENCY_WEIGHT = 0.2

# Regex to extract parameter size (e.g., 405b, 70B, 120b-instruct)
SIZE_PATTERN = re.compile(r'(\d+)[bB]')

# Global exclusions -- set by main.py from settings at runtime.
# Default is deliberately minimal; -preview is NOT excluded by default.
GLOBAL_EXCLUSIONS = ["-base", "dummy"]

# Models known to be decommissioned, nonexistent, or non-chat -- skip entirely
DEAD_MODELS = {
    # Groq decommissioned
    "groq/llama-3.1-70b-versatile", "groq/mixtral-8x7b-32768",
    # Cerebras old IDs
    "cerebras/llama3.1-70b", "cerebras/llama3.1-8b",
    # OpenRouter gone/free-tier-removed
    "openrouter/google/gemini-2.0-flash-exp:free",
    "openrouter/google/gemini-2.0-flash-thinking-exp:free",
    "openrouter/google/learnlm-1.5-pro-experimental:free",
    "openrouter/mistralai/mistral-7b-instruct:free",
    "openrouter/huggingfaceh4/zephyr-7b-beta:free",
    "openrouter/openchat/openchat-7b:free",
    "openrouter/qwen/qwen-2-7b-instruct:free",
    "openrouter/microsoft/phi-3-medium-128k-instruct:free",
    "openrouter/meta-llama/llama-3-8b-instruct:free",
    "openrouter/owl-alpha",
    # NVIDIA NIM: listed in API but not actually deployable (tested 2025-06)
    "01-ai/yi-large", "adept/fuyu-8b", "ai21labs/jamba-1.5-large-instruct",
    "aisingapore/sea-lion-7b-instruct", "databricks/dbrx-instruct",
    "deepseek-ai/deepseek-coder-6.7b-instruct", "deepseek-ai/deepseek-v4-flash",
    "deepseek-ai/deepseek-v4-pro", "google/gemma-2b", "google/gemma-3-12b-it",
    "google/gemma-3-4b-it", "google/gemma-4-31b-it", "google/recurrentgemma-2b",
    "ibm/granite-3.0-3b-a800m-instruct", "ibm/granite-3.0-8b-instruct",
    "ibm/granite-34b-code-instruct", "ibm/granite-8b-code-instruct",
    "meta/codellama-70b", "meta/llama-3.1-70b-instruct",
    "meta/llama-4-maverick-17b-128e-instruct", "meta/llama2-70b",
    "microsoft/phi-3.5-moe-instruct", "microsoft/phi-4-mini-instruct",
    "microsoft/phi-4-multimodal-instruct", "minimaxai/minimax-m2.7",
    "mistralai/codestral-22b-instruct-v0.1", "mistralai/mistral-7b-instruct-v0.3",
    "mistralai/mistral-large", "mistralai/mistral-large-2-instruct",
    "mistralai/mistral-medium-3.5-128b", "mistralai/mistral-nemotron",
    "mistralai/mixtral-8x22b-v0.1", "nv-mistralai/mistral-nemo-12b-instruct",
    "nvidia/cosmos-reason2-8b", "nvidia/llama-3.1-nemotron-51b-instruct",
    "nvidia/llama-3.1-nemotron-70b-instruct", "nvidia/llama-3.1-nemotron-ultra-253b-v1",
    "nvidia/llama3-chatqa-1.5-70b", "nvidia/mistral-nemo-minitron-8b-8k-instruct",
    "nvidia/nemotron-4-340b-instruct", "nvidia/nemotron-nano-3-30b-a3b",
    "nvidia/vila", "qwen/qwen3-coder-480b-a35b-instruct",
    "qwen/qwen3.5-397b-a17b", "writer/palmyra-creative-122b",
    "writer/palmyra-fin-70b-32k", "writer/palmyra-med-70b",
    "writer/palmyra-med-70b-32k", "z-ai/glm-5.1", "zyphra/zamba2-7b-instruct",
    "deepseek-ai/deepseek-r1", "deepseek-ai/deepseek-v3",
    "meta/llama-3.1-405b-instruct",
}


class ModelEngine:
    def __init__(self, api_keys: Dict[str, str], min_params: int = MIN_PARAMETERS_BILLIONS,
                 weights: Dict[str, float] = None, base_urls: Dict[str, str] = None):
        self.api_keys = api_keys
        self.min_params = min_params
        self.weights = weights or {"size": 0.6, "context": 0.2, "latency": 0.2}
        self.base_urls = base_urls or {}
        self.client = httpx.AsyncClient(timeout=10.0)
        self.log_listeners = []

    def add_log_listener(self, listener):
        """Add a callback for log messages. listener(msg: str)"""
        self.log_listeners.append(listener)

    def _log(self, message: str):
        """Log a message and notify listeners."""
        print(message)
        for listener in self.log_listeners:
            try:
                listener(message)
            except:
                pass

    async def fetch_openrouter_models(self) -> List[Dict[str, Any]]:
        """Fetches free models from OpenRouter."""
        url = self.base_urls.get("openrouter", OPENROUTER_MODELS_URL.replace("/models", "")) + "/models"
        try:
            response = await self.client.get(url)
            if response.status_code != 200:
                self._log(f"Error fetching OpenRouter models: {response.status_code}")
                return []
            data = response.json()
            all_models = data.get("data", [])
            free_models = []
            for m in all_models:
                pricing = m.get("pricing", {})
                model_id = m.get("id", "")

                is_free = (float(pricing.get("prompt", 0)) == 0 and float(pricing.get("completion", 0)) == 0)
                if not is_free and ":free" in model_id:
                    is_free = True

                if is_free:
                    params, context = self._resolve_model_metadata(m, "openrouter")
                    if params >= self.min_params or params == 0:
                        free_models.append({
                            "id": model_id,
                            "provider": "openrouter",
                            "parameters": params,
                            "context_length": context,
                        })
            return free_models
        except Exception as e:
            self._log(f"Exception fetching OpenRouter models: {e}")
            return []

    def _resolve_model_metadata(self, model_data: Dict[str, Any], provider: str) -> tuple:
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
        # Normalize size (cap at 405B for scaling)
        size_score = (min(params, 405) / 100.0) * self.weights.get("size", 0.6)

        # Normalize context (cap at 128k for scaling)
        context_score = (min(context_length, 128000) / 128000.0) * self.weights.get("context", 0.2)

        # Latency penalty (capped to prevent it from overwhelming size)
        # 5 seconds is considered a "very slow" but usable model
        latency_penalty = min(latency, 5.0) * self.weights.get("latency", 0.2)

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
                    if params >= self.min_params or params == 0:
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
                    if params >= self.min_params or params == 0:
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
                    if params >= self.min_params or params == 0:
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
                    if params >= self.min_params or params == 0:
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
        """Fetches chat-completion models from GitHub Models (Azure Inference)."""
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
                    # Only include chat-completion models
                    task = m.get("task", "")
                    if task and "chat" not in task.lower():
                        continue
                    name = m.get("name")
                    params, context = self._resolve_model_metadata(m, "github")
                    if params >= self.min_params or params == 0:
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

    async def fetch_gemini_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Google AI Studio (Gemini)."""
        api_key = self.api_keys.get("gemini")
        if not api_key:
            return []
        # Google AI Studio offers these main free tier models:
        gemini_defaults = [
            {"id": "gemini-2.5-pro", "provider": "gemini", "parameters": 0, "context_length": 2000000},
            {"id": "gemini-2.5-flash", "provider": "gemini", "parameters": 0, "context_length": 1000000},
            {"id": "gemini-2.0-flash", "provider": "gemini", "parameters": 0, "context_length": 1000000},
            {"id": "gemini-2.0-flash-lite-preview-02-05", "provider": "gemini", "parameters": 0, "context_length": 1000000},
            {"id": "gemini-2.0-pro-exp-02-05", "provider": "gemini", "parameters": 0, "context_length": 2000000},
            {"id": "gemini-1.5-pro", "provider": "gemini", "parameters": 0, "context_length": 2000000},
            {"id": "gemini-1.5-flash", "provider": "gemini", "parameters": 0, "context_length": 1000000},
            {"id": "gemini-1.5-flash-8b", "provider": "gemini", "parameters": 8, "context_length": 1000000},
        ]

        models = []
        for m in gemini_defaults:
            # Re-resolve in case known_models has more precise info
            params, context = self._resolve_model_metadata(m, "gemini")
            if params >= self.min_params or params == 0:
                m["parameters"] = params
                m["context_length"] = context
                models.append(m)
        return models

    async def fetch_huggingface_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Hugging Face Inference API."""
        api_key = self.api_keys.get("huggingface")
        if not api_key:
            return []

        url = "https://huggingface.co/api/models?filter=text-generation&sort=trendingScore&limit=100"
        try:
            response = await self.client.get(url, timeout=10.0)
            if response.status_code == 200:
                data = response.json()
                models = []
                for m in data:
                    model_id = m.get("id")
                    params, context = self._resolve_model_metadata(m, "huggingface")
                    if params >= self.min_params or params == 0:
                        models.append({
                            "id": model_id,
                            "provider": "huggingface",
                            "parameters": params,
                            "context_length": context,
                        })
                if models:
                    return models
        except Exception as e:
            self._log(f"Exception fetching Hugging Face models: {e}")

        return [
            {"id": "meta-llama/Llama-3.1-405B-Instruct", "provider": "huggingface", "parameters": 405, "context_length": 131072},
            {"id": "meta-llama/Llama-3.1-70B-Instruct", "provider": "huggingface", "parameters": 70, "context_length": 131072},
        ]

    async def fetch_nvidia_models(self) -> List[Dict[str, Any]]:
        """Fetches chat-completion models from NVIDIA NIM."""
        api_key = self.api_keys.get("nvidia")
        if not api_key:
            return []
        url = self.base_urls.get("nvidia", NVIDIA_MODELS_URL)
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                skip_kw = ["embed", "safety", "guard", "reward", "parse",
                           "detect", "qa-4", "vl-", "vision", "clip",
                           "codegemma", "deplot", "starcoder", "kosmos",
                           "bge-m3", "gliner", "neva", "riva", "nvclip",
                           "retriever", "content-safety", "pii"]
                for m in data:
                    name = m.get("id", "")
                    # Skip non-chat models
                    if any(kw in name.lower() for kw in skip_kw):
                        continue
                    params, context = self._resolve_model_metadata(m, "nvidia")
                    if params >= self.min_params or params == 0:
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

    async def fetch_mistral_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Mistral La Plateforme."""
        api_key = self.api_keys.get("mistral")
        if not api_key:
            return []
        url = self.base_urls.get("mistral", "https://api.mistral.ai/v1") + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    mid = m.get("id", "")
                    params, context = self._resolve_model_metadata(m, "mistral")
                    if params >= self.min_params or params == 0:
                        models.append({"id": mid, "provider": "mistral", "parameters": params, "context_length": context})
                return models
        except:
            pass
        return []

    async def fetch_codestral_models(self) -> List[Dict[str, Any]]:
        """Fetches Codestral from Mistral Codestral endpoint."""
        api_key = self.api_keys.get("codestral")
        if not api_key:
            return []
        return [{"id": "codestral-latest", "provider": "codestral", "parameters": 22, "context_length": 256000}]

    async def fetch_cohere_models(self) -> List[Dict[str, Any]]:
        """Fetches free models from Cohere."""
        api_key = self.api_keys.get("cohere")
        if not api_key:
            return []
        cohere_models = [
            {"id": "command-a-03-2025", "parameters": 111, "context_length": 256000},
            {"id": "command-a-plus-05-2026", "parameters": 111, "context_length": 256000},
            {"id": "command-a-reasoning-08-2025", "parameters": 111, "context_length": 256000},
            {"id": "command-r-plus-08-2024", "parameters": 104, "context_length": 128000},
            {"id": "c4ai-aya-expanse-32b", "parameters": 32, "context_length": 128000},
        ]
        models = []
        for m in cohere_models:
            params, context = self._resolve_model_metadata(m, "cohere")
            if params >= self.min_params or params == 0:
                models.append({"id": m["id"], "provider": "cohere", "parameters": params, "context_length": context})
        return models

    async def fetch_sambanova_models(self) -> List[Dict[str, Any]]:
        """Fetches models from SambaNova Cloud."""
        api_key = self.api_keys.get("sambanova")
        if not api_key:
            return []
        url = self.base_urls.get("sambanova", "https://api.sambanova.ai/v1") + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    mid = m.get("id", "")
                    params, context = self._resolve_model_metadata(m, "sambanova")
                    if params >= self.min_params or params == 0:
                        models.append({"id": mid, "provider": "sambanova", "parameters": params, "context_length": context})
                return models
        except:
            pass
        return []

    async def fetch_fireworks_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Fireworks AI."""
        api_key = self.api_keys.get("fireworks")
        if not api_key:
            return []
        url = self.base_urls.get("fireworks", "https://api.fireworks.ai/inference/v1") + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    mid = m.get("id", "")
                    if any(kw in mid.lower() for kw in ["embed", "vision", "image", "whisper", "tts"]):
                        continue
                    params, context = self._resolve_model_metadata(m, "fireworks")
                    if params >= self.min_params or params == 0:
                        models.append({"id": mid, "provider": "fireworks", "parameters": params, "context_length": context})
                return models
        except:
            pass
        return []

    async def fetch_hyperbolic_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Hyperbolic."""
        api_key = self.api_keys.get("hyperbolic")
        if not api_key:
            return []
        url = self.base_urls.get("hyperbolic", "https://api.hyperbolic.xyz/v1") + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    mid = m.get("id", "")
                    params, context = self._resolve_model_metadata(m, "hyperbolic")
                    if params >= self.min_params or params == 0:
                        models.append({"id": mid, "provider": "hyperbolic", "parameters": params, "context_length": context})
                return models
        except:
            pass
        return []

    async def fetch_nebius_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Nebius."""
        api_key = self.api_keys.get("nebius")
        if not api_key:
            return []
        url = self.base_urls.get("nebius", "https://api.studio.nebius.ai/v1") + "/models"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json().get("data", [])
                models = []
                for m in data:
                    mid = m.get("id", "")
                    params, context = self._resolve_model_metadata(m, "nebius")
                    if params >= self.min_params or params == 0:
                        models.append({"id": mid, "provider": "nebius", "parameters": params, "context_length": context})
                return models
        except:
            pass
        return []

    async def fetch_cloudflare_models(self) -> List[Dict[str, Any]]:
        """Fetches models from Cloudflare Workers AI."""
        api_key = self.api_keys.get("cloudflare")
        account_id = self.api_keys.get("cloudflare_account_id", "")
        if not api_key or not account_id:
            return []
        url = f"https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/models/search"
        try:
            response = await self.client.get(url, headers={"Authorization": f"Bearer {api_key}"})
            if response.status_code == 200:
                data = response.json()
                cf_models = data.get("result", []) if isinstance(data, dict) else []
                models = []
                for m in cf_models:
                    mid = m.get("id", m.get("name", ""))
                    if not mid:
                        continue
                    task = m.get("task", "")
                    if task and "text-generation" not in task and "chat" not in task:
                        continue
                    params, context = self._resolve_model_metadata(m, "cloudflare")
                    if params >= self.min_params or params == 0:
                        models.append({"id": mid, "provider": "cloudflare", "parameters": params, "context_length": context})
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

        if "gemini" not in blacklisted_providers:
            gemini_models = await self.fetch_gemini_models()
            candidates.extend(gemini_models)
            database.update_provider_cycle("gemini", len(gemini_models) > 0)

        if "huggingface" not in blacklisted_providers:
            hf_models = await self.fetch_huggingface_models()
            candidates.extend(hf_models)
            database.update_provider_cycle("huggingface", len(hf_models) > 0)

        if "nvidia" not in blacklisted_providers:
            nvidia_models = await self.fetch_nvidia_models()
            candidates.extend(nvidia_models)
            database.update_provider_cycle("nvidia", len(nvidia_models) > 0)

        if "mistral" not in blacklisted_providers:
            mistral_models = await self.fetch_mistral_models()
            self._log(f"  mistral: fetched {len(mistral_models)} models")
            candidates.extend(mistral_models)
            database.update_provider_cycle("mistral", len(mistral_models) > 0)

        if "codestral" not in blacklisted_providers:
            codestral_models = await self.fetch_codestral_models()
            self._log(f"  codestral: fetched {len(codestral_models)} models")
            candidates.extend(codestral_models)
            database.update_provider_cycle("codestral", len(codestral_models) > 0)

        if "cohere" not in blacklisted_providers:
            cohere_models = await self.fetch_cohere_models()
            self._log(f"  cohere: fetched {len(cohere_models)} models")
            candidates.extend(cohere_models)
            database.update_provider_cycle("cohere", len(cohere_models) > 0)

        if "sambanova" not in blacklisted_providers:
            sambanova_models = await self.fetch_sambanova_models()
            self._log(f"  sambanova: fetched {len(sambanova_models)} models")
            candidates.extend(sambanova_models)
            database.update_provider_cycle("sambanova", len(sambanova_models) > 0)

        if "fireworks" not in blacklisted_providers:
            fireworks_models = await self.fetch_fireworks_models()
            self._log(f"  fireworks: fetched {len(fireworks_models)} models")
            candidates.extend(fireworks_models)
            database.update_provider_cycle("fireworks", len(fireworks_models) > 0)

        if "hyperbolic" not in blacklisted_providers:
            hyperbolic_models = await self.fetch_hyperbolic_models()
            self._log(f"  hyperbolic: fetched {len(hyperbolic_models)} models")
            candidates.extend(hyperbolic_models)
            database.update_provider_cycle("hyperbolic", len(hyperbolic_models) > 0)

        if "nebius" not in blacklisted_providers:
            nebius_models = await self.fetch_nebius_models()
            self._log(f"  nebius: fetched {len(nebius_models)} models")
            candidates.extend(nebius_models)
            database.update_provider_cycle("nebius", len(nebius_models) > 0)

        if "cloudflare" not in blacklisted_providers:
            cloudflare_models = await self.fetch_cloudflare_models()
            self._log(f"  cloudflare: fetched {len(cloudflare_models)} models")
            candidates.extend(cloudflare_models)
            database.update_provider_cycle("cloudflare", len(cloudflare_models) > 0)

        # De-duplicate candidates by (id, provider)
        seen = set()
        deduped = []
        for m in candidates:
            key = (m['id'], m['provider'])
            if key not in seen:
                seen.add(key)
                deduped.append(m)
        candidates = deduped

        self._log(f"Fetched {len(candidates)} unique candidates from all providers.")

        # 2. Filter using database skip/failure status
        conn = database.sqlite3.connect(database.DB_NAME)
        cursor = conn.cursor()
        cursor.execute("SELECT model_id, manually_skipped, skip_expiry, failure_count, retry_after, is_blacklisted, last_success, avg_latency FROM model_history")
        db_status = {row[0]: row[1:] for row in cursor.fetchall()}
        conn.close()

        valid_candidates = []
        now = database.datetime.datetime.now()

        for m in candidates:
            # Skip known-dead models (decommissioned, nonexistent)
            full_id = f"{m['provider']}/{m['id']}"
            if full_id in DEAD_MODELS or m['id'] in DEAD_MODELS:
                continue

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

        # Track which providers returned results (so we don't force-in extras from those)
        providers_with_results = set()
        for c in valid_candidates:
            providers_with_results.add(c['provider'])

        # 2b. Force-in known good models that the provider fetches may have missed
        # (e.g. models with no parameter count in their ID, or provider API gaps)
        known_to_force = []
        existing_ids = {c['id'] for c in valid_candidates}

        for litellm_id, spec in known_models.all_models().items():
            # The ID stored in known_models includes provider prefix, e.g. "github/DeepSeek-R1"
            # But our candidate list uses the bare model ID, e.g. "DeepSeek-R1"
            bare_id = litellm_id.split("/", 1)[-1] if "/" in litellm_id else litellm_id
            prov = spec.get("provider", "")

            # Skip if already in candidates (check both bare id and full id)
            if bare_id in existing_ids or litellm_id in existing_ids:
                continue

            # Skip if blacklisted in DB
            db_stat = db_status.get(bare_id) or db_status.get(litellm_id)
            if db_stat and db_stat[4]:  # is_blacklisted
                continue

            # Only force models from providers we have API keys for
            # (nvidia_nim and nvidia share the same key)
            effective_prov = prov
            if prov == "nvidia_nim" and "nvidia_nim" not in self.api_keys:
                effective_prov = "nvidia"
            if effective_prov and effective_prov not in self.api_keys:
                continue

            # If we already fetched from this provider and they didn't list it,
            # skip force-in (the provider API is authoritative for its own models)
            # Exception: huggingface/ollama/gemini have incomplete APIs
            if prov in providers_with_results and prov not in ["huggingface", "ollama", "gemini"]:
                continue

            known_to_force.append({
                "id": bare_id,
                "provider": prov,
                "parameters": spec["params"],
                "context_length": spec["ctx"],
                "score": 0,
                "latency": 0,
            })
            existing_ids.add(bare_id)  # Prevent duplicate force-in

        if known_to_force:
            self._log(f"Force-including {len(known_to_force)} known good models not found in provider fetches.")
            valid_candidates.extend(known_to_force)

        # 3. Benchmark in parallel with concurrency limit
        semaphore = asyncio.Semaphore(5)  # Max 5 parallel benchmarks
        tasks = []
        benchmarking_models = []
        cached_results = []

        async def sem_measure(m_id, prov):
            async with semaphore:
                await asyncio.sleep(0.5)
                return await self.measure_latency(m_id, prov)

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

            tasks.append(sem_measure(m['id'], m['provider']))
            benchmarking_models.append(m)

        self._log(f"Benchmarking {len(tasks)} models ({len(cached_results)} cached)...")
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
                    m['id'], m['provider'], latency,
                    success=True, score=m.get('score', 0),
                    context_length=m.get('context_length', 0),
                    parameters=m.get('parameters', 0),
                )
            else:
                # Include failed models with a penalty so they appear at the bottom but stay in config
                m['latency'] = 99.0
                m['score'] = -10.0
                ranked_list.append(m)

        # Add cached results
        for m, latency in cached_results:
            score = self.calculate_score(m['parameters'], latency, m.get('context_length', 4096))
            m['latency'] = latency
            m['score'] = score
            ranked_list.append(m)

        # 4. Sort by score
        ranked_list.sort(key=lambda x: x['score'], reverse=True)

        self._log(f"Ranked {len(ranked_list)} models. Top 5:")
        for m in ranked_list[:5]:
            self._log(f"  {m['provider']}/{m['id']} score={m['score']:.1f} latency={m['latency']:.2f}s params={m.get('parameters',0)}B")

        return ranked_list

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

        # Clean model ID for native provider benchmarking
        bench_id = model_id
        if provider == "openrouter":
            if model_id.startswith("openrouter/"):
                bench_id = model_id[len("openrouter/"):]
        elif provider == "github":
            bench_id = model_id.split("/")[-1]
        elif provider in ["nvidia", "nvidia_nim"]:
            # NVIDIA NIM API returns model IDs in their native format
            # (e.g. "meta/llama-3.3-70b-instruct", "nvidia/nemotron-3-super-120b-a12b")
            # Do NOT strip the nvidia_ prefix -- use the ID as-is
            if model_id.startswith("nvidia_nim/"):
                bench_id = model_id[len("nvidia_nim/"):]
            else:
                bench_id = model_id  # Use the full model ID as returned by the API
        elif provider == "huggingface":
            if model_id.startswith("huggingface/"):
                bench_id = model_id[len("huggingface/"):]
        elif provider in ["groq", "together", "deepinfra", "cerebras", "ollama", "lm_studio",
                         "sambanova", "fireworks", "hyperbolic", "nebius"]:
            if "/" in model_id:
                bench_id = model_id.split("/")[-1]
        elif provider == "cloudflare":
            # Cloudflare uses @cf/ prefix or bare names
            if model_id.startswith("@cf/"):
                bench_id = model_id  # Keep @cf/ prefix for CF API
        elif provider in ["mistral", "codestral"]:
            pass  # Use model_id as-is

        api_key = self.api_keys.get(provider) or self.api_keys.get("nvidia") if provider in ["nvidia", "nvidia_nim"] else self.api_keys.get(provider)

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
            url = f"https://api-inference.huggingface.co/models/{bench_id}/v1/chat/completions"
        elif provider == "gemini":
            url = (base or "https://generativelanguage.googleapis.com/v1beta") + f"/models/{bench_id}:streamGenerateContent?key={api_key}"
        elif provider in ["nvidia", "nvidia_nim"]:
            url = (base or "https://integrate.api.nvidia.com/v1") + "/chat/completions"
        elif provider == "mistral":
            url = (base or "https://api.mistral.ai/v1") + "/chat/completions"
        elif provider == "codestral":
            url = (base or "https://codestral.mistral.ai/v1") + "/chat/completions"
        elif provider == "cohere":
            url = (base or "https://api.cohere.ai/v1") + "/chat/completions"
        elif provider == "sambanova":
            url = (base or "https://api.sambanova.ai/v1") + "/chat/completions"
        elif provider == "fireworks":
            url = (base or "https://api.fireworks.ai/inference/v1") + "/chat/completions"
        elif provider == "hyperbolic":
            url = (base or "https://api.hyperbolic.xyz/v1") + "/chat/completions"
        elif provider == "nebius":
            url = (base or "https://api.studio.nebius.ai/v1") + "/chat/completions"
        elif provider == "cloudflare":
            account_id = self.api_keys.get("cloudflare_account_id", "")
            if not account_id:
                return None
            url = f"https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1/chat/completions"
        else:
            return None

        headers = {}
        if provider != "gemini" and api_key:
            headers["Authorization"] = f"Bearer {api_key}"
            headers["Content-Type"] = "application/json"

        if provider == "gemini":
            payload = {
                "contents": [{"parts": [{"text": "hi"}]}]
            }
        else:
            payload = {
                "model": bench_id,
                "messages": [{"role": "user", "content": "hi"}],
                "max_tokens": 1,
                "stream": True
            }

        try:
            start_time = time.perf_counter()
            async with self.client.stream("POST", url, headers=headers, json=payload, timeout=30.0) as response:
                if response.status_code == 200:
                    async for line in response.aiter_lines():
                        if line:
                            ttft = time.perf_counter() - start_time
                            return ttft
                elif response.status_code in [429, 504]:
                    self._log(f"Rate limited or timeout for {model_id} ({response.status_code})")
                    database.record_probe(model_id, provider, None, success=False, error_code=response.status_code)
                    return None
                else:
                    err_body = await response.aread()
                    self._log(f"Error {response.status_code} for {model_id}: {err_body}")
                    database.record_probe(model_id, provider, None, success=False, error_code=response.status_code)
                    return None
        except Exception as e:
            self._log(f"Exception measuring latency for {model_id}: {e}")
            database.record_probe(model_id, provider, None, success=False, error_message=str(e)[:200])
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
