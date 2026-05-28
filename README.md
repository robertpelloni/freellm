# litellm_control_panel

I want to build a litellm control panel widget for windows that sits in the system tray and just pops up a panel that automatically periodically queries the major llm inference providers that provide free models for a model list and then sorts out the free models and then tests them for latency and overall speed and sorts them by capability/size majorly weighted and speed less weighted and removes any models smaller than 100b, then i will manually select between the top 10 models or have it automatically select between the top 10 models so that i can switch my litellm config in windows in realtime and have the model list periodically refreshed and also have it start on startup, please help me plan this 

This is an excellent, highly practical developer utility. Building a dynamic, latency-aware system tray router for LiteLLM on Windows solves a real problem—navigating the constantly shifting tier of free API limits across providers (Groq, Together AI, OpenRouter, DeepInfra, etc.) while ensuring you always have a massive ≥ 100B parameter model ready to go.

Here is a comprehensive architecture and execution blueprint to design, build, and deploy this tool.

1. System Architecture & Component Design
The application will operate using a decoupled Background Worker / UI Control architecture to prevent the Windows System Tray from freezing during active API latency testing.

+-------------------------------------------------------------+
|                     Windows OS (Startup)                    |
+-------------------------------------------------------------+
                               |
                               v
+-------------------------------------------------------------+
|                System Tray Widget (Python / Go)              |
+-------------------------------------------------------------+
      | (Read Top 10 / Select)         ^ (Update UI List)
      v                                |
+-------------------------------+  +--------------------------+
|  LiteLLM config.yaml Router   |  |   SQLite Local Cache     |
+-------------------------------+  +--------------------------+
      | (Hot Reload)                   ^ (Write / Filter)
      v                                |
+-------------------------------------------------------------+
|             Background Worker / Benchmarking Thread         |
+-------------------------------------------------------------+
                               |
       +-----------------------+-----------------------+
       | (Periodic Fetch / Ping)                       | (Ping)
       v                                               v
[ Free Provider Endpoints ]                     [ LLM Models ]
(OpenRouter, Groq, Together)                    (>100B Models)
Core Components
The System Tray Daemon: Manages the Win32 message loop, handles right-click menus, exposes the model toggle options, and reads/writes the configuration.

