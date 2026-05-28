import tkinter as tk
from tkinter import scrolledtext
import threading

class LogViewer:
    def __init__(self, process_mgr):
        self.process_mgr = process_mgr
        self.root = tk.Tk()
        self.root.title("LiteLLM Process Logs")
        self.root.geometry("800x600")

        self.log_area = scrolledtext.ScrolledText(self.root, wrap=tk.WORD)
        self.log_area.pack(fill='both', expand=True)

        self.running = True
        self.root.protocol("WM_DELETE_WINDOW", self.on_close)

        # Start log polling
        threading.Thread(target=self.poll_logs, daemon=True).start()

    def on_close(self):
        self.running = False
        self.root.destroy()

    def poll_logs(self):
        while self.running:
            if self.process_mgr.process and self.process_mgr.process.stdout:
                line = self.process_mgr.process.stdout.readline()
                if line:
                    self.root.after(0, self.append_log, line)
            else:
                import time
                time.sleep(1)

    def append_log(self, message):
        self.log_area.insert(tk.END, message)
        self.log_area.see(tk.END)

    def run(self):
        self.root.mainloop()
