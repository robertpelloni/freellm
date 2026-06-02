# TODO: LiteLLM Control Panel (Go Transition)

## Immediate Tasks
- [ ] Initialize Go project (`go mod init`).
- [ ] Create `internal/db` for SQLite management.
- [ ] Create `internal/engine` for benchmarking.
- [ ] Create `internal/proxy` for the stable gateway.

## Features to Port
- [ ] Parameter size regex parser.
- [ ] TTFT benchmarking logic.
- [ ] Scoring algorithm (Size, Context, Latency).
- [ ] Two-group routing (`free-llm`, `free-llm-fallback`).
- [ ] System tray menu and notifications.
- [ ] Maintenance suite (clear skips, blacklist, etc.).

## Go-Specific Enhancements
- [ ] Request buffering queue in the proxy.
- [ ] Goroutine-based parallel benchmarking with configurable concurrency.
- [ ] Embedded web server for monitoring dashboards.
