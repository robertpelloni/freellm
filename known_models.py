"""
Known Good Models — authoritative specs for models that don't self-report.

Providers frequently omit parameter counts and context window sizes from their
API metadata. This file is the single source of truth for model specifications
that the Control Panel uses to ensure these models are always ranked correctly.

Update this dictionary as new frontier/free models are discovered.
"""

# key: litellm_model string (as it appears in config/API calls)
# values: params (billions), ctx (tokens), provider
KNOWN_MODELS = {
    # ── GitHub Models (Azure Inference) ──────────────────────────────────
    "github/gpt-4o-mini":                            {"params": 8,    "ctx": 128000,  "provider": "github"},
    "github/gpt-4o":                                 {"params": 175,  "ctx": 128000,  "provider": "github"},
    "github/o1-mini":                                {"params": 30,   "ctx": 128000,  "provider": "github"},
    "github/o1":                                     {"params": 200,  "ctx": 128000,  "provider": "github"},
    "github/Llama-3.3-70B-Instruct":                 {"params": 70,   "ctx": 128000,  "provider": "github"},
    "github/Llama-3.1-405B-Instruct":                {"params": 405,  "ctx": 128000,  "provider": "github"},
    "github/Llama-3.1-70B-Instruct":                 {"params": 70,   "ctx": 128000,  "provider": "github"},
    "github/Mistral-Large-2411":                     {"params": 123,  "ctx": 128000,  "provider": "github"},
    "github/Phi-4":                                  {"params": 14,   "ctx": 16384,   "provider": "github"},
    "github/DeepSeek-R1":                            {"params": 671,  "ctx": 65536,   "provider": "github"},
    "github/DeepSeek-V3":                            {"params": 671,  "ctx": 65536,   "provider": "github"},

    # ── OpenRouter Free Models ─────────────────────────────────────────────
    "openrouter/google/gemini-2.0-flash-exp:free":       {"params": 10,  "ctx": 1048576, "provider": "openrouter"},
    "openrouter/google/gemini-2.0-flash-thinking-exp:free":{"params": 10, "ctx": 1048576, "provider": "openrouter"},
    "openrouter/google/learnlm-1.5-pro-experimental:free": {"params": 10, "ctx": 1048576, "provider": "openrouter"},
    "openrouter/mistralai/mistral-7b-instruct:free":     {"params": 7,   "ctx": 32768,   "provider": "openrouter"},
    "openrouter/huggingfaceh4/zephyr-7b-beta:free":      {"params": 7,   "ctx": 4096,    "provider": "openrouter"},
    "openrouter/openchat/openchat-7b:free":              {"params": 7,   "ctx": 8192,    "provider": "openrouter"},
    "openrouter/qwen/qwen-2-7b-instruct:free":           {"params": 7,   "ctx": 32768,   "provider": "openrouter"},
    "openrouter/microsoft/phi-3-medium-128k-instruct:free":{"params": 14, "ctx": 128000,  "provider": "openrouter"},
    "openrouter/meta-llama/llama-3-8b-instruct:free":    {"params": 8,   "ctx": 8192,    "provider": "openrouter"},

    # ── NVIDIA NIM Models ──────────────────────────────────────────────────
    "nvidia_nim/meta/llama-3.1-405b-instruct":           {"params": 405, "ctx": 128000,  "provider": "nvidia_nim"},
    "nvidia_nim/meta/llama-3.1-70b-instruct":            {"params": 70,  "ctx": 128000,  "provider": "nvidia_nim"},
    "nvidia_nim/meta/llama-3.3-70b-instruct":            {"params": 70,  "ctx": 128000,  "provider": "nvidia_nim"},
    "nvidia_nim/nvidia/llama-3.1-nemotron-70b-instruct": {"params": 70,  "ctx": 128000,  "provider": "nvidia_nim"},
    "nvidia_nim/deepseek-ai/deepseek-r1":                 {"params": 671, "ctx": 65536,   "provider": "nvidia_nim"},

    # ── Groq Models ────────────────────────────────────────────────────────
    "groq/llama-3.3-70b-versatile":                       {"params": 70,  "ctx": 128000,  "provider": "groq"},
    "groq/llama-3.1-70b-versatile":                       {"params": 70,  "ctx": 128000,  "provider": "groq"},
    "groq/llama-3.1-8b-instant":                          {"params": 8,   "ctx": 131072,  "provider": "groq"},
    "groq/mixtral-8x7b-32768":                            {"params": 47,  "ctx": 32768,   "provider": "groq"},

    # ── Cerebras Models ────────────────────────────────────────────────────
    "cerebras/llama3.1-70b":                              {"params": 70,  "ctx": 131072,  "provider": "cerebras"},
    "cerebras/llama3.1-8b":                               {"params": 8,   "ctx": 131072,  "provider": "cerebras"},

    # ── DeepInfra Models ───────────────────────────────────────────────────
    "deepinfra/meta-llama/Meta-Llama-3.1-405B-Instruct":  {"params": 405, "ctx": 131072,  "provider": "deepinfra"},
    "deepinfra/meta-llama/Meta-Llama-3.1-70B-Instruct":   {"params": 70,  "ctx": 131072,  "provider": "deepinfra"},
    "deepinfra/Qwen/Qwen2.5-72B-Instruct":               {"params": 72,  "ctx": 131072,  "provider": "deepinfra"},

    # ── HuggingFace Models ─────────────────────────────────────────────────
    "huggingface/meta-llama/Llama-3.1-405B-Instruct":     {"params": 405, "ctx": 131072,  "provider": "huggingface"},
    "huggingface/meta-llama/Llama-3.1-70B-Instruct":      {"params": 70,  "ctx": 131072,  "provider": "huggingface"},

    # ── Together Models ────────────────────────────────────────────────────
    "together/meta-llama/Meta-Llama-3.1-405B-Instruct-Turbo": {"params": 405, "ctx": 131072, "provider": "together"},
    "together/meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo":  {"params": 70,  "ctx": 131072, "provider": "together"},

    # ── Ollama / LM Studio (local) ────────────────────────────────────────
    "ollama/llama3.3:70b":                                {"params": 70,  "ctx": 128000,  "provider": "ollama"},
    "ollama/deepseek-r1:671b":                            {"params": 671, "ctx": 65536,   "provider": "ollama"},
    "ollama/qwen2.5:72b":                                 {"params": 72,  "ctx": 131072,  "provider": "ollama"},
}


