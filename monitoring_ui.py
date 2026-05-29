import tkinter as tk
from tkinter import ttk
import database

class MonitoringUI:
    def __init__(self, app_instance=None):
        self.app = app_instance
        self.root = tk.Tk()
        self.root.title("LiteLLM Control Panel - Autonomous Monitoring")
        self.root.geometry("1000x700")

        self.notebook = ttk.Notebook(self.root)
        self.notebook.pack(fill='both', expand=True)

        self.create_activity_tab()
        self.create_performance_tab()

        self.refresh_data()
        self.root.protocol("WM_DELETE_WINDOW", self.root.destroy)

    def create_activity_tab(self):
        tab = ttk.Frame(self.notebook)
        self.notebook.add(tab, text="Activity Log")

        columns = ('time', 'event', 'model', 'details')
        self.activity_tree = ttk.Treeview(tab, columns=columns, show='headings')
        self.activity_tree.heading('time', text='Timestamp')
        self.activity_tree.heading('event', text='Event Type')
        self.activity_tree.heading('model', text='Model ID')
        self.activity_tree.heading('details', text='Details')

        self.activity_tree.column('time', width=180)
        self.activity_tree.column('event', width=150)
        self.activity_tree.column('model', width=250)
        self.activity_tree.column('details', width=400)

        scrollbar = ttk.Scrollbar(tab, orient='vertical', command=self.activity_tree.yview)
        self.activity_tree.configure(yscrollcommand=scrollbar.set)

        self.activity_tree.pack(side='left', fill='both', expand=True, padx=5, pady=5)
        scrollbar.pack(side='right', fill='y', pady=5)

    def create_performance_tab(self):
        tab = ttk.Frame(self.notebook)
        self.notebook.add(tab, text="Performance Metrics")

        # Summary Frame
        summary_frame = ttk.LabelFrame(tab, text="24h System Summary", padding=10)
        summary_frame.pack(fill='x', padx=10, pady=10)

        self.probes_var = tk.StringVar(value="Total Probes: 0")
        self.ttft_var = tk.StringVar(value="Avg TTFT: 0.00s")
        self.success_var = tk.StringVar(value="Success Rate: 0.0%")

        ttk.Label(summary_frame, textvariable=self.probes_var, font=('Helvetica', 12)).grid(row=0, column=0, padx=20)
        ttk.Label(summary_frame, textvariable=self.ttft_var, font=('Helvetica', 12)).grid(row=0, column=1, padx=20)
        ttk.Label(summary_frame, textvariable=self.success_var, font=('Helvetica', 12)).grid(row=0, column=2, padx=20)

        # Provider Breakdown
        breakdown_frame = ttk.LabelFrame(tab, text="Provider Reliability", padding=10)
        breakdown_frame.pack(fill='both', expand=True, padx=10, pady=10)

        columns = ('provider', 'avg_lat', 'success')
        self.perf_tree = ttk.Treeview(breakdown_frame, columns=columns, show='headings')
        self.perf_tree.heading('provider', text='Provider')
        self.perf_tree.heading('avg_lat', text='Avg Latency (s)')
        self.perf_tree.heading('success', text='Success Rate (%)')

        self.perf_tree.pack(fill='both', expand=True)

    def refresh_data(self):
        # 1. Activity Log
        for i in self.activity_tree.get_children():
            self.activity_tree.delete(i)

        logs = database.get_recent_activity(limit=100)
        for ts, event, model, details in logs:
            self.activity_tree.insert('', 'end', values=(ts, event, model or "", details or ""))

        # 2. Performance Summary
        perf = database.get_performance_summary()
        self.probes_var.set(f"Total Probes: {perf['total_probes']}")
        self.ttft_var.set(f"Avg TTFT: {perf['avg_ttft']:.2f}s")
        self.success_var.set(f"Success Rate: {perf['success_rate']:.1f}%")

        for i in self.perf_tree.get_children():
            self.perf_tree.delete(i)

        for prov, lat, succ in perf['providers']:
            self.perf_tree.insert('', 'end', values=(prov, f"{lat:.3f}", f"{succ:.1f}%"))

        # Schedule next refresh
        self.root.after(10000, self.refresh_data)

    def run(self):
        self.root.mainloop()

if __name__ == "__main__":
    ui = MonitoringUI()
    ui.run()
