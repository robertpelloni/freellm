import subprocess
import os
import signal
import sys
import threading
import time


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
        for line in iter(self.process.stdout.readline, ''):
            if line:
                # Detect traffic patterns in LiteLLM logs
                # Common patterns: "POST /chat/completions", "GAVE_RESPONSE", "LiteLLM: Success"
                line_lower = line.lower()
                if "post /" in line_lower or "gave_response" in line_lower or "success" in line_lower:
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

        print(f"Starting LiteLLM with config: {self.config_path}")
        try:
            cmd = ["litellm", "--config", self.config_path]

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
                bufsize=1, # Line buffered
            )

            # Start background thread to consume stdout
            threading.Thread(target=self._read_stdout, daemon=True).start()

            return True
        except Exception as e:
            print(f"Failed to start LiteLLM: {e}")
            return False

    def stop(self):
        if self.process:
            print("Stopping LiteLLM...")
            if sys.platform == "win32":
                subprocess.call(['taskkill', '/F', '/T', '/PID', str(self.process.pid)])
            else:
                os.killpg(os.getpgid(self.process.pid), signal.SIGTERM)
            self.process = None
            return True
        return False

    def restart(self, env=None):
        """Restart the LiteLLM proxy process."""
        self.stop()
        import time
        time.sleep(1)
        return self.start(env=env)

    def is_running(self):
        return self.process is not None and self.process.poll() is None

    def check_health(self):
        """Checks if the LiteLLM proxy is responding."""
        import httpx
        for endpoint in ["/health", "/health/readiness", "/health/liveness"]:
            try:
                response = httpx.get(f"http://localhost:4000{endpoint}", timeout=2.0)
                if response.status_code == 200:
                    return True
            except:
                continue
        return False
