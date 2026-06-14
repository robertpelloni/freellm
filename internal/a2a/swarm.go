package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
)

// SwarmConfig configures the A2A agent swarm.
type SwarmConfig struct {
	MaxConcurrentAgents int
	TaskTimeout         time.Duration
	DiscoveryInterval   time.Duration
}

// DefaultSwarmConfig returns sensible defaults.
func DefaultSwarmConfig() SwarmConfig {
	return SwarmConfig{
		MaxConcurrentAgents: 20,
		TaskTimeout:         120 * time.Second,
		DiscoveryInterval:   5 * time.Minute,
	}
}

// RemoteAgent represents a discovered A2A agent.
type RemoteAgent struct {
	URL      string
	Card     a2a.AgentCard
	LastSeen time.Time
	Healthy  bool
	Client   *a2aclient.Client
	Skills   []string
}

// SwarmCoordinator manages a fleet of A2A subagents.
type SwarmCoordinator struct {
	config SwarmConfig
	mu     sync.RWMutex
	agents map[string]*RemoteAgent
	local  *A2AServer
}

// NewSwarmCoordinator creates a new swarm coordinator.
func NewSwarmCoordinator(config SwarmConfig, local *A2AServer) *SwarmCoordinator {
	return &SwarmCoordinator{
		config: config,
		agents: make(map[string]*RemoteAgent),
		local:  local,
	}
}

// RegisterAgent adds a remote A2A agent to the swarm.
func (sc *SwarmCoordinator) RegisterAgent(ctx context.Context, url string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	cardURL := strings.TrimRight(url, "/") + "/.well-known/agent-card"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(cardURL)
	if err != nil {
		return fmt.Errorf("failed to resolve agent card at %s: %w", cardURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent card endpoint returned status %d", resp.StatusCode)
	}

	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return fmt.Errorf("failed to parse agent card: %w", err)
	}

	var skills []string
	for _, skill := range card.Skills {
		skills = append(skills, skill.ID)
	}

	a2aClient, err := a2aclient.NewFromCard(ctx, &card)
	if err != nil {
		return fmt.Errorf("failed to create A2A client: %w", err)
	}

	sc.agents[url] = &RemoteAgent{
		URL:      url,
		Card:     card,
		LastSeen: time.Now(),
		Healthy:  true,
		Client:   a2aClient,
		Skills:   skills,
	}

	log.Printf("[A2A-SWARM] Registered agent: %s (%s) skills=%v", card.Name, url, skills)
	return nil
}

// UnregisterAgent removes a remote A2A agent from the swarm.
func (sc *SwarmCoordinator) UnregisterAgent(url string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	delete(sc.agents, url)
	log.Printf("[A2A-SWARM] Unregistered agent: %s", url)
}

// DispatchTask sends a task to the best available agent with matching skills.
func (sc *SwarmCoordinator) DispatchTask(ctx context.Context, message string, skillID string) (string, error) {
	agent := sc.findAgentForSkill(skillID)
	if agent == nil {
		return "", fmt.Errorf("no agent available with skill: %s", skillID)
	}

	log.Printf("[A2A-SWARM] Dispatching task to %s for skill %s", agent.Card.Name, skillID)

	msg := &a2a.Message{
		Role: "user",
		Parts: []a2a.Part{
			&a2a.TextPart{Text: message},
		},
	}

	taskCtx, cancel := context.WithTimeout(ctx, sc.config.TaskTimeout)
	defer cancel()

	params := &a2a.MessageSendParams{Message: msg}
	result, err := agent.Client.SendMessage(taskCtx, params)
	if err != nil {
		sc.mu.Lock()
		agent.Healthy = false
		sc.mu.Unlock()
		return "", fmt.Errorf("task failed on agent %s: %w", agent.Card.Name, err)
	}

	var responseText string
	if result != nil {
		// SendMessageResult can be a Task or Message
		switch r := result.(type) {
		case *a2a.Message:
			for _, part := range r.Parts {
				if tp, ok := part.(*a2a.TextPart); ok {
					responseText += tp.Text
				}
			}
		}
	}

	return responseText, nil
}

