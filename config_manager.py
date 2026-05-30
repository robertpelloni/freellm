import os
import shutil
from ruamel.yaml import YAML
from ruamel.yaml.comments import CommentedMap, CommentedSeq
import known_models

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
                   "api_base": "https://models.inference.ai.azure.com"},
    "gemini":     {"prefix": "gemini",     "env_key": "GEMINI_API_KEY"},
    "huggingface":{"prefix": "huggingface","env_key": "HUGGINGFACE_API_KEY"},
    "nvidia":     {"prefix": "nvidia_nim", "env_key": "NVIDIA_NIM_API_KEY"},
    "nvidia_nim": {"prefix": "nvidia_nim", "env_key": "NVIDIA_NIM_API_KEY"},
    "ollama":     {"prefix": "ollama",     "env_key": ""},
    "lm_studio":  {"prefix": "openai",     "env_key": "",
                   "api_base": "http://localhost:1234/v1"},
}


def _build_model_entry(model_id: str, provider: str, group_name: str,
                       timeout: int = 30, api_base: str = None) -> CommentedMap:
    """Build a single model_list entry for the LiteLLM config as a CommentedMap."""
    info = PROVIDER_MAP.get(provider, {"prefix": provider, "env_key": f"{provider.upper()}_API_KEY"})

    prefix = info["prefix"]
    # For github, we want the prefix to be 'openai' as per LiteLLM standards for Azure Inference
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

    base = api_base or info.get("api_base")
    if base:
        lp["api_base"] = base

    entry["litellm_params"] = lp

    # Add metadata for model_info display
    metadata = CommentedMap()
    metadata["score"] = 0
    metadata["latency"] = 0
    entry["model_info"] = metadata

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
    """Write the config merging benchmarked models with existing entries.

    ranked_models: list of dicts with at least: id, provider, latency, score, parameters
    primary_count: top N go into primary_group, rest into fallback_group

    Models from ranked_models are placed by score. Any models already in the
    config that were NOT in this benchmark run are preserved in fallback.
    Preserves router_settings, litellm_settings, and fallbacks structure.
    """
    # Read existing config for settings and existing model entries to preserve
    existing_config = read_config(path)
    router_settings = CommentedMap()
    litellm_settings = CommentedMap()
    existing_entries = {}  # model_id -> raw_entry (for preserving un-benchmarked models)
    if existing_config:
        if existing_config.get("router_settings"):
            router_settings = existing_config["router_settings"]
        if existing_config.get("litellm_settings"):
            litellm_settings = existing_config["litellm_settings"]
        # Collect existing model entries so we can preserve ones not in this benchmark
        for entry in existing_config.get("model_list", []):
            if entry is None:
                continue
            lp = entry.get("litellm_params", {})
            litellm_model = lp.get("model", "")
            if litellm_model:
                existing_entries[litellm_model] = entry

    # Identify which models from ranked_models are already in the config
    ranked_model_ids = set()
    for m in ranked_models:
        info = PROVIDER_MAP.get(m.get("provider", ""), {"prefix": m.get("provider", "")})
        prefix = info.get("prefix", m.get("provider", ""))
        model_id = m["id"]
        litellm_model = f"{prefix}/{model_id}" if not model_id.startswith(prefix + "/") else model_id
        ranked_model_ids.add(litellm_model)

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

    # Cap primary_count to available models
    effective_primary = min(primary_count, len(ranked_models))
    if effective_primary < 1 and len(ranked_models) > 0:
        effective_primary = 1

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
        # Store actual score/latency/params in model_info
        if "model_info" in entry:
            entry["model_info"]["score"] = round(score, 2)
            entry["model_info"]["latency"] = round(lat, 3)
            entry["model_info"]["params"] = params
            entry["model_info"]["context"] = ctx

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
        # Store actual score/latency/params in model_info
        if "model_info" in entry:
            entry["model_info"]["score"] = round(m.get("score", 0), 2)
            entry["model_info"]["latency"] = round(lat, 3)
            entry["model_info"]["params"] = params
            entry["model_info"]["context"] = ctx

        model_list.append(entry)
        model_list.yaml_add_eol_comment(comment_text, fallback_start + j)

    # Preserve existing models that were NOT in this benchmark run
    preserved_count = 0
    for litellm_model, entry in existing_entries.items():
        if litellm_model not in ranked_model_ids:
            entry["model_name"] = fallback_group
            model_list.append(entry)
            preserved_count += 1
    if preserved_count:
        print(f"Preserved {preserved_count} existing models not in this benchmark.")

    # Ensure all known good models from our DB are present in the config
    # even if they weren't in the benchmark or existing config
    current_models_in_config = set()
    for entry in model_list:
        if entry is None:
            continue
        lp = entry.get("litellm_params", {})
        if lp:
            current_models_in_config.add(lp.get("model", ""))
    injected_count = 0
    for litellm_id, spec in known_models.all_models().items():
        prov = spec.get("provider", "")
        info = PROVIDER_MAP.get(prov, {"prefix": prov})
        prefix = info.get("prefix", prov)

        # Calculate the actual litellm_model string that will be generated
        base_id = litellm_id.split("/", 1)[-1] if "/" in litellm_id else litellm_id
        target_model_name = f"{prefix}/{base_id}" if not base_id.startswith(prefix + "/") else base_id

        if target_model_name in current_models_in_config:
            continue

        # Only inject if we have the provider's API key in the config already
        # (i.e. the provider is configured)
        env_key = info.get("env_key", "")
        if not env_key:
            continue  # Local providers like ollama - skip auto-injection

        # Build entry from known model spec
        new_entry = _build_model_entry(
            base_id,
            prov,
            fallback_group,
            timeout=30,
        )
        model_list.append(new_entry)
        injected_count += 1
    if injected_count:
        print(f"Injected {injected_count} known good models from database.")

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
            "enable_pre_call_checks": False,
            "ignore_cooldown_on_fallbacks": True,
        })
    else:
        # Always force this to False to prevent startup health check failures
        router_settings["enable_pre_call_checks"] = False

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
    print(f"Applied {len(ranked_models)} models to {path} (primary={effective_primary}, fallback={len(ranked_models)-effective_primary})")


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
            "enable_pre_call_checks": False,
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
