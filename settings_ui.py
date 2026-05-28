import tkinter as tk
from tkinter import ttk, messagebox
import json
import os
import known_models

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
    def __init__(self, on_save_callback=None, on_proxy_logs_callback=None, on_engine_logs_callback=None):
        self.on_save_callback = on_save_callback
        self.on_proxy_logs_callback = on_proxy_logs_callback
        self.on_engine_logs_callback = on_engine_logs_callback
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
        self.ollama_url.grid(row=8, column=1, columnspan=3, sticky='ew', **padding)

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

        # Known Good Models Management
        km_frame = ttk.LabelFrame(container, text="Known Good Models Override")
        km_frame.pack(fill='x', **padding)

        ttk.Label(km_frame, text="Models with guaranteed specs (override missing metadata):",
                  font=('Helvetica', 9)).pack(fill='x', padx=5, pady=2)

        km_cols = ('id', 'params', 'ctx', 'provider')
        self.km_tree = ttk.Treeview(km_frame, columns=km_cols, show='headings', height=8)
        self.km_tree.heading('id', text='Model ID')
        self.km_tree.heading('params', text='Params(B)')
        self.km_tree.heading('ctx', text='Context')
        self.km_tree.heading('provider', text='Provider')
        self.km_tree.column('id', width=280)
        self.km_tree.column('params', width=70)
        self.km_tree.column('ctx', width=80)
        self.km_tree.column('provider', width=80)
        self.km_tree.pack(fill='both', padx=5, pady=2)

        km_scroll = ttk.Scrollbar(km_frame, orient='vertical', command=self.km_tree.yview)
        self.km_tree.configure(yscrollcommand=km_scroll.set)
        km_scroll.place(relx=1, rely=0, relheight=1, anchor='ne')

        self._populate_known_models()

        km_btn_frame = ttk.Frame(km_frame)
        km_btn_frame.pack(fill='x', padx=5, pady=2)

        ttk.Button(km_btn_frame, text="Add Model", command=self.km_add).pack(side='left', padx=2)
        ttk.Button(km_btn_frame, text="Edit Selected", command=self.km_edit).pack(side='left', padx=2)
        ttk.Button(km_btn_frame, text="Remove Selected", command=self.km_remove).pack(side='left', padx=2)
        ttk.Button(km_btn_frame, text="Reset Defaults", command=self.km_reset).pack(side='left', padx=2)

        # Live Logs Shortcut
        logs_frame = ttk.LabelFrame(container, text="Live Monitoring")
        logs_frame.pack(fill='x', **padding)

        btn_row = ttk.Frame(logs_frame)
        btn_row.pack(fill='x', padx=5, pady=5)

        def safe_proxy():
            if self.on_proxy_logs_callback: self.on_proxy_logs_callback()
            else: messagebox.showinfo("Info", "Log viewer unavailable in standalone mode.")

        def safe_engine():
            if self.on_engine_logs_callback: self.on_engine_logs_callback()
            else: messagebox.showinfo("Info", "Log viewer unavailable in standalone mode.")

        ttk.Button(btn_row, text="Open Proxy Logs",
                   command=safe_proxy).pack(side='left', padx=5)
        ttk.Button(btn_row, text="Open Engine Logs",
                   command=safe_engine).pack(side='left', padx=5)

        # API Server Settings
        api_frame = ttk.LabelFrame(container, text="External API")
        api_frame.pack(fill='x', **padding)

        self.enable_api_var = tk.BooleanVar(value=self.settings.get("ENABLE_API", False))
        ttk.Checkbutton(api_frame, text="Enable External API Server",
                        variable=self.enable_api_var).pack(fill='x', **padding)

        port_row = ttk.Frame(api_frame)
        port_row.pack(fill='x', padx=5, pady=2)
        ttk.Label(port_row, text="API Port:").pack(side='left', padx=5)
        self.api_port = ttk.Entry(port_row, width=10)
        self.api_port.insert(0, str(self.settings.get("API_PORT", 8000)))
        self.api_port.pack(side='left', padx=5)

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
        self.settings["ENABLE_API"] = self.enable_api_var.get()
        try:
            self.settings["API_PORT"] = int(self.api_port.get())
        except ValueError:
            messagebox.showerror("Error", "Invalid API Port. Reverting to default 8000.")
            self.settings["API_PORT"] = 8000
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

    def _populate_known_models(self):
        for i in self.km_tree.get_children():
            self.km_tree.delete(i)
        for litellm_id, spec in sorted(known_models.all_models().items()):
            self.km_tree.insert('', 'end', values=(
                litellm_id,
                spec.get('params', 0),
                spec.get('ctx', 0),
                spec.get('provider', ''),
            ))

    def km_add(self):
        win = tk.Toplevel(self.root)
        win.title('Add Known Model')
        win.geometry('400x200')
        win.resizable(False, False)
        ttk.Label(win, text='LiteLLM Model ID:').grid(row=0, column=0, padx=10, pady=5, sticky='w')
        id_entry = ttk.Entry(win, width=40)
        id_entry.grid(row=0, column=1, padx=10, pady=5)
        ttk.Label(win, text='Parameters (B):').grid(row=1, column=0, padx=10, pady=5, sticky='w')
        params_entry = ttk.Entry(win, width=15)
        params_entry.grid(row=1, column=1, padx=10, pady=5, sticky='w')
        ttk.Label(win, text='Context Length:').grid(row=2, column=0, padx=10, pady=5, sticky='w')
        ctx_entry = ttk.Entry(win, width=15)
        ctx_entry.grid(row=2, column=1, padx=10, pady=5, sticky='w')
        ttk.Label(win, text='Provider:').grid(row=3, column=0, padx=10, pady=5, sticky='w')
        prov_entry = ttk.Entry(win, width=20)
        prov_entry.grid(row=3, column=1, padx=10, pady=5, sticky='w')

        def do_add():
            mid = id_entry.get().strip()
            if not mid:
                messagebox.showwarning('Warning', 'Model ID is required.', parent=win)
                return
            try:
                p = int(params_entry.get())
                c = int(ctx_entry.get())
            except ValueError:
                messagebox.showerror('Error', 'Parameters and Context must be integers.', parent=win)
                return
            prov = prov_entry.get().strip()
            known_models.add_model(mid, p, c, prov)
            self._populate_known_models()
            win.destroy()

        ttk.Button(win, text='Add', command=do_add).grid(row=4, column=0, columnspan=2, pady=15)

    def km_edit(self):
        sel = self.km_tree.selection()
        if not sel:
            messagebox.showinfo('Info', 'Select a model to edit.', parent=self.root)
            return
        vals = self.km_tree.item(sel[0])['values']
        if not vals:
            return
        old_id = str(vals[0])

        win = tk.Toplevel(self.root)
        win.title('Edit Known Model')
        win.geometry('400x200')
        win.resizable(False, False)
        ttk.Label(win, text='LiteLLM Model ID:').grid(row=0, column=0, padx=10, pady=5, sticky='w')
        id_entry = ttk.Entry(win, width=40)
        id_entry.insert(0, old_id)
        id_entry.config(state='readonly')
        id_entry.grid(row=0, column=1, padx=10, pady=5)
        ttk.Label(win, text='Parameters (B):').grid(row=1, column=0, padx=10, pady=5, sticky='w')
        params_entry = ttk.Entry(win, width=15)
        params_entry.insert(0, str(vals[1]))
        params_entry.grid(row=1, column=1, padx=10, pady=5, sticky='w')
        ttk.Label(win, text='Context Length:').grid(row=2, column=0, padx=10, pady=5, sticky='w')
        ctx_entry = ttk.Entry(win, width=15)
        ctx_entry.insert(0, str(vals[2]))
        ctx_entry.grid(row=2, column=1, padx=10, pady=5, sticky='w')
        ttk.Label(win, text='Provider:').grid(row=3, column=0, padx=10, pady=5, sticky='w')
        prov_entry = ttk.Entry(win, width=20)
        prov_entry.insert(0, str(vals[3]))
        prov_entry.grid(row=3, column=1, padx=10, pady=5, sticky='w')

        def do_save():
            try:
                p = int(params_entry.get())
                c = int(ctx_entry.get())
            except ValueError:
                messagebox.showerror('Error', 'Parameters and Context must be integers.', parent=win)
                return
            prov = prov_entry.get().strip()
            known_models.add_model(old_id, p, c, prov)
            self._populate_known_models()
            win.destroy()

        ttk.Button(win, text='Save', command=do_save).grid(row=4, column=0, columnspan=2, pady=15)

    def km_remove(self):
        sel = self.km_tree.selection()
        if not sel:
            messagebox.showinfo('Info', 'Select a model to remove.', parent=self.root)
            return
        vals = self.km_tree.item(sel[0])['values']
        if not vals:
            return
        model_id = str(vals[0])
        if messagebox.askyesno('Confirm', f"Remove '{model_id}' from known models?", parent=self.root):
            known_models.remove_model(model_id)
            self._populate_known_models()

    def km_reset(self):
        if messagebox.askyesno('Confirm', 'Reset all known models to defaults?', parent=self.root):
            import importlib
            importlib.reload(known_models)
            self._populate_known_models()
            messagebox.showinfo('Done', 'Known models reset to defaults.', parent=self.root)

    def run(self):
        self.root.mainloop()

if __name__ == "__main__":
    ui = SettingsUI()
    ui.run()
