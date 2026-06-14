package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"
)

// MessageRouter is an interface for routing messages through an LLM gateway.
// proxy.Gateway satisfies this interface via its RouteMessage method.
type MessageRouter interface {
	RouteMessage(ctx context.Context, message string, model string) (string, error)
}

// FreeLLMAgentExecutor implements a2asrv.AgentExecutor to route
// A2A tasks through a MessageRouter (e.g. the FreeLLM proxy gateway).
type FreeLLMAgentExecutor struct {
	router MessageRouter
	mu     sync.Mutex
}

// NewFreeLLMAgentExecutor creates an A2A executor backed by a MessageRouter.
func NewFreeLLMAgentExecutor(router MessageRouter) *FreeLLMAgentExecutor {
	return &FreeLLMAgentExecutor{
		router: router,
	}
}

// Execute implements a2asrv.AgentExecutor.
// It receives A2A task messages, routes them through FreeLLM as chat completions,
// and returns the LLM response as A2A artifacts.
func (e *FreeLLMAgentExecutor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	// Extract the user message from the request context
	var userMsg string
	if reqCtx.Message != nil {
		for _, part := range reqCtx.Message.Parts {
			if tp, ok := part.(*a2a.TextPart); ok {
				userMsg = tp.Text
			}
		}
	}

	if userMsg == "" {
		userMsg = "Hello"
	}

	taskID := reqCtx.TaskID
	contextID := reqCtx.ContextID

	log.Printf("[A2A] Task %s: routing message (%d chars) through FreeLLM", taskID, len(userMsg))

	// Mark task as working
	queue.Write(ctx, &a2a.TaskStatusUpdateEvent{
		TaskID:    taskID,
		ContextID: contextID,
		Status: a2a.TaskStatus{
			State: a2a.TaskStateWorking,
		},
	})

	// Route through the FreeLLM gateway
	resp, err := e.router.RouteMessage(ctx, userMsg, "free-llm")
	if err != nil {
		log.Printf("[A2A] Task %s failed: %v", taskID, err)
		queue.Write(ctx, &a2a.TaskStatusUpdateEvent{
			TaskID:    taskID,
			ContextID: contextID,
			Status: a2a.TaskStatus{
				State:   a2a.TaskStateFailed,
				Message: &a2a.Message{Role: "agent", Parts: []a2a.Part{&a2a.TextPart{Text: fmt.Sprintf("Error: %v", err)}}},
			},
			Final: true,
		})
		return err
	}

	// Create an artifact with the LLM response
	artifactID := a2a.NewArtifactID()
	artifact := a2a.Artifact{
		ID:   artifactID,
		Name: "llm-response",
		Parts: a2a.ContentParts{
			&a2a.TextPart{
				Text: resp,
			},
		},
	}

	// Publish the artifact
	queue.Write(ctx, &a2a.TaskArtifactUpdateEvent{
		TaskID:    taskID,
		ContextID: contextID,
		Artifact:  &artifact,
		LastChunk: true,
	})

	// Mark task as completed
	queue.Write(ctx, &a2a.TaskStatusUpdateEvent{
		TaskID:    taskID,
		ContextID: contextID,
		Status: a2a.TaskStatus{
			State:   a2a.TaskStateCompleted,
			Message: &a2a.Message{Role: "agent", Parts: []a2a.Part{&a2a.TextPart{Text: "Task completed successfully"}}},
		},
		Final: true,
	})

	log.Printf("[A2A] Task %s completed (%d chars response)", taskID, len(resp))
	return nil
}

// Cancel implements a2asrv.AgentExecutor.
func (e *FreeLLMAgentExecutor) Cancel(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	taskID := reqCtx.TaskID
	contextID := reqCtx.ContextID
	log.Printf("[A2A] Task %s: cancel requested", taskID)

	queue.Write(ctx, &a2a.TaskStatusUpdateEvent{
		TaskID:    taskID,
		ContextID: contextID,
		Status: a2a.TaskStatus{
			State:   a2a.TaskStateCanceled,
			Message: &a2a.Message{Role: "agent", Parts: []a2a.Part{&a2a.TextPart{Text: "Task canceled by user"}}},
		},
		Final: true,
	})
	return nil
}

