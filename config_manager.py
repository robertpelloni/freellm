import os
import shutil
from ruamel.yaml import YAML
from ruamel.yaml.comments import CommentedMap, CommentedSeq

yaml = YAML()
yaml.preserve_quotes = True
yaml.indent(mapping=2, sequence=4, offset=2)

DEFAULT_CONFIG_PATH = "config.yaml"

# Provider -> litellm prefix + env var mapping
PROVIDER_MAP = {
    "openrouter": {"prefix": "openrouter", "env_key": "OPENROUTER_API_KEY"},
    "groq":       {"prefix": "groq",       "env_key": "GROQ_API_KEY"},
    "together":   {"prefix": "together",   "env_key": "TOGETHER_API_KEY"},
    "deepinfra":  {"prefix": "deepinfra",  "env_key": "DEEPINFRA_API_KEY"},
    "cerebras":   {"prefix": "cerebras",   "env_key": "CEREBRAS_API_KEY"},
    "github":     {"prefix": "openai",     "env_key": "GITHUB_TOKEN",
                   "api_base": "https://models.github.ai/inference/v1"},
    "huggingface":{"prefix": "huggingface","env_key": "HUGGINGFACE_API_KEY"},
    "nvidia":     {"prefix": "nvidia_nim", "env_key": "NVIDIA_NIM_API_KEY"},
    "nvidia_nim": {"prefix": "nvidia_nim", "env_key": "NVIDIA_NIM_API_KEY"},
    "ollama":     {"prefix": "ollama",     "env_key": ""},
    "lm_studio":  {"prefix": "openai",     "env_key": "",
                   "api_base": "http://localhost:1234/v1"},
}


def _build_model_entry(model_id: str, provider: str, group_name: str, timeout: int = 30) -> CommentedMap:
    """Build a single model_list entry for the LiteLLM config as a CommentedMap."""
    info = PROVIDER_MAP.get(provider, {"prefix": provider, "env_key": f"{provider.upper()}_API_KEY"})

    prefix = info["prefix"]
    if model_id.startswith(prefix + "/"):
        litellm_model = model_id
    else:
        litellm_model = f"{prefix}/{model_id}"

    entry = CommentedMap()
    entry["model_name"] = group_name

    lp = CommentedMap()
    lp["model"] = litellm_model
    lp["timeout"] = timeout

    env_key = info.get("env_key", "")
    if env_key:
        lp["api_key"] = f"os.environ/{env_key}"

    api_base = info.get("api_base")
    if api_base:
        lp["api_base"] = api_base

    entry["litellm_params"] = lp
    return entry


def read_config(path=DEFAULT_CONFIG_PATH):
    """Read and return the parsed config, or None if it doesn't exist."""
    if not os.path.exists(path):
        return None
    with open(path, 'r', encoding='utf-8') as f:
        return yaml.load(f)


def write_config(config, path=DEFAULT_CONFIG_PATH):
    """Write config dict to yaml file."""
    with open(path, 'w', encoding='utf-8') as f:
        yaml.dump(config, f)


def get_model_entries(path=DEFAULT_CONFIG_PATH):
    """Parse the config and return structured info about all model entries.
    Returns a list of dicts: {id, provider, group, litellm_model, api_key, api_base, timeout, raw_entry}
    """
    config = read_config(path)
    if not config or "model_list" not in config:
        return []

    entries = []
    for entry in config.get("model_list", []):
        if entry is None:
            continue
        model_name = entry.get("model_name", "")
        lp = entry.get("litellm_params", {})
        litellm_model = lp.get("model", "")

        provider = "unknown"
        model_id = litellm_model
        for prov_key, info in PROVIDER_MAP.items():
            prefix = info["prefix"]
            if litellm_model.startswith(prefix + "/"):
                provider = prov_key
                model_id = litellm_model[len(prefix) + 1:]
                break

        entries.append({
            "id": model_id,
            "provider": provider,
            "group": model_name,
            "litellm_model": litellm_model,
            "api_key": lp.get("api_key", ""),
            "api_base": lp.get("api_base", ""),
            "timeout": lp.get("timeout", 30),
            "raw_entry": entry,
        })

    return entries


def get_groups(path=DEFAULT_CONFIG_PATH):
    """Return ordered dict of group_name -> list of model entries."""
    entries = get_model_entries(path)
    groups = {}
    group_order = []
    for e in entries:
        g = e["group"]
        if g not in groups:
            groups[g] = []
            group_order.append(g)
        groups[g].append(e)
    return groups, group_order


