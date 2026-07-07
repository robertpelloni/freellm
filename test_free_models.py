#!/usr/bin/env python3
"""
Test all free models from multiple providers for availability and responsiveness.
Queries each model with a simple, universal prompt that works across all model types.
"""
import os
import json
import time
import requests
from datetime import datetime

# Provider configurations
PROVIDERS = {
    "openrouter": {
        "base_url": "https://openrouter.ai/api/v1/chat/completions",
        "api_key_env": "OPENROUTER_API_KEY",
        "header_name": "Authorization",
        "header_format": "Bearer {}",
        "free_models": [
            "qwen/qwen3-coder:free",
            "nvidia/nemotron-3-ultra-550b-a55b:free",
            "nvidia/nemotron-3-super-120b-a12b:free",
            "nex-agi/nex-n2-pro:free",
            "poolside/laguna-xs.2:free",
            "poolside/laguna-m.1:free",
            "google/gemma-4-26b-a4b-it:free",
            "google/gemma-4-31b-it:free",
            "qwen/qwen3-next-80b-a3b-instruct:free",
            "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free",
            "nvidia/nemotron-3-nano-30b-a3b:free",
            "openai/gpt-oss-120b:free",
            "openai/gpt-oss-20b:free",
            "meta-llama/llama-3.3-70b-instruct:free",
            "meta-llama/llama-3.2-3b-instruct:free",
            "nousresearch/hermes-3-llama-3.1-405b:free",
            "nvidia/nemotron-3.5-content-safety:free",
            "nvidia/nemotron-nano-12b-v2-vl:free",
            "nvidia/nemotron-nano-9b-v2:free",
            "liquid/lfm-2.5-1.2b-thinking:free",
            "liquid/lfm-2.5-1.2b-instruct:free",
            "cognitivecomputations/dolphin-mistral-24b-venice-edition:free",
        ]
    },
    "groq": {
        "base_url": "https://api.groq.com/openai/v1/chat/completions",
        "api_key_env": "GROQ_API_KEY",
        "header_name": "Authorization",
        "header_format": "Bearer {}",
        "free_models": [
            "llama-3.3-70b-versatile",
            "llama-3.1-8b-instant",
            "mixtral-8x7b-32768",
            "gemma-2-9b-it",
        ]
    },
    "huggingface": {
        "base_url": "https://router.huggingface.co/hf-inference/models",
        "api_key_env": "HUGGINGFACE_API_KEY",
        "header_name": "Authorization",
        "header_format": "Bearer {}",
        # Common free HF inference endpoints
        "free_models": [
            "mistralai/Mixtral-8x7B-Instruct-v0.1",
            "meta-llama/Llama-3.3-70B-Instruct",
            "google/gemma-2-9b-it",
            "microsoft/phi-3-mini-4k-instruct",
        ]
    },
}

# Universal test prompt - simple, works for any model type
TEST_PROMPT = "What is 2+2? Answer with just the number."

def test_model(provider_name, model_id, api_key, base_url, header_name, header_format, timeout=30):
    """Test a single model for availability and responsiveness."""
    headers = {
        "Content-Type": "application/json",
        header_name: header_format.format(api_key)
    }
    payload = {
        "model": model_id,
        "messages": [{"role": "user", "content": TEST_PROMPT}],
        "max_tokens": 10,
        "temperature": 0.1
    }

    start = time.time()
    try:
        resp = requests.post(base_url, headers=headers, json=payload, timeout=timeout)
        elapsed = time.time() - start

        if resp.status_code == 200:
            data = resp.json()
            content = data.get("choices", [{}])[0].get("message", {}).get("content", "")
            # Simple validation: should contain "4" or "four"
            is_valid = "4" in content or "four" in content.lower()
            return {
                "success": True,
                "latency": round(elapsed, 3),
                "status_code": resp.status_code,
                "response_preview": content.strip()[:50],
                "valid": is_valid
            }
        else:
            return {
                "success": False,
                "latency": round(elapsed, 3),
                "status_code": resp.status_code,
                "error": resp.text[:100] if resp.text else f"HTTP {resp.status_code}"
            }
    except Exception as e:
        elapsed = time.time() - start
        return {
            "success": False,
            "latency": round(elapsed, 3),
            "error": str(e)[:100]
        }

def main():
    results = []
    timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    print(f"=== Free Model Tester === {timestamp}\n")

    for provider_name, cfg in PROVIDERS.items():
        api_key = os.environ.get(cfg["api_key_env"])
        if not api_key:
            print(f"[{provider_name}] No API key found in env var {cfg['api_key_env']}, skipping.")
            continue

        print(f"--- Testing {provider_name} ---")
        for model_id in cfg["free_models"]:
            print(f"  {model_id}...", end="", flush=True)
            result = test_model(
                provider_name,
                model_id,
                api_key,
                cfg["base_url"],
                cfg["header_name"],
                cfg["header_format"]
            )
            result["model"] = model_id
            result["provider"] = provider_name
            results.append(result)

            if result["success"]:
                print(f" ✓ ({result['latency']}s)")
            else:
                print(f" ✗ ({result.get('error', 'failed')})")

        print()

    # Save raw results
    out_path = "model_test_results.json"
    with open(out_path, "w") as f:
        json.dump({
            "generated_at": timestamp,
            "results": results
        }, f, indent=2)
    print(f"Results saved to {out_path}")

    # Summary
    success_count = sum(1 for r in results if r["success"])
    print(f"\nSummary: {success_count}/{len(results)} models responded successfully")

    # Sort by latency (fastest first) and show top performers
    successful = [r for r in results if r["success"]]
    sorted_by_latency = sorted(successful, key=lambda x: x["latency"])

    print("\nTop 10 fastest working models:")
    for i, r in enumerate(sorted_by_latency[:10], 1):
        print(f"  {i}. {r['model']} ({r['provider']}) - {r['latency']}s")

if __name__ == "__main__":
    main()
