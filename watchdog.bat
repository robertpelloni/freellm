@echo off
cd /d C:\Users\hyper\workspace\freellm

echo [WATCHDOG] %date% %time% Started

:LOOP

:: Dump tasklist to a temp file and count matching lines
tasklist /NH /FI "IMAGENAME eq freellm.exe" 2>nul > "%TEMP%\wd_freellm.txt"
set COUNT=0
for /f %%a in ('find /I /C "freellm.exe" ^< "%TEMP%\wd_freellm.txt"') do set COUNT=%%a

if %COUNT% gtr 1 (
    echo [WATCHDOG] %date% %time% %COUNT% instances! Killing all and restarting...
    taskkill /F /IM freellm.exe >nul 2>&1
    ping -n 5 127.0.0.1 >nul
    start /B freellm.exe
    ping -n 8 127.0.0.1 >nul
    echo [WATCHDOG] %date% %time% Restarted
    goto :WAIT
)

if %COUNT% equ 0 (
    echo [WATCHDOG] %date% %time% Not running. Starting...
    start /B freellm.exe
    ping -n 10 127.0.0.1 >nul
    echo [WATCHDOG] %date% %time% Started
    goto :WAIT
)

:: Exactly 1 - all good, nothing printed to keep logs quiet

:WAIT
ping -n 31 127.0.0.1 >nul
goto :LOOP