def apply_ranked_models(ranked_models: list, path=DEFAULT_CONFIG_PATH,
                         primary_group="free-llm",
                         fallback_group="free-llm-fallback",
                         primary_count=5):
    """Rewrite the config with ranked models split into two groups.

    ranked_models: list of dicts with at least: id, provider, latency, score, parameters
    primary_count: top N go into primary_group, rest into fallback_group

    Preserves router_settings, litellm_settings, and fallbacks structure.
    Adds a header comment with probe results.
    """
    # Read existing config for settings we want to preserve
    existing_config = read_config(path)
    router_settings = CommentedMap()
    litellm_settings = CommentedMap()
    if existing_config:
        if existing_config.get("router_settings"):
            router_settings = existing_config["router_settings"]
        if existing_config.get("litellm_settings"):
            litellm_settings = existing_config["litellm_settings"]

    # Build header comment with probe results
    import datetime
    now = datetime.datetime.now().strftime("%Y-%m-%d")

    comment_lines = [
        f"LiteLLM Config - Updated {now}",
        f"Two groups: {primary_group} (top {primary_count}) + {fallback_group} (remaining)",
        f"Routing: simple-shuffle (picks smart models, not fast small ones)",
        f"Fallback: {primary_group} -> {fallback_group}",
        "",
        "PROBE RESULTS:",
    ]
    for m in ranked_models:
        ctx = m.get("context_length", "?")
        lat = m.get("latency", 0)
        score = m.get("score", 0)
        comment_lines.append(f"  {m['provider']}/{m['id']}  {lat:.1f}s score={score:.1f} ({ctx} ctx)")

    # Build model_list as CommentedSeq
    model_list = CommentedSeq()

    # ── Primary group ──
    # Add section separator comment
    model_list.yaml_add_eol_comment("=== PRIMARY GROUP ===", 0)
    for i, m in enumerate(ranked_models[:primary_count]):
        timeout = 45 if m.get("latency", 0) > 4.0 else 30
        entry = _build_model_entry(m["id"], m["provider"], primary_group, timeout)

        ctx = m.get("context_length", 0)
        lat = m.get("latency", 0)
        score = m.get("score", 0)
        params = m.get("parameters", 0)
        ctx_str = f"{ctx//1000}K" if ctx and isinstance(ctx, (int, float)) and ctx > 0 else "?"
        comment_text = f"Rank {i+1}: {m['id']} via {m['provider']} - {ctx_str} ctx, {lat:.1f}s, {params}B, score={score:.0f}"

        model_list.append(entry)
        model_list.yaml_add_eol_comment(comment_text, i)

    # ── Fallback group ──
    fallback_start = len(model_list)
    for j, m in enumerate(ranked_models[primary_count:]):
        timeout = 60 if m.get("latency", 0) > 4.0 else 30
        entry = _build_model_entry(m["id"], m["provider"], fallback_group, timeout)

        ctx = m.get("context_length", 0)
        lat = m.get("latency", 0)
        params = m.get("parameters", 0)
        ctx_str = f"{ctx//1000}K" if ctx and isinstance(ctx, (int, float)) and ctx > 0 else "?"
        comment_text = f"{m['id']} via {m['provider']} - {ctx_str} ctx, {lat:.1f}s, {params}B"

        model_list.append(entry)
        model_list.yaml_add_eol_comment(comment_text, fallback_start + j)

    # Build full config as CommentedMap
    config = CommentedMap()
    config["model_list"] = model_list

    # Router settings (preserve or default)
    if not router_settings:
        router_settings = CommentedMap({
            "routing_strategy": "simple-shuffle",
            "cooldown_time": 30,
            "allowed_fails": 2,
            "num_retries": 2,
            "timeout": 30,
            "enable_pre_call_checks": True,
            "ignore_cooldown_on_fallbacks": True,
        })
    config["router_settings"] = router_settings

    # LiteLLM settings (preserve or default)
    if not litellm_settings:
        litellm_settings = CommentedMap({
            "drop_params": True,
            "num_retries": 2,
            "request_timeout": 30,
            "allowed_fails": 2,
            "cooldown_time": 30,
        })

    # Ensure fallbacks are set
    litellm_settings["fallbacks"] = [
        {primary_group: [fallback_group]}
    ]
    config["litellm_settings"] = litellm_settings

    # Add header comment
    header = "\n".join(comment_lines) + "\n"
    config.yaml_set_start_comment(header)

    # Backup existing config before overwriting
    if os.path.exists(path):
        shutil.copy2(path, path + ".bak")

    write_config(config, path)
    print(f"Applied {len(ranked_models)} models to {path} (primary={primary_count}, fallback={len(ranked_models)-primary_count})")


