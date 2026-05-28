import tkinter as tk
from tkinter import ttk, messagebox
import json
import os

SETTINGS_FILE = "settings.json"

# Map: settings key -> environment variable name
ENV_KEY_MAP = {
    "OPENROUTER_API_KEY": "OPENROUTER_API_KEY",
    "GROQ_API_KEY": "GROQ_API_KEY",
    "TOGETHER_API_KEY": "TOGETHER_API_KEY",
    "DEEPINFRA_API_KEY": "DEEPINFRA_API_KEY",
    "CEREBRAS_API_KEY": "CEREBRAS_API_KEY",
    "GITHUB_API_KEY": "GITHUB_TOKEN",
    "HUGGINGFACE_API_KEY": "HUGGINGFACE_API_KEY",
    "NVIDIA_API_KEY": "NVIDIA_NIM_API_KEY",
}

def load_settings():
    if os.path.exists(SETTINGS_FILE):
        try:
            with open(SETTINGS_FILE, 'r') as f:
                return json.load(f)
        except:
            pass
    return {
        "OPENROUTER_API_KEY": os.environ.get("OPENROUTER_API_KEY", ""),
        "GROQ_API_KEY": os.environ.get("GROQ_API_KEY", ""),
        "TOGETHER_API_KEY": os.environ.get("TOGETHER_API_KEY", ""),
        "DEEPINFRA_API_KEY": os.environ.get("DEEPINFRA_API_KEY", ""),
        "CEREBRAS_API_KEY": os.environ.get("CEREBRAS_API_KEY", ""),
        "GITHUB_API_KEY": os.environ.get("GITHUB_TOKEN", ""),
        "HUGGINGFACE_API_KEY": os.environ.get("HUGGINGFACE_API_KEY", ""),
        "NVIDIA_API_KEY": os.environ.get("NVIDIA_NIM_API_KEY", ""),
        "OPENROUTER_BASE_URL": "https://openrouter.ai/api/v1",
        "GROQ_BASE_URL": "https://api.groq.com/openai/v1",
        "TOGETHER_BASE_URL": "https://api.together.xyz",
        "DEEPINFRA_BASE_URL": "https://api.deepinfra.com/v1/openai",
        "CEREBRAS_BASE_URL": "https://api.cerebras.ai/v1",
        "GITHUB_BASE_URL": "https://models.inference.ai.azure.com",
        "HUGGINGFACE_BASE_URL": "https://api-inference.huggingface.co",
        "NVIDIA_BASE_URL": "https://integrate.api.nvidia.com/v1",
        "OLLAMA_BASE_URL": "http://localhost:11434",
        "LM_STUDIO_BASE_URL": "http://localhost:1234",
        "MIN_PARAMETERS": 100,
        "AUTO_PILOT": False,
        "GLOBAL_EXCLUSIONS": "-base, vision, dummy",
        "CONFIG_PATH": "C:/Users/hyper/.hermes/litellm-config.yaml",
        "INTERFACE_URL": "http://localhost:4000",
        "AUTO_MANAGE_LITELLM": True,
        "START_WITH_WINDOWS": False,
        "ROUTING_ENABLED": True,
        "ENABLE_NOTIFICATIONS": True,
    "PRIMARY_COUNT": 5,
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
        self.root.geometry("450x800")
        self.root.resizable(False, False)

        self.settings = load_settings()
        self.create_widgets()

    def create_widgets(self):
        padding = {'padx': 10, 'pady': 2}

        # Use a canvas and scrollbar for the long settings form
        canvas = tk.Canvas(self.root)
        scrollbar = ttk.Scrollbar(self.root, orient="vertical", command=canvas.yview)
        scrollable_frame = ttk.Frame(canvas)

        scrollable_frame.bind(
            "<Configure>",
            lambda e: canvas.configure(
                scrollregion=canvas.bbox("all")
            )
        )

        canvas.create_window((0, 0), window=scrollable_frame, anchor="nw")
        canvas.configure(yscrollcommand=scrollbar.set)

        container = scrollable_frame

        # API Keys and Base URLs
        keys_frame = ttk.LabelFrame(container, text="Provider Configuration")
        keys_frame.pack(fill='x', **padding)

        # Helper to add key + url
        def add_provider_fields(frame, name, key_pref, url_pref, row):
            ttk.Label(frame, text=f"{name} Key:").grid(row=row, column=0, sticky='w', **padding)
            ent_key = ttk.Entry(frame, show="*")
            ent_key.insert(0, self.settings.get(key_pref, ""))
            ent_key.grid(row=row, column=1, sticky='ew', **padding)

            ttk.Label(frame, text="URL:").grid(row=row, column=2, sticky='w', **padding)
            ent_url = ttk.Entry(frame)
            ent_url.insert(0, self.settings.get(url_pref, ""))
            ent_url.grid(row=row, column=3, sticky='ew', **padding)
            return ent_key, ent_url

        self.or_key, self.or_url = add_provider_fields(keys_frame, "OpenRouter", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", 0)
        self.groq_key, self.groq_url = add_provider_fields(keys_frame, "Groq", "GROQ_API_KEY", "GROQ_BASE_URL", 1)
        self.together_key, self.together_url = add_provider_fields(keys_frame, "Together", "TOGETHER_API_KEY", "TOGETHER_BASE_URL", 2)
        self.deepinfra_key, self.deepinfra_url = add_provider_fields(keys_frame, "DeepInfra", "DEEPINFRA_API_KEY", "DEEPINFRA_BASE_URL", 3)
        self.cerebras_key, self.cerebras_url = add_provider_fields(keys_frame, "Cerebras", "CEREBRAS_API_KEY", "CEREBRAS_BASE_URL", 4)
        self.github_key, self.github_url = add_provider_fields(keys_frame, "GitHub", "GITHUB_API_KEY", "GITHUB_BASE_URL", 5)
        self.hf_key, self.hf_url = add_provider_fields(keys_frame, "HuggingFace", "HUGGINGFACE_API_KEY", "HUGGINGFACE_BASE_URL", 6)
        self.nvidia_key, self.nvidia_url = add_provider_fields(keys_frame, "NVIDIA", "NVIDIA_API_KEY", "NVIDIA_BASE_URL", 7)

        # Local URLs
        ttk.Label(keys_frame, text="Ollama URL:").grid(row=8, column=0, sticky='w', **padding)
        self.ollama_url = ttk.Entry(keys_frame)
        self.ollama_url.insert(0, self.settings.get("OLLAMA_BASE_URL", "http://localhost:11434"))
        self.ollama_url.grid(row=5, column=1, columnspan=3, sticky='ew', **padding)

        ttk.Label(keys_frame, text="LM Studio URL:").grid(row=9, column=0, sticky='w', **padding)
        self.lms_url = ttk.Entry(keys_frame)
        self.lms_url.insert(0, self.settings.get("LM_STUDIO_BASE_URL", "http://localhost:1234"))
        self.lms_url.grid(row=9, column=1, columnspan=3, sticky='ew', **padding)

        keys_frame.columnconfigure(1, weight=1)
        keys_frame.columnconfigure(3, weight=1)

        # Min Parameters
        ttk.Label(container, text="Minimum Parameters (Billions):").pack(fill='x', **padding)
        self.min_params = ttk.Spinbox(container, from_=1, to=1000)
        self.min_params.set(self.settings.get("MIN_PARAMETERS", 100))
        self.min_params.pack(fill='x', **padding)

        # Global Exclusions
        ttk.Label(container, text="Global Exclusions (comma separated):").pack(fill='x', **padding)
        self.exclusions = ttk.Entry(container)
        self.exclusions.insert(0, self.settings.get("GLOBAL_EXCLUSIONS", "-base, vision, dummy"))
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

        # Lifecycle
        self.auto_manage_var = tk.BooleanVar(value=self.settings.get("AUTO_MANAGE_LITELLM", True))
        ttk.Checkbutton(container, text="Auto-Manage LiteLLM Proxy (Start/Stop with App)", variable=self.auto_manage_var).pack(fill='x', **padding)

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

        canvas.pack(side="left", fill="both", expand=True)
        scrollbar.pack(side="right", fill="y")

    def save(self):
        self.settings["OPENROUTER_API_KEY"] = self.or_key.get()
        self.settings["GROQ_API_KEY"] = self.groq_key.get()
        self.settings["TOGETHER_API_KEY"] = self.together_key.get()
        self.settings["DEEPINFRA_API_KEY"] = self.deepinfra_key.get()
        self.settings["CEREBRAS_API_KEY"] = self.cerebras_key.get()
        self.settings["GITHUB_API_KEY"] = self.github_key.get()
        self.settings["HUGGINGFACE_API_KEY"] = self.hf_key.get()
        self.settings["NVIDIA_API_KEY"] = self.nvidia_key.get()

        self.settings["OPENROUTER_BASE_URL"] = self.or_url.get()
        self.settings["GROQ_BASE_URL"] = self.groq_url.get()
        self.settings["TOGETHER_BASE_URL"] = self.together_url.get()
        self.settings["DEEPINFRA_BASE_URL"] = self.deepinfra_url.get()
        self.settings["CEREBRAS_BASE_URL"] = self.cerebras_url.get()
        self.settings["GITHUB_BASE_URL"] = self.github_url.get()
        self.settings["HUGGINGFACE_BASE_URL"] = self.hf_url.get()
        self.settings["NVIDIA_BASE_URL"] = self.nvidia_url.get()
        self.settings["OLLAMA_BASE_URL"] = self.ollama_url.get()
        self.settings["LM_STUDIO_BASE_URL"] = self.lms_url.get()
        self.settings["GLOBAL_EXCLUSIONS"] = self.exclusions.get()
        self.settings["CONFIG_PATH"] = self.config_path.get()
        self.settings["INTERFACE_URL"] = self.interface_url.get()
        self.settings["AUTO_MANAGE_LITELLM"] = self.auto_manage_var.get()
        self.settings["START_WITH_WINDOWS"] = self.start_with_windows_var.get()
        self.settings["ENABLE_NOTIFICATIONS"] = self.enable_notifications_var.get()
        self.settings["SIZE_WEIGHT"] = float(self.size_weight.get())
        self.settings["CONTEXT_WEIGHT"] = float(self.context_weight.get())
        self.settings["LATENCY_WEIGHT"] = float(self.latency_weight.get())
        try:
            self.settings["MIN_PARAMETERS"] = int(self.min_params.get())
            self.settings["PRIMARY_COUNT"] = int(self.primary_count.get())
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
