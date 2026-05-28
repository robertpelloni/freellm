import subprocess
import os
import signal
import sys

class LiteLLMProcess:
    def __init__(self, config_path="config.yaml"):
        self.config_path = config_path
        self.process = None

    def start(self):
        if self.process and self.process.poll() is None:
            print("LiteLLM is already running.")
            return True

        print(f"Starting LiteLLM with config: {self.config_path}")
        try:
            # We assume 'litellm' is in the PATH
            cmd = ["litellm", "--config", self.config_path]
            # On Windows, we might want to use creationflags to hide the window
            creationflags = 0
            if sys.platform == "win32":
                creationflags = subprocess.CREATE_NO_WINDOW

            self.process = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                creationflags=creationflags
            )
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

    def restart(self):
        self.stop()
        import time
        time.sleep(1)
        return self.start()

    def is_running(self):
        return self.process is not None and self.process.poll() is None

    def check_health(self):
        """Checks if the LiteLLM proxy is responding."""
        import httpx
        try:
            # Standard LiteLLM health check or model list
            response = httpx.get("http://localhost:4000/health", timeout=2.0)
            return response.status_code == 200
        except:
            return False
