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
    # ── GitHub Models ──────────────────────────────────────────────────────
    "openai/gpt-4.1-mini":                           {"params": 1200, "ctx": 1048576, "provider": "github"},
    "openai/gpt-4.1":                                {"params": 1200, "ctx": 1048576, "provider": "github"},
    "openai/gpt-4o":                                 {"params": 1200, "ctx": 128000,  "provider": "github"},
    "openai/gpt-4o-mini":                            {"params": 1200, "ctx": 128000,  "provider": "github"},
    "openai/o3-mini":                                {"params": 1200, "ctx": 200000,  "provider": "github"},
    "openai/o4-mini":                                {"params": 1200, "ctx": 200000,  "provider": "github"},
    "openai/o3":                                     {"params": 1200, "ctx": 200000,  "provider": "github"},
    "openai/o1-mini":                                {"params": 1200, "ctx": 128000,  "provider": "github"},
    "openai/o1":                                     {"params": 1200, "ctx": 200000,  "provider": "github"},
    "openai/DeepSeek-V3-0324":                       {"params": 685,  "ctx": 65536,   "provider": "github"},
    "openai/deepseek-r1-0528":                       {"params": 685,  "ctx": 65536,   "provider": "github"},
    "openai/Meta-Llama-3.1-405B-Instruct":           {"params": 405,  "ctx": 131072,  "provider": "github"},
    "openai/Meta-Llama-3.1-70B-Instruct":            {"params": 70,   "ctx": 131072,  "provider": "github"},
    "openai/Mistral-large-3-675B-Instruct-2512":     {"params": 675,  "ctx": 128000,  "provider": "github"},

    # ── OpenRouter Free Models ─────────────────────────────────────────────
    "openrouter/nvidia/nemotron-3-super-120b-a12b:free":  {"params": 120, "ctx": 1000000, "provider": "openrouter"},
    "openrouter/owl-alpha":                               {"params": 100, "ctx": 1048576, "provider": "openrouter"},
    "openrouter/openai/gpt-oss-120b:free":                {"params": 120, "ctx": 131072,  "provider": "openrouter"},
    "openrouter/openai/gpt-oss-20b:free":                 {"params": 20,  "ctx": 131072,  "provider": "openrouter"},
    "openrouter/nvidia/nemotron-nano-12b-v2-vl:free":     {"params": 12,  "ctx": 128000,  "provider": "openrouter"},
    "openrouter/liquid/lfm-2.5-1.2b-instruct:free":       {"params": 1,   "ctx": 32768,   "provider": "openrouter"},
    "openrouter/deepseek/deepseek-v4-flash:free":         {"params": 284, "ctx": 131072,  "provider": "openrouter"},
    "openrouter/qwen/qwen3-coder:free":                   {"params": 480, "ctx": 128000,  "provider": "openrouter"},
    "openrouter/nousresearch/hermes-3-llama-3.1-405b:free":{"params": 405, "ctx": 131072,  "provider": "openrouter"},

    # ── NVIDIA NIM Models ──────────────────────────────────────────────────
    "nvidia_nim/nvidia/nemotron-3-super-120b-a12b":       {"params": 120, "ctx": 1000000, "provider": "nvidia_nim"},
    "nvidia_nim/mistralai/mistral-large-3-675b-instruct-2512": {"params": 675, "ctx": 128000, "provider": "nvidia_nim"},
    "nvidia_nim/moonshotai/kimi-k2.6":                    {"params": 200, "ctx": 131072,  "provider": "nvidia_nim"},
    "nvidia_nim/qwen/qwen3-coder-480b-a35b-instruct":     {"params": 480, "ctx": 128000,  "provider": "nvidia_nim"},
    "nvidia_nim/meta/llama-3.3-70b-instruct":             {"params": 70,  "ctx": 128000,  "provider": "nvidia_nim"},
    "nvidia_nim/qwen/qwen3.5-397b-a17b":                  {"params": 397, "ctx": 128000,  "provider": "nvidia_nim"},
    "nvidia_nim/meta/llama-4-maverick-17b-128e-instruct":  {"params": 400, "ctx": 1048576, "provider": "nvidia_nim"},
    "nvidia_nim/deepseek-ai/deepseek-r1":                 {"params": 685, "ctx": 65536,   "provider": "nvidia_nim"},

    # ── Groq Models ────────────────────────────────────────────────────────
    "groq/llama-3.3-70b-versatile":                       {"params": 70,  "ctx": 128000,  "provider": "groq"},
    "groq/llama-3.1-8b-instant":                          {"params": 8,   "ctx": 131072,  "provider": "groq"},
    "groq/mistral-saba-24b":                              {"params": 24,  "ctx": 128000,  "provider": "groq"},
    "groq/qwen-qwq-32b":                                 {"params": 32,  "ctx": 131072,  "provider": "groq"},

    # ── Cerebras Models ────────────────────────────────────────────────────
    "cerebras/llama-3.3-70b":                             {"params": 70,  "ctx": 128000,  "provider": "cerebras"},
    "cerebras/llama3.1-8b":                               {"params": 8,   "ctx": 131072,  "provider": "cerebras"},

    # ── DeepInfra Models ───────────────────────────────────────────────────
    "deepinfra/meta-llama/Meta-Llama-3.1-405B-Instruct":  {"params": 405, "ctx": 131072,  "provider": "deepinfra"},
    "deepinfra/meta-llama/Meta-Llama-3.1-70B-Instruct":   {"params": 70,  "ctx": 131072,  "provider": "deepinfra"},
    "deepinfra/Qwen/Qwen2.5-72B-Instruct":               {"params": 72,  "ctx": 131072,  "provider": "deepinfra"},

    # ── HuggingFace Models ─────────────────────────────────────────────────
    "huggingface/meta-llama/Llama-3.1-405B-Instruct":     {"params": 405, "ctx": 131072,  "provider": "huggingface"},
    "huggingface/meta-llama/Llama-3.1-70B-Instruct":      {"params": 70,  "ctx": 131072,  "provider": "huggingface"},
    "huggingface/mistralai/Mistral-Large-Instruct-2412":  {"params": 123, "ctx": 128000,  "provider": "huggingface"},

    # ── Together Models ────────────────────────────────────────────────────
    "together/meta-llama/Meta-Llama-3.1-405B-Instruct-Turbo": {"params": 405, "ctx": 131072, "provider": "together"},
    "together/meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo":  {"params": 70,  "ctx": 131072, "provider": "together"},
    "together/mistralai/Mistral-Large-Instruct-2412":          {"params": 123, "ctx": 128000, "provider": "together"},

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
            # a different prefix (e.g. "gpt-4.1-mini" -> "openai/gpt-4.1-mini")
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