// AgentCard returns the A2A Agent Card for this FreeLLM instance.
func AgentCard(baseURL string) a2a.AgentCard {
	return a2a.AgentCard{
		Name:            "FreeLLM Router Agent",
		Description:     "Routes LLM requests across free models from 30+ providers. Automatically selects the best available model based on live benchmarks. Supports OpenAI and Anthropic API formats.",
		URL:             baseURL + "/a2a",
		Version:         "1.0.0",
		ProtocolVersion: "0.3",
		Provider: &a2a.AgentProvider{
			Org: "FreeLLM",
			URL: "https://github.com/robertpelloni/freellm",
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming:              true,
			PushNotifications:     false,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{
			{
				ID:          "llm-chat",
				Name:        "LLM Chat Routing",
				Description: "Route chat messages through the best available free LLM model. Automatically handles provider failover, rate limiting, and model selection.",
				Tags:        []string{"llm", "chat", "routing", "free", "proxy"},
			},
			{
				ID:          "llm-code",
				Name:        "Code Generation",
				Description: "Generate, review, and debug code using the best available free coding models.",
				Tags:        []string{"code", "programming", "generation"},
			},
			{
				ID:          "llm-reasoning",
				Name:        "Reasoning and Analysis",
				Description: "Deep reasoning and analysis using the most capable available models.",
				Tags:        []string{"reasoning", "analysis", "logic"},
			},
			{
				ID:          "swarm-coordinate",
				Name:        "Swarm Coordination",
				Description: "Coordinate tasks across a swarm of A2A subagents. Distributes work, collects results, and orchestrates multi-agent workflows.",
				Tags:        []string{"swarm", "coordination", "multi-agent"},
			},
		},
	}
}

// ServeAgentCard returns an HTTP handler that serves the A2A Agent Card.
func ServeAgentCard(card a2a.AgentCard) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(card)
	}
}

// A2AServer wraps the A2A protocol server for FreeLLM.
type A2AServer struct {
	handler  a2asrv.RequestHandler
	card     a2a.AgentCard
	executor *FreeLLMAgentExecutor
}

// NewA2AServer creates a new A2A server backed by the FreeLLM gateway.
func NewA2AServer(router MessageRouter, baseURL string) *A2AServer {
	executor := NewFreeLLMAgentExecutor(router)
	card := AgentCard(baseURL)

	handler := a2asrv.NewHandler(executor)

	return &A2AServer{
		handler:  handler,
		card:     card,
		executor: executor,
	}
}

// RegisterRoutes registers A2A HTTP routes on the given mux.
func (s *A2AServer) RegisterRoutes(mux *http.ServeMux) {
	// Agent Card discovery endpoint (A2A spec)
	mux.HandleFunc("/.well-known/agent-card", ServeAgentCard(s.card))

	// A2A JSON-RPC endpoint
	jsonrpcHandler := a2asrv.NewJSONRPCHandler(s.handler)
	mux.HandleFunc("/a2a", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonrpcHandler.ServeHTTP(w, r)
	})

	// A2A agent discovery endpoint
	mux.HandleFunc("/a2a/agents", s.handleAgentList)

	log.Println("[A2A] Server registered: /.well-known/agent-card, /a2a, /a2a/agents")
}

func (s *A2AServer) handleAgentList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	agents := []a2a.AgentCard{s.card}
	json.NewEncoder(w).Encode(agents)
}

// ServeAgentCard handles the /.well-known/agent-card endpoint.
func (s *A2AServer) ServeAgentCard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.card)
}

// ServeA2A handles the /a2a JSON-RPC endpoint.
func (s *A2AServer) ServeA2A(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	jsonrpcHandler := a2asrv.NewJSONRPCHandler(s.handler)
	jsonrpcHandler.ServeHTTP(w, r)
}

// ServeAgentList handles the /a2a/agents discovery endpoint.
func (s *A2AServer) ServeAgentList(w http.ResponseWriter, r *http.Request) {
	s.handleAgentList(w, r)
}
