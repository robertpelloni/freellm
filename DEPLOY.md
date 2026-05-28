# Deployment: LiteLLM Control Panel

## Prerequisites
- Windows 10/11
- Python 3.9+
- LiteLLM installed and running (optional, but needed for switching to work)

## Installation
1. Clone the repository.
2. Install dependencies:
   ```bash
   pip install pystray httpx ruamel.yaml pyinstaller
   ```

## Development
- Run `python main.py` to start the tray application.
- The app will create `provider_metrics.db` in the same directory.
- It will look for `config.yaml` (LiteLLM config) in the configured path.

## Packaging
To create a standalone Windows executable:
```bash
pyinstaller --noconsole --onefile --name "LiteLLMControlPanel" main.py
```
This will generate an `.exe` in the `dist` folder.

## Startup Integration
The application can be configured to start with Windows. This is handled via the "Start with Windows" toggle in the menu, which adds a registry key to `HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run`.
