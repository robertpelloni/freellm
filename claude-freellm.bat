@echo off
REM ═══════════════════════════════════════════════════════════════
REM Claude Code CLI + FreeLLM Proxy Integration
REM ═══════════════════════════════════════════════════════════════
REM
REM Usage: claude-freellm [any claude code args]
REM   claude-freellm              - Start interactive session
REM   claude-freellm -p "hi"      - Print mode (non-interactive)
REM   claude-freellm --model sonnet  - Use specific model alias
REM
REM Prerequisites:
REM   - FreeLLM proxy running on localhost:4000
REM   - Run start.bat in litellm_control_panel to start the tray app
REM
REM The proxy routes all Claude model names to free LLM providers:
REM   claude-sonnet-4   → best available free model (>=100B params)
REM   claude-opus-4     → best available free model
REM   claude-haiku-4    → fastest available free model
REM
REM ═══════════════════════════════════════════════════════════════

REM Check if FreeLLM proxy is running
curl -s http://localhost:4000/health >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo [FreeLLM] Proxy not running! Starting...
    start "" /B cmd /c "cd /d %~dp0 && python -m litellm --config freellm-config.yaml --port 4000"
    echo [FreeLLM] Waiting for proxy to start...
    timeout /t 8 /nobreak >nul
    curl -s http://localhost:4000/health >nul 2>&1
    if %ERRORLEVEL% NEQ 0 (
        echo [FreeLLM] ERROR: Proxy failed to start. Run start.bat first.
        exit /b 1
    )
)
echo [FreeLLM] Proxy healthy at localhost:4000

REM Set environment variables for Claude Code
set ANTHROPIC_BASE_URL=http://localhost:4000
set ANTHROPIC_API_KEY=sk-freellm-proxy
set OPENAI_BASE_URL=http://localhost:4000/v1
set OPENAI_API_KEY=sk-freellm

REM Launch Claude Code with all arguments passed through
claude %*
