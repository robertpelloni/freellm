# Roadmap: LiteLLM Control Panel (Go Transition)

## Milestone 11: Go Core & Data Layer
- [ ] Initialize Go module and project structure.
- [ ] Port SQLite database schema and persistence layer to Go.
- [ ] Implement Go-native configuration management.

## Milestone 12: Benchmarking Engine (Go)
- [ ] Implement async benchmarking logic for major providers.
- [ ] Port "Smart Cache" and circuit breaker logic to Go.
- [ ] Support dynamic model discovery (GitHub, HF, etc.) in Go.

## Milestone 13: Highly Stable Gateway (Go)
- [ ] Implement OpenAI-compatible proxy server.
- [ ] Build the request queueing and buffering system to prevent connection drops.
- [ ] Implement intelligent rotation and fallback logic in Go.

## Milestone 14: Native UI & Tray (Go)
- [ ] Implement Go-native system tray icon and menu.
- [ ] Port dashboards (Ranking, Monitoring, Protocol) to Go-compatible UI (Web-based or Fyne).
- [ ] Wire all backend features to the new UI.

## Milestone 15: Verification & Deployment
- [ ] Full end-to-end integration testing of the Go implementation.
- [ ] Native Windows build and packaging.
