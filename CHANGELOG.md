# Changelog: LiteLLM Control Panel

## [0.6.0] - 2024-06-01
### Added
- Local provider support: Automatically detect and benchmark Ollama and LM Studio models.
- Context-length weighting: Included context length in the scoring algorithm for better model selection.
- Automatic Fallback: Health monitor that triggers a model switch if LiteLLM becomes unresponsive.
- User-configurable scoring weights in the Settings GUI.
- Improved "LiteLLM Instance" menu with real-time status.

## [0.5.0] - 2024-05-31
### Added
- Real-time Log Viewer for LiteLLM proxy process.
- Status indicator (Running/Stopped) in the system tray menu.
- Integrated "Start with Windows" toggle in the Settings GUI.
- Improved process management and status reporting.

## [0.4.0] - 2024-05-30
### Added
- LiteLLM process management: Launch and Stop the proxy directly from the system tray.
- View Config: Quickly open the LiteLLM configuration file from the tray menu.
- Configurable LiteLLM path in the Settings GUI.
- Improved "Start with Windows" enable/disable menu items.

## [0.3.0] - 2024-05-29
### Added
- Direct support for DeepInfra and Cerebras providers.
- Dynamic tray icon color coding (Green/Yellow/Red) based on model latency.
- UI fields for DeepInfra, Cerebras, and Global Exclusions in Settings.
- "Start with Windows" enable/disable toggles in the system tray menu.

## [0.2.0] - 2024-05-28
### Added
- GUI Settings Panel using Tkinter for managing API keys and preferences.
- Direct support for Groq and Together AI providers.
- setup.bat and start.bat scripts for easier Windows execution.
- Persistence for application settings in settings.json.

## [0.1.0] - 2024-05-24
### Added
- Initial project structure and documentation.
- Vision, Roadmap, Todo, and Memory files.
- Deployment and versioning standards.
