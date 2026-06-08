package proxy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/robertpelloni/freellm/internal/engine"
)

// Session tracks a conversation session identified by content hash.
// It remembers the last-known-working model for continuity and
// maintains a list of high-quality alternatives being explored.
type Session struct {
	ID           string
	Preferred    engine.ModelCandidate // Last known working model for this session
	Alternatives []engine.ModelCandidate // Models being explored as potential upgrades
	QualityScore float64 // Best quality score seen for this session
	CreatedAt    time.Time
	LastUsedAt   time.Time
	RequestCount int
}

// SessionTracker manages conversation sessions with model affinity.
// It identifies sessions by content hash (first user message fingerprint)
// and tracks the best model for each session while continuously exploring
// higher-quality alternatives.
type SessionTracker struct {
	mu       sync.RWMutex
	sessions map[string]*Session // sessionID -> Session
	maxAge   time.Duration       // How long to remember a session
}

// NewSessionTracker creates a new session tracker.
func NewSessionTracker() *SessionTracker {
	st := &SessionTracker{
		sessions: make(map[string]*Session),
		maxAge:   4 * time.Hour,
	}
	go st.evictLoop()
	return st
}

// extractSessionFingerprint hashes the first user message content to identify
// a conversation session. This allows continuity across multiple turns of the
// same conversation, even without an explicit session ID.
func extractSessionFingerprint(body []byte) string {
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Messages) == 0 {
		// Fallback: hash the entire body
		h := sha256.Sum256(body)
		return fmt.Sprintf("raw-%x", h[:8])
	}

	// Find the first user message
	for _, msg := range payload.Messages {
		if msg.Role == "user" {
			h := sha256.Sum256(msg.Content)
			return fmt.Sprintf("sess-%x", h[:12])
		}
	}

	// No user message found - hash entire body
	h := sha256.Sum256(body)
	return fmt.Sprintf("raw-%x", h[:8])
}

// Lookup finds or creates a session for the given request body.
func (st *SessionTracker) Lookup(body []byte) *Session {
	fingerprint := extractSessionFingerprint(body)

	st.mu.RLock()
	session, exists := st.sessions[fingerprint]
	st.mu.RUnlock()

	if exists {
		st.mu.Lock()
		session.LastUsedAt = time.Now()
		session.RequestCount++
		st.mu.Unlock()
		return session
	}

	// New session
	session = &Session{
		ID:        fingerprint,
		CreatedAt: time.Now(),
		LastUsedAt: time.Now(),
		RequestCount: 1,
	}

	st.mu.Lock()
	st.sessions[fingerprint] = session
	st.mu.Unlock()

	log.Printf("[SESSION] New session: %s", fingerprint)
	return session
}

// UpdatePreferred updates the session's preferred model after a successful response.
func (st *SessionTracker) UpdatePreferred(sessionID string, model engine.ModelCandidate) {
	st.mu.Lock()
	defer st.mu.Unlock()

	session, exists := st.sessions[sessionID]
	if !exists {
		return
	}

	session.Preferred = model
	if model.Score > session.QualityScore {
		session.QualityScore = model.Score
	}
}

// InvalidatePreferred clears the session's preferred model when it fails
// (e.g. 429 rate limit, 5xx server error). This forces the next request
// to select a new preferred model from the current best candidates.
func (st *SessionTracker) InvalidatePreferred(sessionID string, reason string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	session, exists := st.sessions[sessionID]
	if !exists {
		return
	}

	log.Printf("[SESSION] %s: Invalidating preferred model %s (%s)",
		sessionID, session.Preferred.ID, reason)
	session.Preferred = engine.ModelCandidate{} // Clear preferred
}

// RecordExplorationResult records the outcome of an exploratory fan-out attempt.
// If an alternative model succeeded with higher quality than the current preferred,
// it becomes the new preferred model for the session.
func (st *SessionTracker) RecordExplorationResult(sessionID string, model engine.ModelCandidate, success bool) {
	st.mu.Lock()
	defer st.mu.Unlock()

	session, exists := st.sessions[sessionID]
	if !exists {
		return
	}

	if !success {
		// Remove failed alternatives
		filtered := session.Alternatives[:0]
		for _, alt := range session.Alternatives {
			if alt.ID != model.ID {
				filtered = append(filtered, alt)
			}
		}
		session.Alternatives = filtered
		return
	}

	// Successful alternative - promote if higher quality
	// Quality is determined by model size (parameters) and benchmark score
	modelQuality := qualityScore(model)
	preferredQuality := qualityScore(session.Preferred)

	if modelQuality > preferredQuality || session.Preferred.ID == "" {
		log.Printf("[SESSION] %s: Upgrading preferred model %s -> %s (quality %.1f -> %.1f)",
			sessionID, session.Preferred.ID, model.ID, preferredQuality, modelQuality)
		session.Preferred = model
		session.QualityScore = modelQuality
	}
}

