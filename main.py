import asyncio
import threading
import time
import datetime
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
import dashboard_ui
import query_ui
import status_window

class LiteLLMControlPanel:
    def __init__(self):
        self.settings = settings_ui.load_settings()
        self.last_benchmark_time = None
        self.process_mgr = process_manager.LiteLLMProcess(config_path=self.settings.get("CONFIG_PATH", "config.yaml"))
        api_keys = {
            "openrouter": self.settings.get("OPENROUTER_API_KEY", ""),
            "groq": self.settings.get("GROQ_API_KEY", ""),
            "together": self.settings.get("TOGETHER_API_KEY", ""),
            "deepinfra": self.settings.get("DEEPINFRA_API_KEY", ""),
            "cerebras": self.settings.get("CEREBRAS_API_KEY", "")
        }
        weights = {
            "size": float(self.settings.get("SIZE_WEIGHT", 0.6)),
            "context": float(self.settings.get("CONTEXT_WEIGHT", 0.2)),
            "latency": float(self.settings.get("LATENCY_WEIGHT", 0.2))
        }
        base_urls = {
            "openrouter": self.settings.get("OPENROUTER_BASE_URL", ""),
            "groq": self.settings.get("GROQ_BASE_URL", ""),
            "together": self.settings.get("TOGETHER_BASE_URL", ""),
            "deepinfra": self.settings.get("DEEPINFRA_BASE_URL", ""),
            "cerebras": self.settings.get("CEREBRAS_BASE_URL", ""),
            "ollama": self.settings.get("OLLAMA_BASE_URL", ""),
            "lm_studio": self.settings.get("LM_STUDIO_BASE_URL", "")
        }
        self.engine = engine.ModelEngine(api_keys=api_keys, weights=weights, base_urls=base_urls)
        # Apply exclusions from settings
        engine.GLOBAL_EXCLUSIONS = [x.strip() for x in self.settings.get("GLOBAL_EXCLUSIONS", "").split(",") if x.strip()]

        self.ranked_models: List[Dict[str, Any]] = []
        self.routing_enabled = True
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

    def notify(self, message, title="LiteLLM Router"):
        if not self.icon or not self.settings.get("ENABLE_NOTIFICATIONS", True):
            return
        self.icon.notify(message, title)

    def update_tray_status(self):
        if not self.icon:
            return

        color = 'gray'
        tooltip = "LiteLLM Router"

        if self.ranked_models:
            best = self.ranked_models[0]
            best_latency = best.get('latency', 1.0)
            if best_latency < 0.5:
                color = 'green'
            elif best_latency < 1.5:
                color = 'yellow'
            else:
                color = 'red'

            tooltip = f"Active: {best['id']} ({best['latency']:.2fs})"
        else:
            color = 'red'
            tooltip = "No models available"

        status = " (Running)" if self.process_mgr.is_running() else " (Stopped)"
        tooltip += status

        self.icon.icon = self.create_image(64, 64, color)
        self.icon.title = tooltip

    def on_quit(self, icon, item):
        self.running = False
        if self.settings.get("AUTO_MANAGE_LITELLM", True):
            self.process_mgr.stop()
        icon.stop()

    def toggle_routing(self, icon, item):
        self.routing_enabled = not self.routing_enabled
        print(f"Master Routing: {self.routing_enabled}")

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

    def maintenance_clear_skips(self, icon, item):
        database.clear_skip_list()
        self.notify("Skip list cleared.")

    def maintenance_clear_blacklist(self, icon, item):
        database.clear_blacklist()
        self.notify("Blacklist cleared.")

    def maintenance_reset_stats(self, icon, item):
        database.reset_all_stats()
        self.notify("All provider and model stats reset.")

    def toggle_provider(self, provider_name):
        def inner(icon, item):
            # Toggle is_free_provider status in DB
            conn = database.sqlite3.connect(database.DB_NAME)
            cursor = conn.cursor()
            cursor.execute("SELECT is_free_provider FROM providers WHERE provider_name = ?", (provider_name,))
            row = cursor.fetchone()
            new_status = 0 if row and row[0] else 1
            cursor.execute("UPDATE providers SET is_free_provider = ? WHERE provider_name = ?", (new_status, provider_name))
            conn.commit()
            conn.close()
            self.refresh_now(None, None)
        return inner

    def launch_interface(self, icon=None, item=None):
        import webbrowser
        url = self.settings.get("INTERFACE_URL", "http://localhost:4000")
        webbrowser.open(url)

    def launch_litellm(self, icon, item):
        self.process_mgr.start()

    def stop_litellm(self, icon, item):
        self.process_mgr.stop()

    def restart_litellm(self, icon, item):
        self.process_mgr.restart()

    def show_logs(self, icon, item):
        def run_logs():
            viewer = log_viewer.LogViewer(self.process_mgr)
            viewer.run()
        threading.Thread(target=run_logs, daemon=True).start()

    def show_dashboard(self, icon=None, item=None):
        def run_dashboard():
            ui = dashboard_ui.DashboardUI(self)
            ui.run()
        threading.Thread(target=run_dashboard, daemon=True).start()

    def show_query(self, icon=None, item=None):
        def run_query():
            ui = query_ui.QueryUI(self.settings)
            ui.run()
        threading.Thread(target=run_query, daemon=True).start()

    def show_status(self, icon=None, item=None):
        def run_status():
            ui = status_window.StatusWindow(self)
            ui.run()
        threading.Thread(target=run_status, daemon=True).start()

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
        self.engine.base_urls = {
            "openrouter": self.settings.get("OPENROUTER_BASE_URL", ""),
            "groq": self.settings.get("GROQ_BASE_URL", ""),
            "together": self.settings.get("TOGETHER_BASE_URL", ""),
            "deepinfra": self.settings.get("DEEPINFRA_BASE_URL", ""),
            "cerebras": self.settings.get("CEREBRAS_BASE_URL", ""),
            "ollama": self.settings.get("OLLAMA_BASE_URL", ""),
            "lm_studio": self.settings.get("LM_STUDIO_BASE_URL", "")
        }
        self.engine.weights = {
            "size": float(self.settings.get("SIZE_WEIGHT", 0.6)),
            "context": float(self.settings.get("CONTEXT_WEIGHT", 0.2)),
            "latency": float(self.settings.get("LATENCY_WEIGHT", 0.2))
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

    async def refresh_logic(self, auto_pilot=False):
        print("Refreshing model rankings...")
        self.ranked_models = await self.engine.get_ranked_models()
        self.last_benchmark_time = datetime.datetime.now()

        if self.routing_enabled and auto_pilot and self.ranked_models:
            best = self.ranked_models[0]
            # Check if current is different from best
            # For simplicity, we just notify every time for now or we could check a state
            config_manager.apply_model_to_litellm(best['id'], best['provider'])
            self.notify(f"Switched to {best['id']} ({best['provider']})", "Autonomous Model Switch")

        if self.icon:
            self.icon.menu = self.build_menu()
            self.update_tray_status()
        print("Refresh complete.")

    def build_menu(self):
        menu_items = []

        menu_items.append(item("Master Routing", self.toggle_routing, checked=lambda item: self.routing_enabled))
        menu_items.append(pystray.Menu.SEPARATOR)

        # 1. Primary Actions & Status
        is_running = self.process_mgr.is_running()
        status_text = "Running" if is_running else "Stopped"
        active_model_name = self.ranked_models[0]['id'] if self.ranked_models else "None"

        menu_items.append(item(f"LiteLLM: {status_text} | Active: {active_model_name}", lambda: None, enabled=False))
        menu_items.append(pystray.Menu.SEPARATOR)

        menu_items.append(item("Open LLM Interface", self.launch_interface))
        menu_items.append(item("Quick Query", self.show_query))
        menu_items.append(item("Show Dashboard", self.show_dashboard, default=True))
        menu_items.append(item("System Status", self.show_status))

        menu_items.append(pystray.Menu.SEPARATOR)

        # 2. LiteLLM Session Control
        control_items = [
            item("Start Proxy", self.launch_litellm, enabled=not is_running),
            item("Stop Proxy", self.stop_litellm, enabled=is_running),
            item("Restart Proxy", self.restart_litellm, enabled=is_running),
            item("View Logs", self.show_logs),
            item("View Config", self.view_config)
        ]
        menu_items.append(item("LiteLLM Control", pystray.Menu(*control_items)))

        # 3. Routing & Ranking
        ranking_items = []
        if self.ranked_models:
            for m in self.ranked_models:
                model_label = f"{m['id']} ({m['parameters']}B) - {m['latency']:.2f}s"
                submenu = pystray.Menu(
                    item("Switch to Model", self.select_model(m['id'], m['provider'])),
                    item("Skip (24h)", self.skip_model(m['id'])),
                    item("Blacklist", self.blacklist_model(m['id']))
                )
                ranking_items.append(item(model_label, submenu))
        else:
            ranking_items.append(item("Benchmarking models...", lambda: None, enabled=False))

        menu_items.append(item("Model Rankings", pystray.Menu(*ranking_items)))
        menu_items.append(item("Auto-Pilot Mode", self.toggle_auto_pilot, checked=lambda item: self.auto_pilot))
        menu_items.append(item("Refresh Now", self.refresh_now))

        menu_items.append(pystray.Menu.SEPARATOR)

        # 4. Providers & Maintenance
        provider_stats = database.get_provider_status()
        if provider_stats:
            toggle_items = []
            for name, is_free, empty_cycles, last_check in provider_stats:
                toggle_items.append(item(name, self.toggle_provider(name), checked=lambda item, ns=is_free: ns))
            menu_items.append(item("Enable Providers", pystray.Menu(*toggle_items)))

        menu_items.append(item("Settings", self.show_settings))

        startup_menu = pystray.Menu(
            item("Enable", self.enable_startup),
            item("Disable", self.disable_startup)
        )
        menu_items.append(item("Start with Windows", startup_menu))

        maintenance_menu = pystray.Menu(
            item("Clear Skip List", self.maintenance_clear_skips),
            item("Clear Blacklist", self.maintenance_clear_blacklist),
            item("Reset Provider Stats", self.maintenance_reset_stats)
        )
        menu_items.append(item("Maintenance", maintenance_menu))

        menu_items.append(pystray.Menu.SEPARATOR)
        menu_items.append(item("Quit", self.on_quit))

        return pystray.Menu(*menu_items)

    async def monitor_active_model(self):
        """Monitors the active LiteLLM model health and triggers fallback if needed."""
        consecutive_failures = 0
        while self.running:
            if self.process_mgr.is_running():
                if not self.process_mgr.check_health():
                    consecutive_failures += 1
                    print(f"LiteLLM health check failed ({consecutive_failures}/3)")
                else:
                    consecutive_failures = 0

                if consecutive_failures >= 3:
                    print("Active model seems unhealthy, triggering refresh/fallback...")
                    self.notify("LiteLLM health check failed multiple times. Triggering fallback...", "Health Alert")
                    await self.refresh_logic(auto_pilot=self.auto_pilot)
                    consecutive_failures = 0
            await asyncio.sleep(60)

    async def background_worker(self):
        """Background loop for periodic model benchmarking."""
        # Start health monitor
        asyncio.create_task(self.monitor_active_model())

        while self.running:
            try:
                await self.refresh_logic(auto_pilot=self.auto_pilot)

                # Update menu and icon
                if self.icon:
                    self.icon.menu = self.build_menu()
                    self.update_tray_status()

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
        try:
            self.loop.run_until_complete(self.background_worker())
        except Exception as e:
            print(f"Critical error in async loop: {e}")
            # Notify user of crash
            self.notify(f"The background worker has crashed: {e}. Please restart the application.", "Critical Error")

    def run(self):
        # Start LiteLLM if configured
        if self.settings.get("AUTO_MANAGE_LITELLM", True):
            self.process_mgr.start()

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
