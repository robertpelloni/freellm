# Install FreeLLM Watchdog into the user's Startup folder
# No admin privileges required — starts automatically at login
$ErrorActionPreference = "Continue"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$watchdogScript = Join-Path $scriptDir "freellm-watchdog.ps1"
$startupDir = [Environment]::GetFolderPath("Startup")
$shortcutPath = Join-Path $startupDir "FreeLLM Watchdog.lnk"

$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = "powershell.exe"
$shortcut.Arguments = "-ExecutionPolicy Bypass -WindowStyle Hidden -File `"$watchdogScript`""
$shortcut.WorkingDirectory = $scriptDir
$shortcut.WindowStyle = 7  # Minimized
$shortcut.Description = "FreeLLM Watchdog - keeps FreeLLM running"
$shortcut.Save()

Write-Host "=== Installed to: $shortcutPath ==="
Write-Host "Watchdog will start automatically at login."
Get-Item $shortcutPath | Select-Object FullName, LastWriteTime
