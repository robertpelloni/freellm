#!/usr/bin/env python3
"""
Silent watchdog for freellm.exe.
Replaces the original watchdog.bat which flashed console windows.

Monitors freellm.exe process, restarts if it crashes or duplicates appear.
Runs every 30 seconds. Use with pythonw.exe (no console window).
"""

import os
import subprocess
import sys
import time

WORKSPACE = os.path.dirname(os.path.abspath(__file__))
FRELLM_EXE = os.path.join(WORKSPACE, "freellm.exe")
CHECK_INTERVAL = 30  # seconds
LOG_FILE = os.path.join(WORKSPACE, "watchdog.log")

def log(msg):
    ts = time.strftime("%Y-%m-%d %H:%M:%S")
    with open(LOG_FILE, "a", encoding="utf-8") as f:
        f.write(f"[{ts}][WATCHDOG] {msg}\n")

def find_freellm_pids():
    """Return list of PIDs of freellm.exe processes."""
    try:
        result = subprocess.run(
            ["tasklist", "/FI", "IMAGENAME eq freellm.exe", "/FO", "CSV", "/NH"],
            capture_output=True, text=True, timeout=10,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )
        pids = []
        for line in result.stdout.splitlines():
            if "freellm.exe" in line:
                parts = line.split(",")
                if len(parts) >= 2:
                    pid = parts[1].strip().strip('"')
                    if pid.isdigit():
                        pids.append(int(pid))
        return pids
    except Exception as e:
        log(f"Error finding processes: {e}")
        return []

def start_freellm():
    """Start freellm.exe silently."""
    try:
        subprocess.Popen(
            [FRELLM_EXE],
            cwd=WORKSPACE,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            creationflags=subprocess.CREATE_NO_WINDOW | subprocess.DETACHED_PROCESS,
        )
        log("Started freellm.exe")
        return True
    except Exception as e:
        log(f"Failed to start freellm.exe: {e}")
        return False

def kill_all():
    """Kill all freellm.exe processes."""
    try:
        subprocess.run(
            ["taskkill", "/F", "/IM", "freellm.exe"],
            capture_output=True, timeout=10,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )
        log("Killed all freellm.exe processes")
    except Exception:
        pass

def main():
    log("=" * 50)
    log("WATCHDOG STARTED (silent mode)")
    log(f"Monitoring: {FRELLM_EXE}")
    log(f"Check interval: {CHECK_INTERVAL}s")
    log("=" * 50)

    while True:
        try:
            pids = find_freellm_pids()
            count = len(pids)

            if count > 1:
                log(f"{count} instances found (PIDs: {pids}). Killing all and restarting...")
                kill_all()
                time.sleep(5)
                start_freellm()
                time.sleep(8)
            elif count == 0:
                log("Not running. Starting...")
                start_freellm()
                time.sleep(10)
            # count == 1 → all good, do nothing

        except Exception as e:
            log(f"Watchdog error: {e}")

        time.sleep(CHECK_INTERVAL)

if __name__ == "__main__":
    main()