def reorder_primary(models_in_primary: list, path=DEFAULT_CONFIG_PATH,
                     primary_group="free-llm",
                     fallback_group="free-llm-fallback"):
    """Reorder models between primary and fallback groups without re-benchmarking.

    models_in_primary: list of model_id strings that should be in the primary group
    All other models go to fallback.

    This is for user-driven reordering: move a model up to primary, or down to fallback.
    """
    entries = get_model_entries(path)
    if not entries:
        print("No model entries found in config.")
        return

    # Split into primary and fallback based on user's selection
    primary_entries = [e for e in entries if e["id"] in models_in_primary or e["litellm_model"] in models_in_primary]
    fallback_entries = [e for e in entries if e not in primary_entries]

    # Rebuild config preserving settings
    existing_config = read_config(path)
    router_settings = existing_config.get("router_settings", CommentedMap()) if existing_config else CommentedMap()
    litellm_settings = existing_config.get("litellm_settings", CommentedMap()) if existing_config else CommentedMap()

    model_list = CommentedSeq()
    for i, e in enumerate(primary_entries):
        entry = e["raw_entry"]
        entry["model_name"] = primary_group
        model_list.append(entry)
        model_list.yaml_add_eol_comment(f"Primary: {e['litellm_model']}", i)

    for j, e in enumerate(fallback_entries):
        entry = e["raw_entry"]
        entry["model_name"] = fallback_group
        model_list.append(entry)

    config = CommentedMap()
    config["model_list"] = model_list
    config["router_settings"] = router_settings
    config["litellm_settings"] = litellm_settings
    config["litellm_settings"]["fallbacks"] = [
        {primary_group: [fallback_group]}
    ]

    # Backup and write
    if os.path.exists(path):
        shutil.copy2(path, path + ".bak")

    write_config(config, path)
    print(f"Reordered: {len(primary_entries)} primary, {len(fallback_entries)} fallback")


def ensure_config_exists(path=DEFAULT_CONFIG_PATH):
    """Creates a basic LiteLLM config if it doesn't exist."""
    if not os.path.exists(path):
        config = CommentedMap()
        model_list = CommentedSeq()
        entry = CommentedMap({
            "model_name": "free-llm",
            "litellm_params": CommentedMap({
                "model": "openrouter/nvidia/nemotron-3-super-120b-a12b:free",
                "api_key": "os.environ/OPENROUTER_API_KEY",
                "timeout": 30,
            })
        })
        model_list.append(entry)
        config["model_list"] = model_list
        config["router_settings"] = CommentedMap({
            "routing_strategy": "simple-shuffle",
            "cooldown_time": 30,
            "allowed_fails": 2,
            "num_retries": 2,
            "timeout": 30,
            "enable_pre_call_checks": True,
            "ignore_cooldown_on_fallbacks": True,
        })
        config["litellm_settings"] = CommentedMap({
            "drop_params": True,
            "num_retries": 2,
            "request_timeout": 30,
            "allowed_fails": 2,
            "cooldown_time": 30,
            "fallbacks": [{"free-llm": ["free-llm-fallback"]}],
        })
        write_config(config, path)


# Legacy compatibility
def apply_model_to_litellm(model_id, provider_name, path=DEFAULT_CONFIG_PATH):
    """Legacy: updates a single model entry. Prefer apply_ranked_models for full config management."""
    print(f"Note: apply_model_to_litellm is deprecated. Use apply_ranked_models for full config management.")
    ensure_config_exists(path)
    config = read_config(path)
    if config and "model_list" in config and len(config["model_list"]) > 0:
        info = PROVIDER_MAP.get(provider_name, {"prefix": provider_name, "env_key": f"{provider_name.upper()}_API_KEY"})
        prefix = info["prefix"]
        litellm_model = f"{prefix}/{model_id}" if not model_id.startswith(prefix + "/") else model_id
        config["model_list"][0]["litellm_params"]["model"] = litellm_model
        if info.get("env_key"):
            config["model_list"][0]["litellm_params"]["api_key"] = f"os.environ/{info['env_key']}"
        if info.get("api_base"):
            config["model_list"][0]["litellm_params"]["api_base"] = info["api_base"]
        write_config(config, path)
        print(f"Updated first model entry to {litellm_model}")


if __name__ == "__main__":
    test_path = r"C:\Users\hyper\.hermes\litellm-config.yaml"
    groups, order = get_groups(test_path)
    print(f"Groups: {order}")
    for g in order:
        print(f"\n  {g}:")
        for e in groups[g]:
            print(f"    {e['litellm_model']} (provider={e['provider']}, timeout={e['timeout']})")
