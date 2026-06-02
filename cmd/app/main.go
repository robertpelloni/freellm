package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/getlantern/systray"
	"github.com/robertpelloni/litellm_control_panel/internal/db"
	"github.com/robertpelloni/litellm_control_panel/internal/engine"
	"github.com/robertpelloni/litellm_control_panel/internal/proxy"
	"github.com/robertpelloni/litellm_control_panel/internal/ui"
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetTitle("LiteLLM Control Panel")
	systray.SetTooltip("LiteLLM Control Panel (Go)")

	mOpen := systray.AddMenuItem("Open LLM Interface", "Open the interface in browser")
	mSettings := systray.AddMenuItem("Settings", "Change settings")
	systray.AddSeparator()
	mRefresh := systray.AddMenuItem("Refresh Now", "Run benchmarks immediately")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit the application")

	// Initialize Database
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	// Initialize Engine
	apiKeys := map[string]string{
		"openrouter": os.Getenv("OPENROUTER_API_KEY"),
		"groq":       os.Getenv("GROQ_API_KEY"),
		"github":     os.Getenv("GITHUB_TOKEN"),
	}
	benchmarker := engine.NewBenchmarker(apiKeys, 100)

	// Initialize Proxy Gateway
	gateway := proxy.NewGateway(10, database) // Max 10 active requests
	go func() {
		log.Println("Starting LiteLLM Proxy on :4000")
		if err := http.ListenAndServe(":4000", gateway); err != nil {
			log.Printf("Proxy failed: %v", err)
		}
	}()

	// Initialize Web Dashboard
	uiServer := ui.NewUIServer()
	go func() {
		log.Println("Starting Web Dashboard on :8080")
		if err := uiServer.Start(":8080"); err != nil {
			log.Printf("UI Server failed: %v", err)
		}
	}()

	// Background worker for benchmarking
	go func() {
		for {
			log.Println("Continuous Model Discovery & Benchmarking...")
			ctx := context.Background()

			// 1. Discover candidates from all enabled providers
			candidates := benchmarker.FetchModels(ctx)
			log.Printf("Discovered %d model candidates", len(candidates))

			// 2. Run TTFT benchmarks and rank
			ranked := benchmarker.RunBenchmark(ctx, candidates)
			gateway.UpdateModels(ranked)
			uiServer.UpdateModels(ranked)

			topModel := "none"
			if len(ranked) > 0 {
				topModel = ranked[0].ID
			}

			db.LogActivity(database, "Sync Complete", topModel, fmt.Sprintf("Ranked %d models", len(ranked)))

			time.Sleep(1 * time.Hour)
		}
	}()

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				log.Println("Opening LLM Interface...")
				// open.Run("http://localhost:4000")
			case <-mSettings.ClickedCh:
				log.Println("Opening Settings...")
			case <-mRefresh.ClickedCh:
				log.Println("Refreshing...")
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	// Cleanup
}