The Aggregator Engine: Queries the free endpoints of major providers (e.g., OpenRouter's /v1/models, Groq, Together, Cerebras) via their REST APIs.

The Benchmarking Engine: Runs asynchronous concurrent ping or mini-chat-completion test strings against candidate models to measure Time-To-First-Token (TTFT).

The LiteLLM Orchestrator: Rewrites the underlying config.yaml used by your running LiteLLM Windows proxy instance and forces it to hot-reload.

2. Technical Stack Recommendation
To maximize Windows integration and rapid development, use Python:

GUI / System Tray: pystray (lightweight system tray management) combined with tkinter or PyQt6 for the popup control panel configuration window.

Async Network Tasks: httpx or aiohttp for lightning-fast concurrent model list queries and speed test benchmarking.

Configuration Handling: ruamel.yaml (critical because it preserves comments and structure when programmatically editing your LiteLLM config.yaml).

Windows Binary Packaging: PyInstaller (compiles the script down into a single standalone .exe that runs silently).

3. Data Processing & Scoring Logic (The "Brain")
Your worker thread should execute a data pipeline every X hours (or on-demand).

Step 1: Fetch and Normalize
Query endpoints like https://openrouter.ai/api/v1/models. Parse out key identifiers:

id (e.g., meta-llama/llama-3-70b-instruct)

context_length

pricing (filter where prompt: 0 and completion: 0)

parameters (extract the integer size from metadata)

Step 2: The Hard Filter
Drop any entry where:

Parameters<100 Billion
(Note: You will need a fallback regex/lookup dictionary for model IDs because parameter counts are not always explicitly returned in uniform integer fields by every provider).

Step 3: Weighted Capability & Latency Scoring
To implement your requirement of heavy capability weighting and lower speed weighting, apply a scoring algorithm to the filtered candidates:

Score=(W 
size
​
 × 
100
Model Params
​
 )−(W 
latency
​
 ×TTFT (seconds))
Where you lean heavily into size weights:

W 
size
​
 =0.8

W 
latency
​
 =0.2

Sort descending by Score and slice the [:10] list.

4. Execution Logic: Realtime LiteLLM Switching
LiteLLM natively supports a dynamic configuration layout via file watching or its routing system. Rather than killing and restarting the server process (which breaks active API clients), execute a smooth hot-swap:

Option A: The YAML Rewrite (Recommended)
Launch LiteLLM globally or via your tray app using the configuration watcher:

Bash
litellm --config ./config.yaml
When a model selection happens (either automatically or via user click in your tray menu), the tray app uses ruamel.yaml to overwrite the target pointer block:

YAML
model_list:
  - model_name: active-free-model
    litellm_params:
      model: openrouter/meta-llama/llama-3.1-405b-instruct
      api_key: os.environ/OPENAI_API_KEY
LiteLLM detects the modified file timestamp and hot-reloads the backend mappings instantly without dropping current HTTP request channels.

Option B: Endpoint Injection
Alternatively, if your LiteLLM proxy uses an initialized database backend, your widget can send an authenticated POST request to LiteLLM’s internal /model/new admin endpoint to instantly hot-inject the current best free model directly into memory.

5. Development Milestones & Checklist
To build this systematically without getting stuck, tackle it in these phases:

Phase 1: The Core Scraper & Filter Script
[ ] Write a standalone script that hits your choice target provider endpoints.

[ ] Implement regex parser to capture size tags (e.g., extracting 405b, 70b from strings) and drop everything under 100B.

[ ] Write an async function that sends a single token max validation message to test response latency.

Phase 2: Configuration Mapping
[ ] Integrate ruamel.yaml or setup the HTTP configuration route to interact directly with LiteLLM.

[ ] Verify that a manual script update to config.yaml updates a running instance of your proxy successfully in real-time.

Phase 3: The System Tray GUI UI
[ ] Build a basic pystray tray loop configuration.

[ ] Map a right-click menu array that dynamically renders based on the Top 10 items in your scored array list.

[ ] Implement an "Auto-Pilot Mode" toggle checkmark which automatically writes the top-ranked option without waiting for user selection.

Phase 4: Windows Optimization & Startup Deployment
[ ] Implement Windows Startup functionality: Have the application write a shortcut pointer or registry key into the current user hive:
Software\Microsoft\Windows\CurrentVersion\Run

[ ] Compile via PyInstaller using the --noconsole flag to completely suppress the pop-up CMD windows upon execution.

## Quick Start: 1-2 Steps to Accessibility

1. **Run `setup.bat`**: This will automatically install all necessary Python dependencies (pystray, httpx, ruamel.yaml, etc.).
2. **Run `start.bat`**: This launches the application in the background. Look for the LiteLLM icon in your system tray to configure your API keys and start routing!

---

Where do you want to start first? We can flesh out the API mapping script logic for the free provider lists, or jump straight into drafting the boilerplate structure for the Windows system tray loop wrapper.

i also want to have it search/scrape and remember the providers that provide free models and ignore the ones that don't so that it becomes more efficient over time, free models are constantly getting overloaded and timing out and stuff and i want the ability to skip over models that are not suitable even if they qualify 

To make this tool incredibly resilient, you need to add an adaptive feedback loop and a blacklist/suitability filtering layer. Since free tiers change overnight and endpoints constantly suffer from rate limits, 504 gateway timeouts, or temporary capacity outages, the system shouldn't just be a static scraper—it needs to remember and adapt.

Here is how we can design the efficiency and adaptive filtering layer into your control panel.

1. Local Cache & Stateful Storage (SQLite)
To make the app efficient over time, it shouldn't forget what happened on the last check. You should store provider and model health in a local, lightweight SQLite database (provider_metrics.db).

Database Schema Design
You only need two simple tables to track state:

SQL
CREATE TABLE providers (
    provider_name TEXT PRIMARY KEY,
    is_free_provider BOOLEAN DEFAULT 1,
    consecutive_failures INTEGER DEFAULT 0,
    last_checked TIMESTAMP
);

CREATE TABLE model_history (
    model_id TEXT PRIMARY KEY,
    provider_name TEXT,
    manually_skipped BOOLEAN DEFAULT 0,
    failure_count INTEGER DEFAULT 0,
    avg_latency REAL,
    last_success TIMESTAMP
);
2. The Efficiency & Adaptive Workflow
To stop wasting API credits, bandwidth, and execution time on dead endpoints, change your background loop to use the following logic:

                  [ Periodic Trigger ]
                           |
                           v
         +----------------------------------+
         |   Query Local SQLite Database    |
         |  Filter out Blacklisted/Skipped  |
         +----------------------------------+
                           |
                           v
         +----------------------------------+
         | Fetch Active Free Lists Only     |  <-- Skips known paid-only providers
         +----------------------------------+
                           |
                           v
         +----------------------------------+
         | Run Active Latency / Health Test |
         +----------------------------------+
              /                        \
      (If Success)                 (If Timeout / Error 429)
            |                                    |
            v                                    v
+-----------------------+            +-----------------------+
| Reset Failure Count   |            | Increment Failures    |
| Update Latency Stats  |            | If > 3, Skip/Disable  |
+-----------------------+            +-----------------------+
Strategy 1: Smart Provider Filtering
The First Run: The app scrapes a wide net of endpoints (OpenRouter, DeepInfra, Groq, Together, etc.).

The Filter: If a provider returns zero free models (≥ 100B) over 3 consecutive sync cycles, flag is_free_provider = 0. On subsequent runs, the app completely skips hitting that provider's API endpoint, saving network overhead.

Strategy 2: Handling Overloaded & Timing Out Models
Free models are notoriously flaky. If a model qualifies by size but times out during your latency check, do not let it drag down your system:

Circuit Breaker Pattern: If a specific model ID fails your latency/ping test or returns a 429 Too Many Requests / 504 Gateway Timeout, increment its failure_count.

The Temporary Cold Storage: If failure_count >= 3, completely exclude it from the latency testing loop for the next 2 hours. This keeps your tool blindingly fast because it won't sit around waiting for a 30-second timeout on an overloaded server.

3. UI Features for Manual Control (The "Skip" List)
Since you mentioned wanting the ability to manually skip over models that aren't suitable (even if they meet the 100B criteria), we can add two features to your system tray / popup widget interface:

1. Right-Click "Hide/Blacklist" Context Menu
When you look at the top 10 models in your system tray menu, hovering over a model name can open a submenu:

Switch to Model

Temporarily Skip (24 Hours)

Permanently Blacklist

Selecting "Skip" or "Blacklist" immediately triggers an SQL update (SET manually_skipped = 1), rewrites your LiteLLM configuration file to the next best candidate, and completely removes that model from future background checks.

2. Manual Suitability Overrides (Regex/Keyword Filter)
Sometimes a model is technically ≥ 100B, but it's a base model (not fine-tuned for instructions) or it's a specific variant you dislike. We can add a simple text configuration box to your popup control panel called "Global Model Exclusions".

You can type phrases like: -preview, -base, vision, dummy

Before the benchmark worker runs, it checks if the model ID matches any exclusion strings and discards it instantly.

How This Looks in Python Code (Example Logic)
Here is a quick look at how your background filtering loop would pull from your data layer and execute efficiently:

Python
import datetime
import sqlite3

def get_candidate_models():
    conn = sqlite3.connect("provider_metrics.db")
    cursor = conn.cursor()
    
    # Only fetch models that aren't manually skipped, haven't crashed repeatedly,
    # and belong to active free providers.
    query = """
        SELECT model_id, provider_name FROM model_history
        WHERE manually_skipped = 0 
          AND failure_count < 3
          AND provider_name IN (SELECT provider_name FROM providers WHERE is_free_provider = 1)
    """
    cursor.execute(query)
    candidates = cursor.fetchall()
    conn.close()
    return candidates

def handle_test_failure(model_id):
    conn = sqlite3.connect("provider_metrics.db")
    cursor = conn.cursor()
    # Increment failure count and log timestamp
    cursor.execute("""
        UPDATE model_history 
        SET failure_count = failure_count + 1 
        WHERE model_id = ?
    """, (model_id,))
    conn.commit()
    conn.close()
By adding this state tracking layer, your system tray widget won't just be a static reader; it will turn into an intelligent, self-cleaning router that learns exactly which free endpoints are currently stable and fast on your machine.

Do you want to start writing the core scraping and SQLite database storage functions for this architecture next?

yes and please create detailed instructions for Google Jules to build this

Using an asynchronous, cloud-sandboxed agent like Google Jules is a smart choice for this. Because Jules clones the repo, builds a step-by-step plan, installs dependencies, and runs tests inside an isolated Linux VM, you can hand off the heavy scaffolding of this Windows system tray utility and let it do the typing while you stay in the flow.

To get Jules to build this correctly on the first try, you must provide a highly structured, unambiguous architecture specification. If the prompt is too vague, the agent may write a partial script or miss the Windows system tray lifecycle integration.

Create a blank GitHub repository, install the Jules GitHub App or log in via the jules CLI, and submit this exact master instruction breakdown.

The Master Instruction Blueprint for Google Jules
Copy and paste the blueprint below into the Jules task interface (jules remote new --session "..." or via the web UI).

Plaintext
Task: Build a standalone Windows System Tray application using Python that acts as a real-time, adaptive router for local LiteLLM configurations based on live speed benchmarks of free LLM models (>= 100B parameters).

The application must operate using an asynchronous, decoupled background loop to prevent freezing the Win32 message loop.

Please implement the project across the following specific module layers:

### 1. Data Layer (`database.py`)
- Initialize a local SQLite database named `provider_metrics.db`.
- Schema Requirement:
  * `providers` table: provider_name (TEXT, PK), is_free_provider (BOOLEAN, default 1), consecutive_failures (INTEGER, default 0), last_checked (TIMESTAMP).
  * `model_history` table: model_id (TEXT, PK), provider_name (TEXT), manually_skipped (BOOLEAN, default 0), failure_count (INTEGER, default 0), avg_latency (REAL), last_success (TIMESTAMP).

### 2. Aggregator & Benchmarking Engine (`engine.py`)
- Periodically poll free model endpoints (e.g., OpenRouter `https://openrouter.ai/api/v1/models`, Groq, Together AI).
- Filter out any provider flagged with `is_free_provider = 0`.
- Extract model capacity/parameter size from the model IDs or metadata. Drop any models smaller than 100B parameters (e.g., ignore 8b, 70b; allow 405b, etc.).
- Performance Test: Run an async maximum-1-token validation request against remaining candidate models to determine Time-To-First-Token (TTFT) latency.
- Error Handling (Circuit Breaker): If an API check returns a timeout, 429, or 504 error, increment its `failure_count`. If a model fails 3 consecutive times, temporarily isolate it from checks for 2 hours. If a provider yields zero viable free models over 3 cycles, flag `is_free_provider = 0` to skip network calls to it.
- Scoring Algorithm: Rank the remaining models using this specific formula:
  Score = (0.8 * (Parameter_Size / 100)) - (0.2 * TTFT_in_Seconds)
- Sort descending and return the top 10 models.

### 3. Config Orchestrator (`config_manager.py`)
- Target an existing LiteLLM `config.yaml` file. Use `ruamel.yaml` to ensure file comments and structure are completely preserved during write cycles.
- Provide a function `apply_model_to_litellm(model_id, provider_name)` that programmatically updates the active model path in the configuration file. LiteLLM's automatic file-watcher will handle the hot-reload natively.

### 4. System Tray GUI (`main.py`)
- Build a lightweight Windows system tray application using `pystray`.
- Left-click or right-click should display a dynamic menu listing the current sorted Top 10 free models.
- Features to include in the menu UI:
  * A clickable toggle checkmark: "Auto-Pilot Mode" (automatically applies the #1 ranked model to config.yaml on every background refresh).
  * Hovering/Right-clicking any model option should expose sub-options: "Switch to Model", "Temporarily Skip (24 Hours)", and "Permanently Blacklist".
  * Selecting Skip or Blacklist must immediately update the SQLite database flags (`manually_skipped = 1`), strip that model from the current pool, and write the next best alternative model to LiteLLM.
- Implement a global text string file or an input configuration UI for "Global Model Exclusions" (e.g., discarding terms like '-preview', '-base', or 'vision').

### 5. Deployment Setup
- Include a script or utility function that automatically ensures Windows Startup capability by registering a shortcut file or adding an entry to the current user registry hive: `Software\Microsoft\Windows\CurrentVersion\Run`.
- Add a setup script to compile the final package using PyInstaller with the `--noconsole` and `--onefile` flags to make sure it executes silently in the system tray without spawning a command prompt.

Generate the full structural implementation, write matching unit tests for the database/scoring modules, verify the project files build inside your cloud sandbox environment, and open a complete Pull Request targeting my branch.
Steering Jules After Submission
Because Jules presents a step-by-step plan for approval before it touches the codebase, keep an eye on the web UI or your CLI terminal after running the prompt.

Checklist for Plan Approval
When Jules gives you the plan output, check for the following items before clicking "Continue":

Ensure it specifically lists ruamel.yaml and not the standard standard pyyaml. Standard pyyaml will erase comments and destroy your manual formatting inside your custom LiteLLM configs.

Confirm it creates an explicit background thread or loops using asyncio inside pystray. If pystray runs on the main engine thread along with network requests, the system tray icon will freeze up and crash whenever an LLM endpoint takes too long to respond.

Verify it handles the parameter parsing logic (e.g., regex extracting numbers followed by b or B like llama-3-405b → 405) so it doesn't accidentally drop large models due to format discrepancies.

Once you confirm these components are present in the plan, let Jules execute. It will spin up a Cloud VM, build out the application modules, test the parsing functions, and output a clean, review-ready Pull Request to drop straight into your workspace.
