package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"time"

	"github.com/robertpelloni/freellm/internal/engine"
)

// applyProviderCooldown sets a temporary cooldown for a provider.
func (g *Gateway) applyProviderCooldown(provider string, d time.Duration) {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	exp := time.Now().Add(d)
	if cur, ok := g.providerCooldown[provider]; !ok || exp.After(cur) {
		g.providerCooldown[provider] = exp
	}
}

// emit ships a notable router event to the job's client-facing event
// channel. It is non-blocking: if no one is listening or the buffer is
// full (e.g. ServeHTTP already returned, or the client disconnected), the
// event is dropped so the router is never stalled by a slow consumer. A
// nil job or nil channel is a no-op.
func emit(job *RequestJob, tag, message string) {
	if job == nil || job.Events == nil {
		return
	}
	select {
	case job.Events <- RouterEvent{Tag: tag, Message: message}:
	default:
	}
}

// processJob runs the routing loop with fan-out and smart switching.
// It NEVER gives up. It will cycle through all available models indefinitely,
// resetting its attempt history periodically so that cooldown-expired models
// get retried.
func (g *Gateway) processJob(job *RequestJob) {
	g.mu.RLock()
	allModels := g.RankedModels
	g.mu.RUnlock()

	if len(allModels) == 0 {
		job.Response <- &ProxyResponse{Status: 503, Err: fmt.Errorf("no models available")}
		return
	}

	body, err := io.ReadAll(job.Request.Body)
	if err != nil {
		job.Response <- &ProxyResponse{Status: 400, Err: fmt.Errorf("read body: %v", err)}
		return
	}

	// Apply multi-layered context compression if enabled (rtk, Headroom, LLMLingua)
	compressedBody, err := g.CompressContext(body)
	if err == nil {
		body = compressedBody
	}

	log.Printf("[ROUTER] Processing request (size: %d bytes)", len(body))

	tried := make(map[string]bool)

	g.queueMu.Lock()
	startIdx := g.queueIndex
	g.queueIndex = (g.queueIndex + 1) % 1000000
	g.queueMu.Unlock()

	// Retry loop: sequential round-robin rotating queue
	for attempt := 1; ; attempt++ {
		// ── Candidate selection & sorting ────────────────────────────────
		candidates := g.filterCandidatesWithOverride(allModels, g.MinParamsFilter)
		if len(candidates) == 0 {
			log.Printf("[ROUTER] Attempt %d: No candidates available (MinParamsFilter: %d). Waiting...", attempt, g.MinParamsFilter)
			emit(job, "ROUTER", fmt.Sprintf("attempt %d: no candidates available, waiting", attempt))
			select {
			case <-job.Ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		// Sort candidates by proven status and quality score
		sort.SliceStable(candidates, func(i, j int) bool {
			provI := g.IsProven(candidates[i].ID, candidates[i].Provider)
			provJ := g.IsProven(candidates[j].ID, candidates[j].Provider)
			if provI != provJ {
				return provI // proven comes first
			}
			return QualityScore(candidates[i]) > QualityScore(candidates[j])
		})

		numCandidates := len(candidates)
		candidateTriedInThisAttempt := false

		for step := 0; step < numCandidates; step++ {
			idx := (startIdx + step) % numCandidates
			m := candidates[idx]

			// Skip if tried in this attempt
			if tried[m.ID+"|"+m.Provider] {
				continue
			}

			// Cooldown logic: skip provider if on cooldown, UNLESS we are in emergency mode
			g.cooldownMu.Lock()
			until, onCooldown := g.providerCooldown[m.Provider]
			g.cooldownMu.Unlock()
			if onCooldown && time.Now().Before(until) && g.MinParamsFilter > 0 {
				continue
			}

			// Pre-flight check
			if !g.PreFlightCheck(m) {
				continue
			}

			candidateTriedInThisAttempt = true

			// Global concurrency limit
			select {
			case g.upstreamSem <- struct{}{}:
			case <-job.Ctx.Done():
				return
			}

			// Per-provider concurrency limit
			sem := g.GetProviderSem(m.Provider)
			select {
			case sem <- struct{}{}:
			case <-job.Ctx.Done():
				<-g.upstreamSem
				return
			}

			log.Printf("[ROUTER] Attempt %d: Sending request to %s(%s) (seq queue)", attempt, m.ID, m.Provider)
			emit(job, "ROUTER", fmt.Sprintf("attempt %d: routing to %s(%s)", attempt, m.ID, m.Provider))

			resp := g.forwardRequestInternal(job.Ctx, g.Client, job.Request, m, body, false, nil)

			// Release semaphores
			<-sem
			<-g.upstreamSem

			// Scoring updates
			if resp.Status == 200 {
				if g.Judge.Enabled && !job.IsStream {
					verdict, err := g.evaluateResponseWithJudge(job.Ctx, resp.Body)
					if err != nil {
						log.Printf("[ROUTER] Local judge evaluation failed: %v. Assuming response is OK.", err)
					} else if !verdict.Complete || verdict.HasErrors {
						log.Printf("[ROUTER] Local judge rejected response from %s(%s). Reason: %s. Retrying next candidate...", m.ID, m.Provider, verdict.Reason)
						emit(job, "ROUTER", fmt.Sprintf("%s(%s) failed judge evaluation: %s", m.ID, m.Provider, verdict.Reason))
						g.AdjustModelScore(m.ID, m.Provider, 0.4) // Penalize score
						tried[m.ID+"|"+m.Provider] = true
						continue // Skip to next candidate in the queue
					} else {
						log.Printf("[ROUTER] Local judge approved response from %s(%s).", m.ID, m.Provider)
						if verdict.RewrittenContent != "" {
							log.Printf("[ROUTER] Local judge rewrote response.")
							var fullResp map[string]interface{}
							if json.Unmarshal(resp.Body, &fullResp) == nil {
								if choices, ok := fullResp["choices"].([]interface{}); ok && len(choices) > 0 {
									if choice, ok := choices[0].(map[string]interface{}); ok {
										if msg, ok := choice["message"].(map[string]interface{}); ok {
											msg["content"] = verdict.RewrittenContent
											if newBody, err := json.Marshal(fullResp); err == nil {
												resp.Body = newBody
											}
										}
									}
								}
							}
						}
					}
				}

				g.AdjustModelScore(m.ID, m.Provider, 2.0)
				g.MarkProven(m.ID, m.Provider)
				g.onSuccess(job, m, resp, body)
				return
			} else if resp.Status == 429 {
				log.Printf("[ROUTER] Provider %s hit rate limit (429), cooling down for 30s.", m.Provider)
				emit(job, "ROUTER", fmt.Sprintf("%s(%s) rate-limited (429), cooling down", m.ID, m.Provider))
				g.AdjustModelScore(m.ID, m.Provider, 0.8)
				g.applyProviderCooldown(m.Provider, 30*time.Second)
			} else if resp.Status >= 400 && resp.Status < 500 {
				log.Printf("[ROUTER] Provider %s returned permanent error (%d), disabling model.", m.Provider, resp.Status)
				emit(job, "ROUTER", fmt.Sprintf("%s(%s) permanent error (%d), disabling", m.ID, m.Provider, resp.Status))
				g.recordModelFailure(m.ID, m.Provider, resp.Status, resp.ErrorMessage)
				g.AdjustModelScore(m.ID, m.Provider, 0.1)
				if resp.Status == 401 || resp.Status == 403 || resp.Status == 404 {
					g.DemoteModel(m.ID)
				}
			} else {
				g.AdjustModelScore(m.ID, m.Provider, 0.5)
			}

			log.Printf("[ROUTER] Attempt %d: %s(%s) failed: %v (Status %d)", attempt, m.ID, m.Provider, resp.Err, resp.Status)
			emit(job, "ROUTER", fmt.Sprintf("attempt %d: %s(%s) failed (status %d)", attempt, m.ID, m.Provider, resp.Status))

			tried[m.ID+"|"+m.Provider] = true
		}

		// If no candidates were tried or all failed, sleep and retry
		if !candidateTriedInThisAttempt {
			log.Printf("[ROUTER] Attempt %d: No fresh/uncooled candidates were eligible. Resetting tried models.", attempt)
			tried = make(map[string]bool)
		}

		log.Printf("[ROUTER] Attempt %d complete. No model succeeded. Retrying...", attempt)
		emit(job, "ROUTER", fmt.Sprintf("attempt %d complete: all eligible models tried, retrying", attempt))

		select {
		case <-job.Ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// QualityScore calculates a score based on ranking, parameter count, and context window
func QualityScore(m engine.ModelCandidate) float64 {
	score := m.Score
	if score < 0 {
		return -1
	}
	// Bonus for larger models (more parameters = generally smarter)
	if m.Parameters > 0 {
		paramBonus := float64(m.Parameters) / 100.0 // Normalize to 100B (since Parameters are stored in billions, e.g. 70, 405)
		if paramBonus > 2.0 {
			paramBonus = 2.0
		}
		score += paramBonus
	}
	// Bonus for larger context windows
	if m.ContextLength > 0 {
		ctxBonus := float64(m.ContextLength) / 100_000.0 // Normalize to 100K
		if ctxBonus > 1.0 {
			ctxBonus = 1.0
		}
		score += ctxBonus
	}
	return score
}