def lookup(litellm_model: str) -> dict | None:
    """Look up a model by its full litellm model string.

    Tries: exact match, then stripping provider prefix, then tail match.
    Returns {"params": int, "ctx": int, "provider": str} or None.
    """
    # Exact match
    if litellm_model in KNOWN_MODELS:
        return KNOWN_MODELS[litellm_model]

    # Try stripping provider prefixes
    for prefix in ("openrouter/", "nvidia_nim/", "github/", "groq/",
                   "together/", "deepinfra/", "cerebras/", "huggingface/",
                   "ollama/", "openai/", "lm_studio/"):
        if litellm_model.startswith(prefix):
            stripped = litellm_model[len(prefix):]
            if stripped in KNOWN_MODELS:
                return KNOWN_MODELS[stripped]
            # Try the reverse: the stripped name might match a key that uses
            # a different prefix
            for known_id, info in KNOWN_MODELS.items():
                if known_id.endswith("/" + stripped) or known_id.endswith(stripped):
                    return info

    # Tail match: model_id ends with a known key
    for known_id, info in KNOWN_MODELS.items():
        known_tail = known_id.split("/")[-1] if "/" in known_id else known_id
        model_tail = litellm_model.split("/")[-1] if "/" in litellm_model else litellm_model
        if model_tail == known_tail:
            return info
        if litellm_model.endswith(known_tail):
            return info

    return None


def add_model(litellm_model: str, params: int, ctx: int, provider: str):
    """Add or update a model in the known models dict at runtime."""
    KNOWN_MODELS[litellm_model] = {"params": params, "ctx": ctx, "provider": provider}


def remove_model(litellm_model: str):
    """Remove a model from the known models dict at runtime."""
    KNOWN_MODELS.pop(litellm_model, None)


def all_models() -> dict:
    """Return the full known models dictionary."""
    return dict(KNOWN_MODELS)