// GetRoutingPlan returns the model routing plan for a session.
// The plan prioritizes the preferred model (for continuity) but also
// includes 2-3 high-quality alternatives for exploration.
func (st *SessionTracker) GetRoutingPlan(session *Session, allModels []engine.ModelCandidate, activeCooldowns map[string]bool) RoutingPlan {
	plan := RoutingPlan{
		SessionID: session.ID,
	}

	st.mu.RLock()
	defer st.mu.RUnlock()

	// 1. Preferred model (session continuity) - always try first if available
	if session.Preferred.ID != "" && !activeCooldowns[session.Preferred.Provider] {
		// Refresh preferred model's score from current rankings
		for _, m := range allModels {
			if m.ID == session.Preferred.ID && m.Provider == session.Preferred.Provider {
				session.Preferred.Score = m.Score
				break
			}
		}
		plan.Preferred = session.Preferred
	}

	// 2. Select high-quality alternatives, prioritizing:
	//    - Models with higher quality score than preferred (upgrade candidates)
	//    - Diverse providers (different API endpoints)
	//    - Models with good track records
	seenProviders := make(map[string]bool)
	if session.Preferred.ID != "" {
		seenProviders[session.Preferred.Provider] = true
	}

	// Sort all models by quality (size * score), not latency
	qualityModels := make([]engine.ModelCandidate, len(allModels))
	copy(qualityModels, allModels)
	sort.Slice(qualityModels, func(i, j int) bool {
		return qualityScore(qualityModels[i]) > qualityScore(qualityModels[j])
	})

	// Pick top 3 diverse high-quality alternatives
	for _, m := range qualityModels {
		if activeCooldowns[m.Provider] {
			continue
		}
		if seenProviders[m.Provider] {
			continue
		}
		if m.Score < 0 {
			continue // skip broken models
		}
		plan.Alternatives = append(plan.Alternatives, m)
		seenProviders[m.Provider] = true
		if len(plan.Alternatives) >= 3 {
			break
		}
	}

	// 3. If no preferred model yet, use the top-quality model
	if plan.Preferred.ID == "" && len(qualityModels) > 0 {
		for _, m := range qualityModels {
			if !activeCooldowns[m.Provider] && m.Score >= 0 {
				plan.Preferred = m
				break
			}
		}
	}

	return plan
}

// UpdateAlternatives refreshes the list of exploration candidates from current rankings.
func (st *SessionTracker) UpdateAlternatives(sessionID string, allModels []engine.ModelCandidate) {
	st.mu.Lock()
	defer st.mu.Unlock()

	session, exists := st.sessions[sessionID]
	if !exists {
		return
	}

	seenProviders := make(map[string]bool)
	if session.Preferred.ID != "" {
		seenProviders[session.Preferred.Provider] = true
	}

	qualityModels := make([]engine.ModelCandidate, len(allModels))
	copy(qualityModels, allModels)
	sort.Slice(qualityModels, func(i, j int) bool {
		return qualityScore(qualityModels[i]) > qualityScore(qualityModels[j])
	})

	var newAlts []engine.ModelCandidate
	for _, m := range qualityModels {
		if seenProviders[m.Provider] {
			continue
		}
		newAlts = append(newAlts, m)
		seenProviders[m.Provider] = true
		if len(newAlts) >= 3 {
			break
		}
	}
	session.Alternatives = newAlts
}

// ActiveSessionCount returns the number of active sessions.
func (st *SessionTracker) ActiveSessionCount() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.sessions)
}

// qualityScore computes a quality metric for a model, prioritizing
// model size (parameters) and benchmark score over latency.
// Higher quality models that need time to think are not penalized.
func qualityScore(m engine.ModelCandidate) float64 {
	// Base score from benchmark
	score := m.Score
	if score < 0 {
		return -1
	}

	// Size bonus: larger models are higher quality
	// 400B+ models get a significant boost
	sizeBonus := 0.0
	switch {
	case m.Parameters >= 400000:
		sizeBonus = 2.0
	case m.Parameters >= 100000:
		sizeBonus = 1.5
	case m.Parameters >= 70000:
		sizeBonus = 1.0
	case m.Parameters >= 30000:
		sizeBonus = 0.5
	default:
		sizeBonus = 0.0
	}

	// Context length bonus: longer context = more capable
	ctxBonus := 0.0
	switch {
	case m.ContextLength >= 128000:
		ctxBonus = 0.3
	case m.ContextLength >= 64000:
		ctxBonus = 0.2
	case m.ContextLength >= 32000:
		ctxBonus = 0.1
	}

	// We intentionally DO NOT penalize latency.
	// Quality models take time to think. High latency from a good model
	// is acceptable; high latency from a bad model just means it's overloaded.

	return score + sizeBonus + ctxBonus
}

// RoutingPlan describes which models to try for a session request.
type RoutingPlan struct {
	SessionID   string
	Preferred   engine.ModelCandidate   // Session's preferred model (continuity)
	Alternatives []engine.ModelCandidate // Exploration candidates (quality upgrades)
}

// AllModels returns all models in the plan (preferred first, then alternatives).
func (rp *RoutingPlan) AllModels() []engine.ModelCandidate {
	models := []engine.ModelCandidate{rp.Preferred}
	models = append(models, rp.Alternatives...)
	return models
}

// ModelCount returns the total number of models in the plan.
func (rp *RoutingPlan) ModelCount() int {
	count := 0
	if rp.Preferred.ID != "" {
		count++
	}
	count += len(rp.Alternatives)
	return count
}

// evictLoop periodically removes stale sessions.
func (st *SessionTracker) evictLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		st.mu.Lock()
		now := time.Now()
		for id, session := range st.sessions {
			if now.Sub(session.LastUsedAt) > st.maxAge {
				delete(st.sessions, id)
				log.Printf("[SESSION] Evicted stale session: %s (age: %v, requests: %d)",
					id, now.Sub(session.LastUsedAt), session.RequestCount)
			}
		}
		st.mu.Unlock()
	}
}
