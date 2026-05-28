# LiteLLM Control Panel

A robust Windows system tray utility for autonomous LLM routing, benchmarking, and management.

LiteLLM Control Panel automatically monitors and benchmarks free LLM models (>= 100B parameters) across multiple providers, ensuring your local LiteLLM proxy always routes to the best-performing model available.

## 🚀 Key Features

- **Autonomous Routing:** Automatically updates your LiteLLM `config.yaml` with the #1 ranked free model.
- **Real-time Benchmarking:** Measures Time-To-First-Token (TTFT) using asynchronous streaming requests.
- **Multi-Provider Support:**
  - **Cloud:** OpenRouter, Groq, Together AI, DeepInfra, Cerebras, GitHub Models, Hugging Face, NVIDIA NIM.
  - **Local:** Ollama, LM Studio.
- **Intelligent Filtering:**
  - Strict filtering for massive models (>= 100B parameters).
  - Configurable global exclusions (e.g., `-preview`, `vision`).
  - Adaptive "Smart Cache" for local models.
- **User Interface:**
  - **Main Dashboard:** Comprehensive view of model rankings and performance scores.
  - **Quick Query:** Minimalist chat window for instant model testing.
  - **Advanced Log Viewer:** Real-time search and filtering for the LiteLLM proxy output.
- **Deep Windows Integration:**
  - System Tray icon with dynamic color coding (Green/Yellow/Red/Blue/Gray).
  - Automated proxy lifecycle management (Start/Stop/Restart).
  - "Start with Windows" support.
  - Native desktop notifications for model switches and health alerts.

## 🛠️ Quick Start (1-2 Steps)

1. **Run `setup.bat`**: Automatically installs all Python dependencies.
2. **Run `start.bat`**: Launches the control panel in your system tray.

*Note: Configure your API keys in the **Settings** panel (right-click tray icon) to enable benchmarking for cloud providers.*

## 📐 Scoring Algorithm

Models are ranked based on a weighted score of three primary factors:
- **Parameter Size (60%)**: Favors more capable, larger models.
- **Context Length (20%)**: Higher weights for models with larger context windows.
- **Latency (20%)**: Penalizes slow TTFT response times.

*Weights are fully configurable via the Settings GUI.*

## ⚙️ Maintenance & Safety

- **Config Integrity:** Uses `ruamel.yaml` to ensure your LiteLLM configuration structure and comments are preserved.
- **Health Monitoring:** Implements a circuit-breaker pattern to isolate failing endpoints for 2 hours.
- **Backup/Restore:** Built-in tools to backup and restore your `config.yaml` from the tray menu.

---
Developed for high-performance LLM workflows on Windows.
