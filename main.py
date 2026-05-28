import asyncio
import threading
import time
from typing import List, Dict, Any
from PIL import Image, ImageDraw
import pystray
from pystray import MenuItem as item

import database
import engine
import config_manager
import settings_ui
import process_manager
import log_viewer

class LiteLLMControlPanel:
    def __init__(self):
        self.settings = settings_ui.load_settings()
        self.process_mgr = process_manager.LiteLLMProcess(config_path=self.settings.get("CONFIG_PATH", "config.yaml"))
        api_keys = {
            "openrouter": self.settings.get("OPENROUTER_API_KEY", ""),
            "groq": self.settings.get("GROQ_API_KEY", ""),
            "together": self.settings.get("TOGETHER_API_KEY", ""),
            "deepinfra": self.settings.get("DEEPINFRA_API_KEY", ""),
            "cerebras": self.settings.get("CEREBRAS_API_KEY", "")
        }
        self.engine = engine.ModelEngine(api_keys=api_keys)
        # Apply exclusions from settings
        engine.GLOBAL_EXCLUSIONS = [x.strip() for x in self.settings.get("GLOBAL_EXCLUSIONS", "").split(",") if x.strip()]

        self.ranked_models: List[Dict[str, Any]] = []
        self.auto_pilot = self.settings.get("AUTO_PILOT", False)
        self.running = True
        self.icon = None
        self.loop = asyncio.new_event_loop()

        # Initialize DB
        database.init_db()

    def create_image(self, width, height, color):
        # Generate a simple icon with a specific color
        image = Image.new('RGB', (width, height), 'black')
        dc = ImageDraw.Draw(image)
        # Draw a colored circle in the middle
        margin = 10
        dc.ellipse([margin, margin, width-margin, height-margin], fill=color)
        return image

    def update_icon_color(self):
        if not self.icon:
            return

        color = 'gray'
        if self.ranked_models:
            best_latency = self.ranked_models[0].get('latency', 1.0)
            if best_latency < 0.5:
                color = 'green'
            elif best_latency < 1.5:
                color = 'yellow'
            else:
                color = 'red'
        else:
            color = 'red'

        self.icon.icon = self.create_image(64, 64, color)

    def on_quit(self, icon, item):
        self.running = False
        icon.stop()

    def toggle_auto_pilot(self, icon, item):
        self.auto_pilot = not self.auto_pilot
        self.settings["AUTO_PILOT"] = self.auto_pilot
        settings_ui.save_settings(self.settings)
        print(f"Auto-Pilot: {self.auto_pilot}")

    def toggle_startup(self, icon, item):
        import startup
        # We don't have a reliable way to check if it's already in startup from here without reading registry
        # For simplicity, we just toggle based on what we think, but it's better to just provide a "Enable" / "Disable"
        pass

    def enable_startup(self, icon, item):
        import startup
        startup.add_to_startup()

    def disable_startup(self, icon, item):
        import startup
        startup.remove_from_startup()

    def launch_litellm(self, icon, item):
        self.process_mgr.start()

    def stop_litellm(self, icon, item):
        self.process_mgr.stop()

    def show_logs(self, icon, item):
        def run_logs():
            viewer = log_viewer.LogViewer(self.process_mgr)
            viewer.run()
        threading.Thread(target=run_logs, daemon=True).start()

    def view_config(self, icon, item):
        import os
        config_path = self.settings.get("CONFIG_PATH", "config.yaml")
        if os.path.exists(config_path):
            if os.name == 'nt':
                os.startfile(config_path)
            else:
                import subprocess
                subprocess.call(['open', config_path])

    def show_settings(self, icon, item):
        def run_ui():
            ui = settings_ui.SettingsUI(on_save_callback=self.on_settings_saved)
            ui.run()
        # Run in a separate thread to not block the icon
        threading.Thread(target=run_ui, daemon=True).start()

    def on_settings_saved(self, new_settings):
        self.settings = new_settings
        self.process_mgr.config_path = self.settings.get("CONFIG_PATH", "config.yaml")

        # Apply startup setting
        import startup
        if self.settings.get("START_WITH_WINDOWS", False):
            startup.add_to_startup()
        else:
            startup.remove_from_startup()

        self.engine.api_keys = {
            "openrouter": self.settings.get("OPENROUTER_API_KEY", ""),
            "groq": self.settings.get("GROQ_API_KEY", ""),
            "together": self.settings.get("TOGETHER_API_KEY", ""),
            "deepinfra": self.settings.get("DEEPINFRA_API_KEY", ""),
            "cerebras": self.settings.get("CEREBRAS_API_KEY", "")
        }
        self.engine.min_params = int(self.settings.get("MIN_PARAMETERS", 100))
        engine.GLOBAL_EXCLUSIONS = [x.strip() for x in self.settings.get("GLOBAL_EXCLUSIONS", "").split(",") if x.strip()]
        # Force a refresh
        asyncio.run_coroutine_threadsafe(self.refresh_logic(), self.loop)

    def select_model(self, model_id, provider):
        def inner(icon, item):
            config_manager.apply_model_to_litellm(model_id, provider)
        return inner

    def skip_model(self, model_id):
        def inner(icon, item):
            database.set_model_skip_status(model_id, True, duration_hours=24)
            asyncio.run_coroutine_threadsafe(self.refresh_logic(), self.loop)
        return inner

    def blacklist_model(self, model_id):
        def inner(icon, item):
            database.set_model_blacklist_status(model_id, True)
            asyncio.run_coroutine_threadsafe(self.refresh_logic(), self.loop)
        return inner

    def refresh_now(self, icon, item):
        asyncio.run_coroutine_threadsafe(self.refresh_logic(), self.loop)

    async def refresh_logic(self):
        print("Manual refresh triggered...")
        self.ranked_models = await self.engine.get_ranked_models()
        if self.icon:
            self.icon.menu = self.build_menu()
            self.update_icon_color()
        print("Manual refresh complete.")

    def build_menu(self):
        menu_items = []

        # Top 10 models
        if self.ranked_models:
            menu_items.append(item("Top Models:", lambda: None, enabled=False))
            for m in self.ranked_models:
                model_label = f"{m['id']} ({m['parameters']}B) - {m['latency']:.2f}s"

                # Submenu for each model
                submenu = pystray.Menu(
                    item("Switch to Model", self.select_model(m['id'], m['provider'])),
                    item("Skip (24h)", self.skip_model(m['id'])),
                    item("Blacklist", self.blacklist_model(m['id']))
                )

                menu_items.append(item(model_label, submenu))
        else:
            menu_items.append(item("Discovering models...", lambda: None, enabled=False))

        menu_items.append(pystray.Menu.SEPARATOR)
        menu_items.append(item("Auto-Pilot Mode", self.toggle_auto_pilot, checked=lambda item: self.auto_pilot))
        menu_items.append(item("Refresh Now", self.refresh_now))

        status_text = "Status: Running" if self.process_mgr.is_running() else "Status: Stopped"
        instance_menu = pystray.Menu(
            item(status_text, lambda: None, enabled=False),
            item("Launch", self.launch_litellm, enabled=lambda item: not self.process_mgr.is_running()),
            item("Stop", self.stop_litellm, enabled=lambda item: self.process_mgr.is_running()),
            item("View Logs", self.show_logs),
            item("View Config", self.view_config)
        )
        menu_items.append(item("LiteLLM Instance", instance_menu))

        menu_items.append(item("Settings", self.show_settings))

        startup_menu = pystray.Menu(
            item("Enable", self.enable_startup),
            item("Disable", self.disable_startup)
        )
        menu_items.append(item("Start with Windows", startup_menu))

        menu_items.append(pystray.Menu.SEPARATOR)
        menu_items.append(item("Quit", self.on_quit))

        return pystray.Menu(*menu_items)

    async def background_worker(self):
        """Background loop for periodic model benchmarking."""
        while self.running:
            try:
                print("Refreshing model rankings...")
                self.ranked_models = await self.engine.get_ranked_models()

                if self.auto_pilot and self.ranked_models:
                    best = self.ranked_models[0]
                    config_manager.apply_model_to_litellm(best['id'], best['provider'])

                # Update menu and icon
                if self.icon:
                    self.icon.menu = self.build_menu()
                    self.update_icon_color()

                print("Rankings updated.")
            except Exception as e:
                print(f"Error in background worker: {e}")

            # Wait for 1 hour (or as configured)
            for _ in range(3600):
                if not self.running:
                    break
                await asyncio.sleep(1)

    def run_async_loop(self):
        asyncio.set_event_loop(self.loop)
        self.loop.run_until_complete(self.background_worker())

    def run(self):
        # Start background thread
        threading.Thread(target=self.run_async_loop, daemon=True).start()

        # Start tray icon
        self.icon = pystray.Icon(
            "LiteLLM",
            self.create_image(64, 64, 'gray'),
            "LiteLLM Router",
            menu=self.build_menu()
        )
        self.icon.run()

if __name__ == "__main__":
    app = LiteLLMControlPanel()
    app.run()
