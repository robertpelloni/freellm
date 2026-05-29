# Changelog: LiteLLM Control Panel

## [2.4.0] - 2024-06-18
### Added
- Autonomous Monitoring Dashboard: New two-tab UI for verifying autonomous behavior and performance metrics.
- Internal Activity Logging: Core engine and health monitor now log events (switches, failures, fallbacks) to a persistent SQLite table.
- Reliability Analytics: 24h TTFT and success rate aggregation per model and provider.

## [2.4.1] - 2024-06-19
### Added
- End-to-End Integration Testing: New test suite (integration_tests.py) to verify UI-to-Backend-to-API communication.

## [2.3.0] - 2024-06-17
### Added
- Cost Savings Tracking: Automatically calculates and tracks money saved by using free models.
- Savings Dashboard: New UI to visualize total USD saved and breakdown by model.
- Pricing Metadata Sync: Engine now extracts real-time pricing from OpenRouter to ensure accurate savings data.

## [2.2.0] - 2024-06-16
### Added
- External API Layer: New FastAPI-based service for remote monitoring and control.
- Model Comparison UI: New side-by-side view to compare top-tier models with a single prompt.
- Integrated Monitoring: Added direct log viewer shortcuts within the Settings GUI.
- Concurrent Streaming: Model comparison supports simultaneous response streaming from up to 3 models.

## [2.1.1] - 2024-06-15
### Added
- Dynamic Hugging Face Discovery: The engine now fetches text-generation models directly from HF API.
- Live Engine Logs: New log viewer specifically for monitoring benchmarking and ranking events in real-time.
- Multi-Log Support: Separated "Proxy Logs" and "Engine Logs" in the LiteLLM Control menu.

### Fixed
- Fixed bug where `log_viewer.py` would start redundant polling threads on every filter application.
- Renamed internal parameter extraction method to align with the test suite.
- Fixed log viewer stalling after 1000 lines by using a more robust ID-based tracking system.
- Restored inadvertently removed features: Model Leaderboard, Probe Cleanup, and Env Var API key fallbacks.

## [2.1.0] - 2024-06-14
### Added
- Complete known models integration.
- Exclusions from settings.
- Models management UI.

## [1.4.0] - 2024-06-09
### Added
- Advanced Log Viewer: Added live search, filtering, and data management tools.
- Smart Cache: Optimized benchmarking by caching local model latency for 15 minutes.
- Usage Tracking: Integrated query and token tracking for the Quick Query UI.
- Usage Dashboard: Real-time usage summary displayed in the Main Dashboard.

## [1.3.0] - 2024-06-08
### Added
- Direct LiteLLM Control: Start, Stop, and Restart the proxy directly from the top-level tray menu.
- Refactored Menu: Grouped actions into logical sections (Actions, Control, Rankings, Providers).
- Enhanced Status: Live "LiteLLM + Active Model" status line at the top of the menu.

## [1.2.0] - 2024-06-07
### Added
- System Status Window: A centralized dashboard for LiteLLM proxy status, active model, and provider health.
- UX Improvements: Prominent "Open LLM Interface" option and streamlined menu structure.
- Enhanced Status Reporting: Integrated real-time monitoring into the status window.

## [1.1.0] - 2024-06-06
### Added
- Quick Query UI: Integrated a minimalist chat window for direct interaction with the active model.
- Streaming Support: Real-time response streaming in the Quick Query window.
- Lifecycle Automation: Option to automatically start and stop the LiteLLM proxy with the Control Panel.
- UX Refinements: Added Quick Query to the top of the tray menu for instant access.

## [1.9.0] - 2024-06-14
### Added
- Provider Expansion: Added direct support for GitHub Models, Hugging Face, and NVIDIA NIM.
- Custom Endpoints: All providers now support user-configurable Base URLs in Settings.
- Professional README: Complete rewrite highlighting advanced features and provider list.
- Improved UI Layout: Refined Settings GUI for better organization of the expanded provider list.

## [1.8.0] - 2024-06-13
### Added
- Working State Indicator: Tray icon now turns blue when a benchmarking cycle is in progress.
- Quick Actions: "Copy Active Model" and "Documentation" links added to the tray menu.
- Config Management: Backup and Restore LiteLLM `config.yaml` directly from the Maintenance menu.
- Improved Feedback: More descriptive tooltips for working and offline states.

## [1.7.0] - 2024-06-12
### Added
- Network Resilience: Implemented active connectivity checks and intelligent retries for benchmarking.
- Auto-Reconnect: Background worker now retries every 5 minutes if internet is lost.
- Process Auto-Restart: Health monitor now automatically restarts the LiteLLM proxy if it stops unexpectedly.
- Visual Connectivity Status: Tray icon and tooltip now reflect "Offline" state during network interruptions.

## [1.6.0] - 2024-06-11
### Added
- State Persistence: "Master Routing" enabled/disabled state is now saved across sessions.
- Startup Acceleration: Last known model rankings are loaded immediately from the database on app launch.
- Improved UX: Populated tray menu and dashboard even before the first benchmarking cycle completes.

## [1.5.0] - 2024-06-10
### Added
- Custom Endpoints: Configure base URLs for all providers (cloud and local) in Settings.
- Master Routing Toggle: New "Master Routing" option in tray menu to globally enable/disable configuration updates.
- Refined Settings GUI: Scrollable interface with grid layout for better organization of many settings.

## [1.0.0] - 2024-06-05
### Added
- Official 1.0.0 Release!
- Enhanced Tray Menu: Real-time "Active Model" and "Last Benchmark" display.
- Provider Status Dashboard: View health of all model providers at a glance from the tray.
- Quick Route Configuration: Toggle providers on/off directly from the context menu.
- Integrated Process Status: Tooltip now shows if LiteLLM proxy is Running or Stopped.

## [0.9.0] - 2024-06-04
### Added
- Main Dashboard: A structured GUI to view all model rankings, scores, and status at once.
- Dynamic Tooltip: System tray icon now shows the active model and TTFT latency on hover.
- Maintenance Tools: New menu options to clear skips, clear blacklist, and reset provider statistics.
- Refined UX: Dashboard is now the primary tray icon action.

## [0.8.0] - 2024-06-03
### Added
- Click-to-open functionality: Double-clicking or selecting the tray icon now opens the LLM Interface.
- Configurable "LLM Interface URL" in the Settings GUI.
- Streamlined access to the model interface.

## [0.7.0] - 2024-06-02
### Added
- Background Notification System: Alerts for model switches, health failures, and crashes.
- Notification toggle in Settings GUI.
- Enhanced Persistence: Global exception handling in the background worker to prevent silent exits.
- Improved reliability and user alerting.

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
