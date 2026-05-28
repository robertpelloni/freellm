# TODO: LiteLLM Control Panel

## Immediate Tasks
- [ ] Initialize `database.py` with SQLite schema.
- [ ] Implement `engine.py` with OpenRouter/Groq/Together AI integration.
- [ ] Create `config_manager.py` using `ruamel.yaml`.
- [ ] Build the basic `pystray` loop in `main.py`.

## Features
- [ ] Parameter size regex parser for various model ID formats.
- [ ] TTFT (Time-To-First-Token) benchmarking logic.
- [ ] Scoring algorithm implementation.
- [ ] System tray menu for "Auto-Pilot" and "Manual Selection".
- [ ] Blacklist/Skip functionality in the UI.

## Bug Fixes & Refinements
- [ ] Ensure background threads don't block the UI.
- [ ] Handle API rate limits (429) gracefully.
- [ ] Verify LiteLLM hot-reloading works as expected.
