# Structural Map: LiteLLM Control Panel

## Core Logic
- `main.py`: Entry point, system tray management, background worker orchestration.
- `engine.py`: Async benchmarking engine, model discovery, TTFT measurement.
- `database.py`: SQLite data layer, persistence for rankings, history, and usage.
- `config_manager.py`: YAML configuration orchestration for LiteLLM (Hermes-compatible).
- `process_manager.py`: Child process lifecycle management for LiteLLM proxy.

## UI Components
- `dashboard_ui.py`: Main ranking oversight and usage summary.
- `settings_ui.py`: Comprehensive configuration GUI for providers and scoring weights.
- `query_ui.py`: Minimalist streaming chat window for active model testing.
- `log_viewer.py`: Real-time searchable log stream for LiteLLM.
- `status_window.py`: Centralized status hub for health monitoring.

## Utilities
- `startup.py`: Windows registry integration for "Start with Windows".
- `build_exe.py`: PyInstaller packaging script.
- `setup.bat`: Dependency installation script.
- `start.bat`: Silent background execution entry point.
- `tests.py`: Unit test suite.

## Metadata
- `VERSION.md`: Current global version string (v2.1.0).
- `CHANGELOG.md`: Version-by-version adjustment logs.
- `VISION.md`: Ultimate project goal and foundational concepts.
- `MEMORY.md`: Internal architectural observations and design preferences.
- `ROADMAP.md`: Long-term structural milestones.
- `TODO.md`: Immediate short-term tasks.
