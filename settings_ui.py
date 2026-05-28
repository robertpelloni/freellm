import tkinter as tk
from tkinter import ttk, messagebox
import json
import os

SETTINGS_FILE = "settings.json"

def load_settings():
    if os.path.exists(SETTINGS_FILE):
        try:
            with open(SETTINGS_FILE, 'r') as f:
                return json.load(f)
        except:
            pass
    return {
        "OPENROUTER_API_KEY": "",
        "GROQ_API_KEY": "",
        "TOGETHER_API_KEY": "",
        "DEEPINFRA_API_KEY": "",
        "CEREBRAS_API_KEY": "",
        "MIN_PARAMETERS": 100,
        "AUTO_PILOT": False,
        "GLOBAL_EXCLUSIONS": "-preview, -base, vision, dummy",
        "CONFIG_PATH": "config.yaml",
        "INTERFACE_URL": "http://localhost:4000",
        "START_WITH_WINDOWS": False,
        "ENABLE_NOTIFICATIONS": True,
        "SIZE_WEIGHT": 0.6,
        "CONTEXT_WEIGHT": 0.2,
        "LATENCY_WEIGHT": 0.2
    }

def save_settings(settings):
    with open(SETTINGS_FILE, 'w') as f:
        json.dump(settings, f, indent=4)

