@echo off
REM ═══════════════════════════════════════════════════════════════
REM Claude Code CLI + FreeLLM Proxy Integration (v4.5.2)
REM ═══════════════════════════════════════════════════════════════
REM
REM Usage: claude-freellm [any claude code args]
REM   claude-freellm              - Start interactive session
REM   claude-freellm -p "hi"     - Print mode (non-interactive)
REM   claude-freellm --model opus - Use Opus alias (routes to best model)
REM
REM Prerequisites:
REM   - FreeLLM proxy running on localhost:4000
REM   - Run freellm.exe or start-freellm.bat to start the tray app
REM
REM The proxy routes ALL Claude model names to free LLM providers:
REM   claude-sonnet-4  -> best available free model (>=100B params)
REM   claude-opus-4    -> best available free model
REM   claude-haiku-4   -> fastest available free model
REM   Subagents        -> also routed through FreeLLM
REM
REM ═══════════════════════════════════════════════════════════════

REM Check if FreeLLM proxy is running
curl -s http://localhost:4000/health >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo [FreeLLM] Proxy not running! Starting...
    start "" /B "%~dp0freellm.exe"
    echo [FreeLLM] Waiting for proxy to start...
    timeout /t 8 /nobreak >nul
    curl -s http://localhost:4000/health >nul 2>&1
    if %ERRORLEVEL% NEQ 0 (
        echo [FreeLLM] ERROR: Proxy failed to start. Run freellm.exe first.
        exit /b 1
    )
)

echo [FreeLLM] Proxy healthy at localhost:4000

REM Set environment variables for Claude Code
set ANTHROPIC_BASE_URL=http://localhost:4000
set ANTHROPIC_API_KEY=sk-freellm-proxy
set OPENAI_BASE_URL=http://localhost:4000/v1
set OPENAI_API_KEY=sk-freellm

REM Model aliases - Claude Code will use these names and FreeLLM routes them
REM to the best available free model regardless of the name sent
set ANTHROPIC_MODEL=claude-sonnet-4-20250514
set ANTHROPIC_SMALL_FAST_MODEL=claude-haiku-4-20250514
set ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-20250514
set ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-20250514
set ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku-4-20250514

REM Launch Claude Code with all arguments passed through
claude %*