// BroadcastTask sends a task to ALL healthy agents with a matching skill.
func (sc *SwarmCoordinator) BroadcastTask(ctx context.Context, message string, skillID string) (map[string]string, error) {
	sc.mu.RLock()
	var candidates []*RemoteAgent
	for _, agent := range sc.agents {
		if !agent.Healthy {
			continue
		}
		if skillID != "" {
			found := false
			for _, s := range agent.Skills {
				if s == skillID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		candidates = append(candidates, agent)
	}
	sc.mu.RUnlock()

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no agents available for broadcast with skill: %s", skillID)
	}

	log.Printf("[A2A-SWARM] Broadcasting to %d agents", len(candidates))

	type result struct {
		name string
		resp string
		err  error
	}

	results := make(chan result, len(candidates))
	for _, agent := range candidates {
		go func(a *RemoteAgent) {
			msg := &a2a.Message{
				Role: "user",
				Parts: []a2a.Part{
					&a2a.TextPart{Text: message},
				},
			}
			params := &a2a.MessageSendParams{Message: msg}
			taskCtx, cancel := context.WithTimeout(ctx, sc.config.TaskTimeout)
			defer cancel()

			r, err := a.Client.SendMessage(taskCtx, params)
			if err != nil {
				results <- result{name: a.Card.Name, err: err}
				return
			}
			var text string
			if r != nil {
				switch rv := r.(type) {
				case *a2a.Message:
					for _, part := range rv.Parts {
						if tp, ok := part.(*a2a.TextPart); ok {
							text += tp.Text
						}
					}
				}
			}
			results <- result{name: a.Card.Name, resp: text}
		}(agent)
	}

	responses := make(map[string]string)
	for i := 0; i < len(candidates); i++ {
		r := <-results
		if r.err != nil {
			log.Printf("[A2A-SWARM] Agent %s failed: %v", r.name, r.err)
			continue
		}
		responses[r.name] = r.resp
	}

	return responses, nil
}

// FanOutTask distributes subtasks across the swarm in parallel.
func (sc *SwarmCoordinator) FanOutTask(ctx context.Context, subtasks map[string]string) (map[string]string, error) {
	type result struct {
		skillID  string
		response string
		err      error
	}

	results := make(chan result, len(subtasks))
	for skillID, message := range subtasks {
		go func(sid, msg string) {
			resp, err := sc.DispatchTask(ctx, msg, sid)
			results <- result{skillID: sid, response: resp, err: err}
		}(skillID, message)
	}

	responses := make(map[string]string)
	for i := 0; i < len(subtasks); i++ {
		r := <-results
		if r.err != nil {
			responses[r.skillID] = fmt.Sprintf("ERROR: %v", r.err)
			continue
		}
		responses[r.skillID] = r.response
	}

	return responses, nil
}

func (sc *SwarmCoordinator) findAgentForSkill(skillID string) *RemoteAgent {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	var best *RemoteAgent
	for _, agent := range sc.agents {
		if !agent.Healthy {
			continue
		}
		for _, s := range agent.Skills {
			if s == skillID {
				if best == nil || agent.LastSeen.After(best.LastSeen) {
					best = agent
				}
				break
			}
		}
	}
	return best
}

// ListAgents returns info about all known agents.
func (sc *SwarmCoordinator) ListAgents() []map[string]interface{} {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	var agents []map[string]interface{}
	for _, agent := range sc.agents {
		agents = append(agents, map[string]interface{}{
			"url":       agent.URL,
			"name":      agent.Card.Name,
			"healthy":   agent.Healthy,
			"last_seen": agent.LastSeen.Format(time.RFC3339),
			"skills":    agent.Skills,
		})
	}
	return agents
}

// AgentCount returns the number of registered agents.
func (sc *SwarmCoordinator) AgentCount() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return len(sc.agents)
}

// HealthyAgentCount returns the number of healthy agents.
func (sc *SwarmCoordinator) HealthyAgentCount() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	count := 0
	for _, agent := range sc.agents {
		if agent.Healthy {
			count++
		}
	}
	return count
}
