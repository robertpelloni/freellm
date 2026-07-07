# Session Handoff: FreeLLM v4.5.2

## Overview
All three main APIs working reliably. Model name now logged after each routed message. System tray shows current model in title bar and context menu with dynamic slot updates.

## Working APIs (Verified)
- **Chat Completions** `/v1/chat/completions`: ✅ 3/3 success, ~0.3-1s
- **Anthropic Messages** `/v1/messages`: ✅ Stop=end_turn/max_tokens
- **Responses API** `/v1/responses`: ✅ Non-streaming with content
- **Models** `/v1/models`: ✅ 309 models
- **Dashboard**: ✅ Web UI on :8080

## Fixes Applied This Session
1. **Model name logging**: `[PROXY] Routed to: model (provider) score=X` after each message
2. **Tray title bar**: Shows current primary model name (e.g. "FreeLLM: zai-glm-4.7")
3. **Dynamic context menu**: Pre-created slots (10 primary, 20 fallback) with SetTitle updates
4. **Queue bypass**: `go g.processJob(job)` directly instead of queuing
5. **Responses API**: Dual JSON/SSE parsing in translateNonStreamToResponses
6. **Provider cooldown**: 5s on 429/timeout (faster recovery than 10s)
7. **Non-blocking semaphores**: 2s timeout in fan-out, skip provider on timeout
8. **Score floor, cache guard, isTransientError, reasoning migration**: All carried forward

## Known Issues
- **Windows tray caps-changed flood**: From systray library WM_SETTINGCHANGE events
- **First request cold start**: May time out if all providers on cooldown from TormentNexus
- **Responses API status=incomplete**: When max_output_tokens hit (correct behavior)

## Next Steps
- Fix Windows tray caps-changed event flood
- Implement OIDC/Keycloak authentication
- Expand Model Comparison UI for multi-modal evaluation
