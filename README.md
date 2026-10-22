# FreeLLM

A Windows system-tray utility (Python) that keeps LLM workflows pointed at the best *free* models: it continuously benchmarks large free models (≥100B parameters) across providers, measures time-to-first-token with streaming requests, and rewrites your proxy routing config so the `free-llm` group (and its `free-llm-fallback` group) always resolve to whatever is currently fastest and alive.

**Providers covered:** GitHub Models, Hugging Face, NVIDIA NIM, OpenRouter, Groq, Together AI, DeepInfra, Cerebras — plus local Ollama and LM Studio.

## What's in the tree

- `engine.py`, `api_server.py`, `config_manager.py` — benchmarking/routing engine, local API, config management
- `dashboard_ui.py`, `comparison_ui.py`, `execution_dashboard*` — tray dashboards and model comparison UI
- `database.py` — benchmark history/results (`benchmark_results.json` is a sample output)
- `build_exe.py` — Windows executable build; `claude-freellm.bat` / `codex-freellm.bat` — launchers that start AI CLIs against the FreeLLM routing
- `deploy_hetzner.ps1` — deployment script for the server side

Version 2.0.0 (per the previous README). The tray icon reflects state (healthy / working / offline) with native Windows notifications on model switches, and it can persist with "Start with Windows".

## Status

A personal infrastructure tool from Robert Pelloni's multi-provider agent-fleet work — the pragmatic answer to "which free model is actually good right now" changing every week. Windows-first; actively used, not packaged for general distribution.
