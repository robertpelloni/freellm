# FreeLLM Watchdog — monitors freellm.exe and restarts it if it crashes
# Runs silently in the background with minimal resource usage.

param(
    [int]$CheckIntervalSeconds = 15,
    [string]$FreeLLMDir = "C:\Users\hyper\workspace\freellm"
)

$logFile = Join-Path $FreeLLMDir "logs\watchdog.log"
$pidFile = Join-Path ([System.IO.Path]::GetTempPath()) "freellm.pid"

# Load .env file
$envFile = Join-Path $FreeLLMDir ".env"
if (Test-Path $envFile) {
    Get-Content $envFile | Where-Object { $_ -and $_ -notmatch '^#' } | ForEach-Object {
        $key, $val = $_ -split '=', 2
        if ($key -and $val) {
            $key = $key.Trim()
            $val = $val.Trim().Trim('"').Trim("'")
            [System.Environment]::SetEnvironmentVariable($key, $val, [System.EnvironmentVariableTarget]::Process)
        }
    }
}

# Ensure logs directory exists
$logDir = Join-Path $FreeLLMDir "logs"
if (-not (Test-Path $logDir)) { New-Item -ItemType Directory -Path $logDir -Force | Out-Null }

function Write-Log {
    param([string]$Message)
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    "$timestamp [WATCHDOG] $Message" | Out-File -FilePath $logFile -Append -Encoding UTF8
}

function Get-FreeLLMProcess {
    # Check by PID file
    if (Test-Path $pidFile) {
        $savedPid = Get-Content $pidFile -Raw -ErrorAction SilentlyContinue
        if ($savedPid) {
            $savedPid = $savedPid.Trim()
            $proc = Get-Process -Id $savedPid -ErrorAction SilentlyContinue
            if ($proc -and $proc.ProcessName -eq "freellm") {
                # Verify it's actually listening on the port
                try {
                    $req = [System.Net.WebRequest]::Create("http://localhost:4000/api/proxy/health")
                    $req.Timeout = 2000
                    $resp = $req.GetResponse()
                    $resp.Close()
                    return @($savedPid)
                } catch {
                    # Process exists but not responding - stale
                    Write-Log "PID $savedPid exists but not responding. Removing stale PID file."
                    Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
                }
            }
        }
    }
    
    # Fallback: check by process name
    $procs = Get-Process -Name "freellm" -ErrorAction SilentlyContinue
    if ($procs) {
        return $procs
    }
    
    return @()
}

function Start-FreeLLM {
    $exePath = Join-Path $FreeLLMDir "freellm.exe"
    if (-not (Test-Path $exePath)) {
        Write-Log "ERROR: freellm.exe not found at $exePath"
        return $false
    }
    
    try {
        $process = Start-Process -FilePath $exePath -WorkingDirectory $FreeLLMDir -WindowStyle Hidden -PassThru
        Write-Log "Started freellm.exe (PID: $($process.Id))"
        
        # Write PID file
        $process.Id | Out-File -FilePath $pidFile -Force -Encoding UTF8
        
        # Wait for the HTTP server to respond (via proxy health endpoint)
        $timeout = 20
        for ($i = 0; $i -lt $timeout; $i++) {
            try {
                $req = [System.Net.WebRequest]::Create("http://localhost:4000/api/proxy/health")
                $req.Timeout = 2000
                $resp = $req.GetResponse()
                $resp.Close()
                Write-Log "Health check passed after ${i}s (PID: $($process.Id))"
                return $true
            } catch {
                if (-not (Get-Process -Id $process.Id -ErrorAction SilentlyContinue)) {
                    Write-Log "Process exited during startup"
                    return $false
                }
                Start-Sleep -Seconds 1
            }
        }
        
        Write-Log "WARNING: Health check did not pass within ${timeout}s, but process is running"
        return $true
    } catch {
        Write-Log "ERROR starting freellm.exe: $_"
        return $false
    }
}

# Main watchdog loop
Write-Log "Watchdog started (check interval: ${CheckIntervalSeconds}s)"

while ($true) {
    $running = Get-FreeLLMProcess
    
    if ($running.Count -eq 0) {
        Write-Log "freellm.exe not running. Attempting restart..."
        $result = Start-FreeLLM
        if ($result) {
            Write-Log "Restart successful"
        } else {
            Write-Log "Restart FAILED"
        }
    } elseif ($running.Count -gt 1) {
        Write-Log "Multiple instances ($($running.Count)). Killing duplicates..."
        # Keep the first one, kill the rest
        $running | Select-Object -Skip 1 | ForEach-Object {
            Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
            Write-Log "Killed duplicate PID $($_.Id)"
        }
    }
    
    Start-Sleep -Seconds $CheckIntervalSeconds
}
