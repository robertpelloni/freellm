# Session Handoff: FreeLLM v4.5.2

## Overview
All three main APIs are now working reliably with 5/5 success rate on Chat Completions. The proxy uses concurrent fan-out racing with provider cooldown/diversity and direct processJob calls (bypassing queue).

## Working APIs (Verified 5/5)
- **Chat Completions** (`/v1/chat/completions`): ✅ 5/5 success, ~1s first, ~0.3s subsequent
- **Anthropic Messages** (`/v1/messages`): ✅ Model=claude-sonnet-4-20250514, ~0.7s
- **Responses API** (`/v1/responses`): ✅ Non-streaming with content, streaming works
- **Models** (`/v1/models`): ✅ 314 models available
- **Dashboard** (`/`): ✅ Web UI

## Key Architecture Decisions
- **Queue bypass**: `go g.processJob(job)` directly instead of queuing through workers
- **Fan-out racing**: 5 concurrent model requests, first 200 wins
- **Provider cooldown**: 5s on 429/timeout (was 10s, reduced for faster recovery)
- **Non-blocking semaphores**: 2s timeout, skip provider if slot unavailable
- **Provider diversity**: nvidia+nvidia_nim share one fan-out slot
- **Known-working providers**: nvidia, sambanova, cerebras, mistral always included in fan-out
- **Score floor**: Large-param models can't drop below `(min(params,405)/100)*0.2`
- **Cache guard**: Don't save rankings when top model has negative score
- **isTransientError**: 429/413/402/408/503 don't count as model failures
- **Responses API**: Dual JSON/SSE parsing in translateNonStreamToResponses
- **Reasoning migration**: reasoning/reasoning_content moved to content field

## Known Issues
- **Windows tray caps-changed flood**: systray library receives rapid WM_SETTINGCHANGE events
- **NVIDIA rate limiting**: Consistent 429 under TormentNexus load; cooldown mitigates
- **Responses API status=incomplete**: When max_output_tokens is hit, correctly reports incomplete

## Next Steps
- Fix Windows tray caps-changed event flood (use alternative systray or debounce)
- Implement OIDC/Keycloak authentication
- Expand Model Comparison UI for multi-modal evaluation
