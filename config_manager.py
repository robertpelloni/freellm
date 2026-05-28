import os
from ruamel.yaml import YAML

yaml = YAML()
yaml.preserve_quotes = True
yaml.indent(mapping=2, sequence=4, offset=2)

DEFAULT_CONFIG_PATH = "config.yaml"

def ensure_config_exists(path=DEFAULT_CONFIG_PATH):
    """Creates a basic LiteLLM config if it doesn't exist."""
    if not os.path.exists(path):
        data = {
            "model_list": [
                {
                    "model_name": "active-free-model",
                    "litellm_params": {
                        "model": "openrouter/google/gemma-2-9b-it-free", # Placeholder
                        "api_key": "os.environ/OPENROUTER_API_KEY"
                    }
                }
            ]
        }
        with open(path, 'w') as f:
            yaml.dump(data, f)

def apply_model_to_litellm(model_id, provider_name, path=DEFAULT_CONFIG_PATH):
    """
    Updates the active model in the LiteLLM config.yaml.
    Specifically targets the 'active-free-model' entry.
    """
    if not os.path.exists(path):
        ensure_config_exists(path)

    with open(path, 'r') as f:
        config = yaml.load(f)

    # Find and update the active-free-model
    found = False
    if "model_list" in config:
        for model_entry in config["model_list"]:
            if model_entry.get("model_name") == "active-free-model":
                # Map provider to LiteLLM format if necessary
                # OpenRouter models are usually prefixed with 'openrouter/'
                litellm_model = f"{provider_name}/{model_id}" if provider_name != "openai" else model_id
                model_entry["litellm_params"]["model"] = litellm_model
                found = True
                break

    if not found:
        # Append if not found
        new_entry = {
            "model_name": "active-free-model",
            "litellm_params": {
                "model": f"{provider_name}/{model_id}",
                "api_key": f"os.environ/{provider_name.upper()}_API_KEY"
            }
        }
        if "model_list" not in config:
            config["model_list"] = []
        config["model_list"].append(new_entry)

    with open(path, 'w') as f:
        yaml.dump(config, f)

    print(f"Applied model {model_id} from {provider_name} to {path}")

if __name__ == "__main__":
    # Test
    apply_model_to_litellm("meta-llama/llama-3.1-405b-instruct", "openrouter", "test_config.yaml")
    with open("test_config.yaml", 'r') as f:
        print(f.read())