class SettingsUI:
    def __init__(self, on_save_callback=None):
        self.on_save_callback = on_save_callback
        self.root = tk.Tk()
        self.root.title("LiteLLM Control Panel Settings")
        self.root.geometry("400x350")
        self.root.resizable(False, False)

        self.settings = load_settings()
        self.create_widgets()

    def create_widgets(self):
        self.root.geometry("450x800")
        padding = {'padx': 10, 'pady': 2}

        container = ttk.Frame(self.root)
        container.pack(fill='both', expand=True, padx=10, pady=10)

        # API Keys
        ttk.Label(container, text="OpenRouter API Key:").pack(fill='x', **padding)
        self.or_key = ttk.Entry(container, show="*")
        self.or_key.insert(0, self.settings.get("OPENROUTER_API_KEY", ""))
        self.or_key.pack(fill='x', **padding)

        ttk.Label(container, text="Groq API Key:").pack(fill='x', **padding)
        self.groq_key = ttk.Entry(container, show="*")
        self.groq_key.insert(0, self.settings.get("GROQ_API_KEY", ""))
        self.groq_key.pack(fill='x', **padding)

        ttk.Label(container, text="Together API Key:").pack(fill='x', **padding)
        self.together_key = ttk.Entry(container, show="*")
        self.together_key.insert(0, self.settings.get("TOGETHER_API_KEY", ""))
        self.together_key.pack(fill='x', **padding)

        ttk.Label(container, text="DeepInfra API Key:").pack(fill='x', **padding)
        self.deepinfra_key = ttk.Entry(container, show="*")
        self.deepinfra_key.insert(0, self.settings.get("DEEPINFRA_API_KEY", ""))
        self.deepinfra_key.pack(fill='x', **padding)

        ttk.Label(container, text="Cerebras API Key:").pack(fill='x', **padding)
        self.cerebras_key = ttk.Entry(container, show="*")
        self.cerebras_key.insert(0, self.settings.get("CEREBRAS_API_KEY", ""))
        self.cerebras_key.pack(fill='x', **padding)

        # Min Parameters
        ttk.Label(container, text="Minimum Parameters (Billions):").pack(fill='x', **padding)
        self.min_params = ttk.Spinbox(container, from_=1, to=1000)
        self.min_params.set(self.settings.get("MIN_PARAMETERS", 100))
        self.min_params.pack(fill='x', **padding)

        # Global Exclusions
        ttk.Label(container, text="Global Exclusions (comma separated):").pack(fill='x', **padding)
        self.exclusions = ttk.Entry(container)
        self.exclusions.insert(0, self.settings.get("GLOBAL_EXCLUSIONS", "-preview, -base, vision, dummy"))
        self.exclusions.pack(fill='x', **padding)

        # Config Path
        ttk.Label(container, text="LiteLLM Config Path:").pack(fill='x', **padding)
        self.config_path = ttk.Entry(container)
        self.config_path.insert(0, self.settings.get("CONFIG_PATH", "config.yaml"))
        self.config_path.pack(fill='x', **padding)

        # Interface URL
        ttk.Label(container, text="LLM Interface URL:").pack(fill='x', **padding)
        self.interface_url = ttk.Entry(container)
        self.interface_url.insert(0, self.settings.get("INTERFACE_URL", "http://localhost:4000"))
        self.interface_url.pack(fill='x', **padding)

        # Start with Windows
        self.start_with_windows_var = tk.BooleanVar(value=self.settings.get("START_WITH_WINDOWS", False))
        ttk.Checkbutton(container, text="Start with Windows", variable=self.start_with_windows_var).pack(fill='x', **padding)

        # Enable Notifications
        self.enable_notifications_var = tk.BooleanVar(value=self.settings.get("ENABLE_NOTIFICATIONS", True))
        ttk.Checkbutton(container, text="Enable Notifications", variable=self.enable_notifications_var).pack(fill='x', **padding)

        # Weights
        weights_frame = ttk.LabelFrame(container, text="Scoring Weights")
        weights_frame.pack(fill='x', **padding)

        ttk.Label(weights_frame, text="Size:").grid(row=0, column=0, **padding)
        self.size_weight = ttk.Spinbox(weights_frame, from_=0, to=1, increment=0.1, width=5)
        self.size_weight.set(self.settings.get("SIZE_WEIGHT", 0.6))
        self.size_weight.grid(row=0, column=1, **padding)

        ttk.Label(weights_frame, text="Context:").grid(row=0, column=2, **padding)
        self.context_weight = ttk.Spinbox(weights_frame, from_=0, to=1, increment=0.1, width=5)
        self.context_weight.set(self.settings.get("CONTEXT_WEIGHT", 0.2))
        self.context_weight.grid(row=0, column=3, **padding)

        ttk.Label(weights_frame, text="Latency:").grid(row=0, column=4, **padding)
        self.latency_weight = ttk.Spinbox(weights_frame, from_=0, to=1, increment=0.1, width=5)
        self.latency_weight.set(self.settings.get("LATENCY_WEIGHT", 0.2))
        self.latency_weight.grid(row=0, column=5, **padding)

        # Save Button
        ttk.Button(container, text="Save Settings", command=self.save).pack(pady=20)

    def save(self):
        self.settings["OPENROUTER_API_KEY"] = self.or_key.get()
        self.settings["GROQ_API_KEY"] = self.groq_key.get()
        self.settings["TOGETHER_API_KEY"] = self.together_key.get()
        self.settings["DEEPINFRA_API_KEY"] = self.deepinfra_key.get()
        self.settings["CEREBRAS_API_KEY"] = self.cerebras_key.get()
        self.settings["GLOBAL_EXCLUSIONS"] = self.exclusions.get()
        self.settings["CONFIG_PATH"] = self.config_path.get()
        self.settings["INTERFACE_URL"] = self.interface_url.get()
        self.settings["START_WITH_WINDOWS"] = self.start_with_windows_var.get()
        self.settings["ENABLE_NOTIFICATIONS"] = self.enable_notifications_var.get()
        self.settings["SIZE_WEIGHT"] = float(self.size_weight.get())
        self.settings["CONTEXT_WEIGHT"] = float(self.context_weight.get())
        self.settings["LATENCY_WEIGHT"] = float(self.latency_weight.get())
        try:
            self.settings["MIN_PARAMETERS"] = int(self.min_params.get())
        except ValueError:
            messagebox.showerror("Error", "Invalid parameter value")
            return

        save_settings(self.settings)
        messagebox.showinfo("Success", "Settings saved successfully!")

        if self.on_save_callback:
            self.on_save_callback(self.settings)

        self.root.destroy()

    def run(self):
        self.root.mainloop()

if __name__ == "__main__":
    ui = SettingsUI()
    ui.run()
