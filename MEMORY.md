# Memory: FreeLLM Architectural Observations (Go Transition)

## Codebase Traits
- **Language:** Go (Golang)
- **Concurrency:** Goroutines and Channels for benchmarking and request queueing.
- **GUI Framework:** `systray` (System Tray), Web-based or Fyne for dashboards.
- **Networking:** Native `net/http` for the OpenAI-compatible proxy.
- **Storage:** SQLite (modernc.org/sqlite) and PostgreSQL (github.com/lib/pq) for persistent tracking. Supports `DATABASE_URL` and `POSTGRES_*` environment variables.

## Design Preferences
- **Buffered Gateway:** Implementation of a request queue to ensure "Highly Stable Network" behavior.
- **Decoupled Architecture:** Benchmarking engine runs independently of the proxy server.
- **Favor Large Models:** Strict >= 100B parameter filter (configurable).
- **Circuit Breaking:** Failure counts used to temporarily isolate unstable models.
- **FreeLLM Compatibility:** Maintaining full OpenAI-format support for seamless drop-in replacement.

## Discovered Optimizations
- Using Go's `http.ReverseProxy` as a base for the gateway logic.
- Implementing a custom `RoundTripper` to handle retries, fallbacks, and buffering.
- Go's native compilation provides a single, portable binary for easy deployment.

## External Agent Context
- **pi-agent / pi-coding-agent:** Refers to the agent framework by Mario Zechner. The FreeLLM proxy is specifically optimized to support this agent's tool-call parsing requirements.
- **Tool Transformation:** The proxy includes a transformation layer to convert plaintext tool-call formats (longcat, minimax, XML, triple-backticks) into formal OpenAI `tool_calls` for these agents.
- **Terminology:** "pi-agnet" or "pi-mono" are common user typos or shorthand for this agent framework.
