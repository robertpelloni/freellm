@echo off
cd /d C:\Users\hyper\workspace\freellm
echo [FreeLLM] Compiling Go proxy...
go build -buildvcs=false -o freellm.exe ./cmd/app/
set GEMINI_API_KEY=
start /B freellm.exe
echo FreeLLM Proxy started on port 4000
echo   OpenAI endpoint: http://localhost:4000/v1/chat/completions
echo   Anthropic endpoint: http://localhost:4000/v1/messages
echo   Models endpoint: http://localhost:4000/v1/models
echo.
echo Available models:
echo   gemini-3.5-flash (15 RPM free)
echo   gemini-3-flash-preview (20 RPM free)  
echo   gemini-3.1-pro-preview (paid only)
echo   free-llm (fan-out to best available)
echo   gpt-4o / gpt-4o-mini (alias - fan-out)
echo   claude-3-5-sonnet-20241022 (alias - fan-out)
