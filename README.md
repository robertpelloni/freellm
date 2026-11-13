# FreeLLM

A Windows system-tray utility that keeps LLM workflows pointed at the best *free* models: it continuously benchmarks large free models (≥100B parameters) across providers, measures time-to-first-token with streaming requests, and rewrites your proxy routing config so the `free-llm` group (and its `free-llm-fallback` group) always resolve to whatever is currently fastest and alive.

**Providers covered:** GitHub Models, Hugging Face, NVIDIA NIM, OpenRouter, Groq, Together AI, DeepInfra, Cerebras — plus local Ollama and LM Studio.

## Status

**Personal infrastructure tool, sporadically maintained.** Version in tree: 4.6.3 (`VERSION.md`; `CHANGELOG.md` is stale — its last entry is 4.5.0, 2025-05-22). Last commit: 2026-11-05 (a sync merge). Last substantive code fix: 2026-07-07 (cross-platform build / tool-call transform fix). Windows-first; actively used by its author, not packaged for general distribution.

## What's in the tree

- `engine.py`, `api_server.py`, `config_manager.py` — benchmarking/routing engine, local API, config management
- `dashboard_ui.py`, `comparison_ui.py`, `execution_dashboard*` — tray dashboards and model comparison UI
- `database.py` — benchmark history/results (`benchmark_results.json` is a sample output)
- `cmd/`, `internal/` — Go port (proxy gateway with request queueing, SQLite persistence, embedded web dashboard; see `CHANGELOG.md` 4.5.0)
- `build_exe.py` — Windows executable build; `claude-freellm.bat` / `codex-freellm.bat` — launchers that start AI CLIs against the FreeLLM routing
- `deploy_hetzner.ps1` — deployment script for the server side
- Watchdog scripts (`watchdog.*`, `freellm-watchdog.ps1`) to keep the tray app running

The tray icon reflects state (healthy / working / offline) with native Windows notifications on model switches, and it can persist with "Start with Windows".

## Background

Built as part of Robert Pelloni's multi-provider agent-fleet work — the pragmatic answer to "which free model is actually good right now" changing every week.
