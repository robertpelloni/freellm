import tkinter as tk
from tkinter import ttk
import threading

class DashboardUI:
    def __init__(self, app_instance):
        self.app = app_instance
        self.root = tk.Tk()
        self.root.title("LiteLLM Control Panel - Dashboard")
        self.root.geometry("1000x600")

        self.create_widgets()
        self.running = True
        self.root.protocol("WM_DELETE_WINDOW", self.on_close)

        # Start periodic update
        self.update_loop()

    def on_close(self):
        self.running = False
        self.root.destroy()

    def create_widgets(self):
        # Header
        header = ttk.Frame(self.root, padding=10)
        header.pack(fill='x')
        ttk.Label(header, text="Model Performance & Rankings", font=('Helvetica', 14, 'bold')).pack(side='left')

        self.usage_label = ttk.Label(header, text="Usage: 0 queries (0 tokens)", font=('Helvetica', 10))
        self.usage_label.pack(side='right', padx=20)

        # Table
        columns = ('id', 'provider', 'parameters', 'latency', 'score', 'actions')
        self.tree = ttk.Treeview(self.root, columns=columns, show='headings')

        self.tree.heading('id', text='Model ID')
        self.tree.heading('provider', text='Provider')
        self.tree.heading('parameters', text='Params (B)')
        self.tree.heading('latency', text='Latency (s)')
        self.tree.heading('score', text='Score')
        self.tree.heading('actions', text='Status')

        self.tree.column('id', width=300)
        self.tree.column('provider', width=100)
        self.tree.column('parameters', width=100)
        self.tree.column('latency', width=100)
        self.tree.column('score', width=100)
        self.tree.column('actions', width=200)

        self.tree.pack(fill='both', expand=True, padx=10, pady=10)

        # Footer
        footer = ttk.Frame(self.root, padding=10)
        footer.pack(fill='x')
        ttk.Button(footer, text="Refresh Now", command=lambda: self.app.refresh_now(None, None)).pack(side='right')

        # Right-click menu for tree
        self.context_menu = tk.Menu(self.tree, tearoff=0)
        self.context_menu.add_command(label="Switch to Model", command=self.action_switch)
        self.context_menu.add_command(label="Skip (24h)", command=self.action_skip)
        self.context_menu.add_command(label="Blacklist", command=self.action_blacklist)

        self.tree.bind("<Button-3>", self.show_context_menu)

    def show_context_menu(self, event):
        item = self.tree.identify_row(event.y)
        if item:
            self.tree.selection_set(item)
            self.context_menu.post(event.x_root, event.y_root)

    def action_switch(self):
        selected = self.tree.item(self.tree.selection())['values']
        if selected:
            self.app.select_model(selected[0], selected[1])(None, None)

    def action_skip(self):
        selected = self.tree.item(self.tree.selection())['values']
        if selected:
            self.app.skip_model(selected[0])(None, None)

    def action_blacklist(self):
        selected = self.tree.item(self.tree.selection())['values']
        if selected:
            self.app.blacklist_model(selected[0])(None, None)

    def update_loop(self):
        if not self.running:
            return

        # Update usage stats
        queries, prompts, completions = database.get_total_usage()
        prompts = prompts or 0
        completions = completions or 0
        self.usage_label.config(text=f"Usage: {queries} queries ({prompts + completions} tokens)")

        # Update table content
        for i in self.tree.get_children():
            self.tree.delete(i)

        for m in self.app.ranked_models:
            self.tree.insert('', 'end', values=(
                m['id'],
                m['provider'],
                f"{m['parameters']}B",
                f"{m['latency']:.3f}",
                f"{m['score']:.2f}",
                "Top Ranked" if self.app.ranked_models.index(m) == 0 else ""
            ))

        self.root.after(5000, self.update_loop)

    def run(self):
        self.root.mainloop()
