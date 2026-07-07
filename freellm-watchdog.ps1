# FreeLLM Watchdog - ensures exactly one instance is always running
# Usage: powershell -ExecutionPolicy Bypass -File freellm-watchdog.ps1

$ErrorActionPreference = "SilentlyContinue"
$freellmDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$exe = Join-Path $freellmDir "freellm.exe"
$logDir = Join-Path $freellmDir "logs"
$watchdogLog = Join-Path $logDir "watchdog.log"

# Create logs dir if needed
if (-not (Test-Path $logDir)) { New-Item -ItemType Directory -Path $logDir -Force | Out-Null }

function Write-Log {
    param([string]$msg)
    $ts = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $line = "[$ts] $msg"
    Add-Content -Path $watchdogLog -Value $line
    Write-Host $line
}

function Get-FreeLLMPids {
    Get-Process -Name "freellm" -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id
}

function Test-Port {
    param([int]$port)
    try {
        $tcp = New-Object System.Net.Sockets.TcpClient
        $tcp.Connect("127.0.0.1", $port)
        $tcp.Close()
        return $true
    } catch {
        return $false
    }
}

Write-Log "Watchdog started. Monitoring $exe"

while ($true) {
    $pids = Get-FreeLLMPids
    $portAlive = Test-Port -port 4000

    if ($pids.Count -eq 0) {
        Write-Log "FreeLLM not running. Starting..."
        # Compile Go binary if source exists
        if (Test-Path (Join-Path $freellmDir "cmd\app\main.go")) {
            Write-Log "Compiling Go proxy (freellm.exe)..."
            $tempExe = Join-Path $freellmDir "freellm.exe.old"
            if (Test-Path $tempExe) { Remove-Item $tempExe -Force }
            if (Test-Path $exe) { Rename-Item -Path $exe -NewName "freellm.exe.old" -Force }
            
            Push-Location $freellmDir
            & go build -buildvcs=false -o $exe ./cmd/app/
            $buildExit = $LASTEXITCODE
            Pop-Location
            if ($buildExit -eq 0) {
                Write-Log "Go proxy compiled successfully."
                if (Test-Path $tempExe) { Remove-Item $tempExe -Force }
            } else {
                Write-Log "Go proxy compilation failed. Restoring backup."
                if (Test-Path $tempExe) { Rename-Item -Path $tempExe -NewName "freellm.exe" -Force }
            }
        }
        Start-Process -FilePath $exe -WorkingDirectory $freellmDir -WindowStyle Hidden -RedirectStandardOutput (Join-Path $freellmDir "go_stdout.log") -RedirectStandardError (Join-Path $freellmDir "go_stderr.log")
        Start-Sleep -Seconds 10
        $newPids = Get-FreeLLMPids
        if ($newPids.Count -gt 0) {
            Write-Log "FreeLLM started (PID: $($newPids -join ', '))"
        } else {
            Write-Log "WARNING: FreeLLM failed to start. Retrying in 30s..."
            Start-Sleep -Seconds 20
        }
    }
    elseif ($pids.Count -gt 1) {
        # Multiple instances detected — keep newest, kill oldest by PID
        Write-Log "WARNING: $($pids.Count) instances detected (PIDs: $($pids -join ', ')). Keeping newest."
        $sorted = $pids | Sort-Object -Descending
        $keep = $sorted[0]
        for ($i = 1; $i -lt $sorted.Count; $i++) {
            $killPid = $sorted[$i]
            Write-Log "Killing duplicate PID $killPid"
            Stop-Process -Id $killPid -Force -ErrorAction SilentlyContinue
        }
    }
    elseif (-not $portAlive) {
        # Process exists but port not listening — may be starting up or hung
        Write-Log "Process alive (PID: $($pids[0])) but port 4000 not responding. Waiting 60s..."
        Start-Sleep -Seconds 60
        if (-not (Test-Port -port 4000)) {
            Write-Log "Port 4000 still not responding after 60s. Killing PID $($pids[0]) and restarting."
            Stop-Process -Id $pids[0] -Force -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 3
            Start-Process -FilePath $exe -WorkingDirectory $freellmDir -WindowStyle Hidden -RedirectStandardOutput (Join-Path $freellmDir "go_stdout.log") -RedirectStandardError (Join-Path $freellmDir "go_stderr.log")
            Start-Sleep -Seconds 10
        }
    }
    else {
        # All good — check again in 30 seconds
        Start-Sleep -Seconds 30
    }
}
