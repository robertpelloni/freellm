# FreeLLM — Universal LLM Proxy Router

## Purpose
Go-based proxy that routes LLM chat completion requests across 33+ free/tiered providers using a sequential round-robin queue. Designed as a drop-in OpenAI-compatible endpoint for tools like Claude Code, Cline, and pi-agent.

## Current State

### Architecture
- **Sequential round-robin router**: Candidates sorted by quality score + proven status, tried one at a time with 15s timeout
- **Provider circuit breaker**: 3 failures trips 2min provider cooldown; auth errors (401/402/403) trip immediately
- **Model circuit breaker**: 3 fatal errors disables model for 5min; 401/402/403/404 permanently disables
- **Exponential backoff**: Between full cycles: 1s → 2s → 4s → 8s → 16s → 32s → 60s (cap)
- **Score filter**: Models with `min_params_filter: 120` and negative scores are excluded; floor fixes zero-param models to 0.1 score
- **Debug stream toggle**: `ShowDebugStream` (default false) controls all router events and model prefix injection
- **Watchdog**: `watchdog.bat` monitors freellm.exe every 30s, kills duplicates, auto-restarts

### Running
- **Proxy**: Port 4000, 460 models loaded from cache
- **tokdiet**: Port 7787 (compression sidecar, crashes frequently)
- **Watchdog**: Batch file running in background CMD process
- **Dashboard**: Port 8080 (possibly down)
- **LLMLingua**: Disabled in config

### What Works
- Nvidia/nvidia_nim providers (only ones with valid API keys returning 200)
- Sequential rotation through candidates
- Provider circuit breaker (cooldown on failures)
- Model scoring and filtering
- Background model discovery every 6 hours
- Windows services file entries registered

### What's Broken
- Most providers fail: openrouter(402), opencode_zen(401), dashscope(403), groq(404), github(429), minimax(502)
- Cache has 460 models but only ~5 nvidia models actually work
- tokdiet sidecar crashes repeatedly (EADDRINUSE loop partially fixed with port wait)
- Dashboard not verified running
- Startup shortcut not verified working after reboot

## Key Decisions Made
1. **Sequential over parallel**: Simplified from fan-out to one-at-a-time rotation to avoid rate limits and simplify debugging
2. **Batch watchdog over PowerShell**: PowerShell processes get reaped when parent exits; batch via CMD is more persistent
3. **Score floor for unknown params**: Models with unknown parameters (0) and negative scores get 0.1 floor to pass the score filter
4. **Provider auth → immediate circuit break**: 401/402/403 immediately trips provider cooldown (no waiting for 3 failures)
5. **Empty 200 responses treated as failures**: Router skips to next candidate instead of returning empty content

## Milestones
- [x] Sequential round-robin routing implemented
- [x] Provider circuit breaker wired (was dead code)
- [x] Exponential backoff between cycles
- [x] ShowDebugStream toggle (default off)
- [x] Empty response detection (skip to next candidate)
- [x] Stream continuation on premature [DONE]
- [x] Background model discovery every 6h
- [x] Watchdog batch file with duplicate cleanup
- [x] Primary/fallback model concept removed
- [x] Context menu simplified (no primary/fallback split)
- [ ] Cache refresh: prune dead models (only keep providers with working API keys)
- [ ] Dashboard health check and auto-restart
- [ ] Log rotation/cleanup for accumulated log files
- [ ] Startup shortcut verification
- [ ] tokdiet crash loop fully resolved
