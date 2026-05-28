# TODO: LiteLLM Control Panel

## Immediate Tasks
- [x] Implement a "Live Logs" or "Event Stream" viewer to monitor benchmarking in real-time.
- [x] Refine the Log Viewer with search and filtering capabilities.
- [x] Implement side-by-side model comparison UI.
- [x] Implement External API Layer for remote monitoring and control.

## Features
- [x] Parameter size regex parser for various model ID formats.
- [x] TTFT (Time-To-First-Token) benchmarking logic.
- [x] Scoring algorithm implementation with Context Length weighting.
- [x] System tray menu for "Auto-Pilot" and "Manual Selection".
- [x] Blacklist/Skip functionality in the UI.
- [x] Local provider support (Ollama, LM Studio).
- [x] Automatic Fallback health monitor.
- [x] Direct LiteLLM Control (Start/Stop/Restart) from tray.
- [x] Streamlined System Status and Dashboard UIs.

## Bug Fixes & Refinements
- [x] Ensure background threads don't block the UI.
- [x] Handle API rate limits (429) gracefully.
- [x] Verify LiteLLM hot-reloading works as expected.
