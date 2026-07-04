@echo off
echo ===================================================
echo FreeLLM Watchdog & Proxy Restart Utility
echo ===================================================
echo.

echo [FreeLLM] Stopping running proxy (freellm.exe)...
taskkill /F /IM freellm.exe >nul 2>&1

echo [FreeLLM] Stopping running watchdog processes...
wmic process where "commandline like '%%freellm-watchdog%%'" call terminate >nul 2>&1

echo [FreeLLM] Compiling latest Go proxy changes...
go build -buildvcs=false -o freellm.exe ./cmd/app/
if %ERRORLEVEL% NEQ 0 (
    echo.
    echo [FreeLLM] ERROR: Go compilation failed!
    echo Please ensure Go is installed and in your PATH.
    pause
    exit /b 1
)
echo [FreeLLM] Go proxy compiled successfully.

echo [FreeLLM] Restarting watchdog in background...
start "FreeLLM Watchdog" powershell -WindowStyle Hidden -ExecutionPolicy Bypass -File freellm-watchdog.ps1

echo.
echo [FreeLLM] Restart complete! The new proxy is now compiled and running.
echo ===================================================
pause
