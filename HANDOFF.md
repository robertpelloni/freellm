# Session Handoff: FreeLLM v4.5.2

## Overview
All three main APIs are now working: Chat Completions, Anthropic Messages, and OpenAI Responses API. The proxy routes requests through concurrent fan-out (racing top model candidates) and bypasses the request queue for immediate processing.

## Working APIs
- **Chat Completions** (`/v1/chat/completions`): ✅ Model=zai-glm-4.7 (Cerebras), ~0.5s response time
- **Anthropic Messages** (`/v1/messages`): ✅ Model=claude-sonnet-4-20250514 (actually Cerebras GLM mapped), ~0.7s
- **Responses API** (`/v1/responses`): ✅ Non-streaming returns proper content, streaming works
- **Models** (`/v1/models`): ✅ 300+ models available
- **Dashboard** (`/`): ✅ Web UI

## Key Fixes Applied This Session
1. **Queue bypass**: `go g.processJob(job)` instead of queue send — avoids congestion from TormentNexus
2. **Responses API JSON parsing**: `translateNonStreamToResponses` now handles both SSE (`data: ` prefix) and regular JSON responses — was only handling SSE, returning empty content
3. **Response building**: Added proper Responses API response object construction (respID, output array, usage, status)

## Known Issues
- **First request cold start**: The first Chat API request after proxy startup may time out (8s) while TormentNexus saturates workers. Subsequent requests succeed in <1s.
- **Windows tray caps-changed flood**: System tray UI receives rapid "caps-changed" events that degrade performance.
- **NVIDIA rate limiting**: NVIDIA consistently returns 429 under load; cooldown mechanism mitigates but doesn't eliminate this.

## Architecture
- **Request flow**: ServeHTTP → processJob (direct, bypasses queue) → fan-out (3 candidates raced) → first 200 wins
- **Provider cooldown**: 429/timeout → 10s cooldown, skipped in fan-out
- **Provider diversity**: nvidia+nvidia_nim share one fan-out slot
- **Known-working providers**: nvidia (qwen3.5-397b), sambanova (DeepSeek-V3.1), cerebras (gpt-oss-120b), mistral (mistral-large-latest) always included
- **Score floor**: Large-param models can't drop below `(min(params,405)/100)*0.2`
- **Cache guard**: Don't save rankings cache when top model has negative score

## Next Steps
- Fix cold-start timeout for first Chat API request
- Implement OIDC/Keycloak authentication
- Expand Model Comparison UI for multi-modal evaluation
- Fix Windows tray caps-changed event flood
