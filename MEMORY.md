# Memory: LiteLLM Control Panel Architectural Observations

## Codebase Traits
- **Language:** Python
- **GUI Framework:** `pystray` (System Tray), `tkinter`/`PyQt6` (Optional Panel)
- **Networking:** `httpx` or `aiohttp` for async tasks.
- **Config Management:** `ruamel.yaml` to preserve user comments.
- **Storage:** SQLite for persistent health and preference tracking.

## Design Preferences
- **Decoupled Architecture:** Background worker handles heavy lifting; UI stays responsive.
- **Favor Large Models:** Strict >= 100B parameter filter.
- **Weighting:** Capability (80%) weighted significantly higher than Latency (20%).
- **Circuit Breaking:** Failure counts used to temporarily isolate unstable models.

## Discovered Optimizations
- Using LiteLLM's native file-watching allows for zero-downtime model switching.
- Caching provider status prevents unnecessary network calls to paid-only or dead endpoints.
- The `ModelEngine` maintains a `log_queue` for real-time benchmarking events, which is consumed by the `LogViewer` in 'engine log' mode.
- Dynamic Hugging Face discovery is implemented via the HF API, supplementing the static `known_models` list.
