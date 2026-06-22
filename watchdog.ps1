# FreeLLM Watchdog — monitors freellm.exe and restarts it if it crashes
# Runs silently in the background with minimal resource usage.

param(
    [int]$CheckIntervalSeconds = 15,
    [string]$FreeLLMDir = "C:\Users\hyper\workspace\freellm"
)

$logFile = Join-Path $FreeLLMDir "logs\watchdog.log"
$pidFile = Join-Path ([System.IO.Path]::GetTempPath()) "freellm.pid"

# Ensure logs directory exists
$logDir = Join-Path $FreeLLMDir "logs"
if (-not (Test-Path $logDir)) { New-Item -ItemType Directory -Path $logDir -Force | Out-Null }

function Write-Log {
    param([string]$Message)
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    "$timestamp [WATCHDOG] $Message" | Out-File -FilePath $logFile -Append -Encoding UTF8
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
        
        # Write PID file for other tools
        $process.Id | Out-File -FilePath $pidFile -Force -Encoding UTF8
        
        # Wait a moment then check if it's still running
        Start-Sleep -Seconds 3
        if (-not (Get-Process -Id $process.Id -ErrorAction SilentlyContinue)) {
            Write-Log "WARNING: freellm.exe exited immediately after start"
            return $false
        }
        
        # Wait for the HTTP server to respond
        $timeout = 15
        $elapsed = 0
        while ($elapsed -lt $timeout) {
            try {
                $req = [System.Net.WebRequest]::Create("http://localhost:4000/health/readiness")
                $req.Timeout = 3000
                $resp = $req.GetResponse()
                $resp.Close()
                Write-Log "Health check passed (HTTP $($resp.StatusCode))"
                return $true
            } catch {
                Start-Sleep -Seconds 1
                $elapsed++
            }
        }
        
        Write-Log "WARNING: Health check did not pass within ${timeout}s"
        return $true  # Process is running even if health check hasn't passed yet
    } catch {
        Write-Log "ERROR starting freellm.exe: $_"
        return $false
    }
}

# Main watchdog loop
Write-Log "Watchdog started (check interval: ${CheckIntervalSeconds}s)"

while ($true) {
    $running = $false
    
    # Check by PID file first
    if (Test-Path $pidFile) {
        $pid = Get-Content $pidFile -Raw -ErrorAction SilentlyContinue
        if ($pid) {
            $pid = $pid.Trim()
            $proc = Get-Process -Id $pid -ErrorAction SilentlyContinue
            if ($proc -and $proc.ProcessName -match "freellm|app") {
                $running = $true
            }
        }
    }
    
    # Fallback: check by process name
    if (-not $running) {
        $procs = Get-Process -Name "freellm" -ErrorAction SilentlyContinue
        if (-not $procs) {
            $procs = Get-Process -Name "app" -ErrorAction SilentlyContinue | Where-Object { $_.CommandLine -match "freellm" }
        }
        if ($procs) {
            $running = $true
            # Update PID file
            $procs[0].Id | Out-File -FilePath $pidFile -Force -Encoding UTF8
        }
    }
    
    if (-not $running) {
        Write-Log "freellm.exe not running. Attempting restart..."
        $result = Start-FreeLLM
        if ($result) {
            Write-Log "Restart successful"
        } else {
            Write-Log "Restart FAILED"
        }
    }
    
    Start-Sleep -Seconds $CheckIntervalSeconds
}
