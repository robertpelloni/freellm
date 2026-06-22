package proxy

import (
	"sync"
	"time"
)

// NextStepPlugin is a simple plugin that submits "continue" after completion
// events with a 5-minute cooldown between messages per session.
type NextStepPlugin struct {
	mu           sync.Mutex
	lastContinue map[string]time.Time // sessionID -> last continue time
}

// NewNextStepPlugin creates a new NextStep plugin.
func NewNextStepPlugin() *NextStepPlugin {
	return &NextStepPlugin{
		lastContinue: make(map[string]time.Time),
	}
}

// ShouldContinue checks if 5 minutes have passed since the last continue for this session.
// If yes, it records the current time and returns true.
func (p *NextStepPlugin) ShouldContinue(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	last, ok := p.lastContinue[sessionID]
	if ok && time.Since(last) < 5*time.Minute {
		return false
	}
	p.lastContinue[sessionID] = time.Now()
	return true
}

// Reset clears the cooldown for a session when a new user request arrives.
func (p *NextStepPlugin) Reset(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.lastContinue, sessionID)
}
