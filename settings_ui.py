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
        "MIN_PARAMETERS": 100,
        "AUTO_PILOT": False
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
        padding = {'padx': 10, 'pady': 5}

        # API Keys
        ttk.Label(self.root, text="OpenRouter API Key:").pack(fill='x', **padding)
        self.or_key = ttk.Entry(self.root, show="*")
        self.or_key.insert(0, self.settings.get("OPENROUTER_API_KEY", ""))
        self.or_key.pack(fill='x', **padding)

        ttk.Label(self.root, text="Groq API Key:").pack(fill='x', **padding)
        self.groq_key = ttk.Entry(self.root, show="*")
        self.groq_key.insert(0, self.settings.get("GROQ_API_KEY", ""))
        self.groq_key.pack(fill='x', **padding)

        ttk.Label(self.root, text="Together API Key:").pack(fill='x', **padding)
        self.together_key = ttk.Entry(self.root, show="*")
        self.together_key.insert(0, self.settings.get("TOGETHER_API_KEY", ""))
        self.together_key.pack(fill='x', **padding)

        # Min Parameters
        ttk.Label(self.root, text="Minimum Parameters (Billions):").pack(fill='x', **padding)
        self.min_params = ttk.Spinbox(self.root, from_=1, to=1000)
        self.min_params.set(self.settings.get("MIN_PARAMETERS", 100))
        self.min_params.pack(fill='x', **padding)

        # Save Button
        ttk.Button(self.root, text="Save Settings", command=self.save).pack(pady=20)

    def save(self):
        self.settings["OPENROUTER_API_KEY"] = self.or_key.get()
        self.settings["GROQ_API_KEY"] = self.groq_key.get()
        self.settings["TOGETHER_API_KEY"] = self.together_key.get()
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
