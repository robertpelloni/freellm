import subprocess
import os
import signal
import sys
import threading
import time


def _kill_port_4000():
    """Kill any process listening on port 4000 to prevent bind failures."""
    try:
        if sys.platform == "win32":
            result = subprocess.run(
                ["netstat", "-ano"], capture_output=True, text=True, timeout=5
            )
            pids = set()
            for line in result.stdout.split("\n"):
                if ":4000" in line and "LISTEN" in line:
                    parts = line.strip().split()
                    if parts:
                        try:
                            pids.add(int(parts[-1]))
                        except ValueError:
                            pass
            import ctypes

            kernel32 = ctypes.windll.kernel32
            for pid in pids:
                handle = kernel32.OpenProcess(1, False, pid)
                if handle:
                    kernel32.TerminateProcess(handle, 1)
                    kernel32.CloseHandle()
                    print(f"Killed zombie on port 4000 (PID {pid})")
            if pids:
                time.sleep(2)
        else:
            # Unix: use lsof or fuser
            subprocess.run(["fuser", "-k", "4000/tcp"], capture_output=True, timeout=5)
            time.sleep(1)
    except Exception as e:
        print(f"Warning: could not clear port 4000: {e}")


class LiteLLMProcess:
    def __init__(self, config_path="config.yaml"):
        self.config_path = config_path
        self.process = None
        self.log_buffer = []
        self.last_traffic_time = 0
        self.traffic_active = False

    def _read_stdout(self):
        """Continuously read stdout to prevent pipe deadlocks and track traffic."""
        if not self.process or not self.process.stdout:
            return
        for line in iter(self.process.stdout.readline, ""):
            if line:
                line_lower = line.lower()
                if (
                    "post /" in line_lower
                    or "gave_response" in line_lower
                    or "success" in line_lower
                ):
                    self.last_traffic_time = time.time()
                    self.traffic_active = True
                self.log_buffer.append(line)
                if len(self.log_buffer) > 1000:
                    self.log_buffer.pop(0)

    def is_traffic_active(self):
        """Check if there has been recent traffic (last 2 seconds)."""
        if time.time() - self.last_traffic_time < 2.0:
            return True
        self.traffic_active = False
        return False

    def start(self, env=None):
        """Start the LiteLLM proxy process.

        Args:
            env: Optional dict of environment variables to pass to the child
                 process (e.g. API keys). These are merged on top of the
                 current process's environment.
        """
        if self.process and self.process.poll() is None:
            print("LiteLLM is already running.")
            return True

        # Always kill any zombie on port 4000 before starting
        _kill_port_4000()

        print(f"Starting LiteLLM with config: {self.config_path}")

        try:
            cmd = ["litellm", "--config", self.config_path, "--port", "4000"]
            creationflags = 0
            if sys.platform == "win32":
                creationflags = subprocess.CREATE_NO_WINDOW

            # Prepare full environment: inherit current + overlay extras
            full_env = os.environ.copy()
            if env:
                full_env.update(env)

            self.process = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                creationflags=creationflags,
                env=full_env,
                bufsize=1,  # Line buffered
            )

            # Start background thread to consume stdout
            threading.Thread(target=self._read_stdout, daemon=True).start()

            # Wait for startup and verify
            time.sleep(10)
            if self.process.poll() is not None:
                print(
                    f"LiteLLM DIED IMMEDIATELY with code {self.process.poll()}"
                )
                print(f"First log lines: {self.log_buffer[:10]}")
                return False

            # Verify port 4000 is listening
            import socket

            for attempt in range(6):  # Wait up to 30s for port
                try:
                    sock = socket.create_connection(
                        ("127.0.0.1", 4000), timeout=2
                    )
                    sock.close()
                    print("LiteLLM is listening on port 4000")
                    return True
                except (ConnectionRefusedError, OSError):
                    time.sleep(5)
                    if self.process.poll() is not None:
                        print(
                            f"LiteLLM exited during startup with code {self.process.poll()}"
                        )
                        print(f"Log lines: {self.log_buffer[:10]}")
                        return False

            print("WARNING: LiteLLM started but port 4000 not yet open")
            return True

        except Exception as e:
            print(f"Failed to start LiteLLM: {e}")
            return False

    def stop(self):
        if self.process:
            print("Stopping LiteLLM...")
            if sys.platform == "win32":
                subprocess.call(
                    ["taskkill", "/F", "/T", "/PID", str(self.process.pid)]
                )
            else:
                os.killpg(os.getpgid(self.process.pid), signal.SIGTERM)
            self.process = None
            return True
        return False

    def restart(self, env=None):
        """Restart the LiteLLM proxy process."""
        self.stop()
        _kill_port_4000()
        time.sleep(2)
        return self.start(env=env)

    def is_running(self):
        if self.process is None:
            return False
        poll = self.process.poll()
        if poll is not None:
            print(f"LiteLLM process exited with code {poll}")
            return False
        return True

    def check_health(self):
        """Checks if the LiteLLM proxy is responding.

        First does a quick TCP port check, then tries HTTP endpoints.
        A timeout on the HTTP check means the server is alive but busy = healthy.
        """
        import socket
        import httpx

        # Quick TCP check - if port 4000 isn't open, server is down
        try:
            sock = socket.create_connection(("127.0.0.1", 4000), timeout=2)
            sock.close()
        except (socket.timeout, ConnectionRefusedError, OSError):
            return False

        # Port is open - try HTTP health endpoint
        for endpoint in ["/health", "/health/readiness", "/health/liveness"]:
            try:
                response = httpx.get(
                    f"http://localhost:4000{endpoint}", timeout=5.0
                )
                if response.status_code == 200:
                    return True
            except httpx.TimeoutException:
                # Timeout = server is alive but slow to respond = HEALTHY
                return True
            except:
                continue

        # Port is open but no HTTP response yet (still initializing)
        return True  # Port is open = server is alive
