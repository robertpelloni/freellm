package proxy

import (
	"fmt"
	"io"
	"log"
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

// refreshTriedList periodically resets the tried-models map so that models that
// were on cooldown get another chance as soon as their cooldown expires.
// This is the core of the "never give up" guarantee.
func (g *Gateway) refreshTriedList(tried *map[string]bool, failedProviders *map[string]int) {
	*tried = make(map[string]bool)
	*failedProviders = make(map[string]int)
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
	log.Printf("[ROUTER] Processing request (size: %d bytes)", len(body))

	tried := make(map[string]bool)

	// Retry loop: Use random fan-out for resilient routing
	for attempt := 1; ; attempt++ {

		// ── Candidate selection ─────────────────────────────────────────
		// Filter candidates based on params and current availability
		candidates := g.filterCandidatesWithOverride(allModels, g.MinParamsFilter)
		
		var fresh []engine.ModelCandidate
		g.cooldownMu.Lock()
		for _, m := range candidates {
			// Skip models already tried in this cycle
			if tried[m.ID+"|"+m.Provider] { continue }
			
			// Cooldown logic: skip providers currently on timeout
			if until, onCooldown := g.providerCooldown[m.Provider]; onCooldown && time.Now().Before(until) {
				continue
			}
			
			// Basic health check
			if !g.PreFlightCheck(m) { continue }
			
			fresh = append(fresh, m)
		}
		g.cooldownMu.Unlock()

		if len(fresh) == 0 {
			log.Printf("[ROUTER] Attempt %d: No fresh candidates available. Waiting...", attempt)
			time.Sleep(2 * time.Second)
			tried = make(map[string]bool)
			continue
		}

		// ── Fan-out selection with provider diversity ───────────────────
		fanSize := g.FanOutSize
		if fanSize < 1 { fanSize = 1 }
		if fanSize > len(fresh) { fanSize = len(fresh) }

		var batch []engine.ModelCandidate
		providerUsed := make(map[string]bool)
		for _, m := range fresh {
			if !providerUsed[m.Provider] {
				batch = append(batch, m)
				providerUsed[m.Provider] = true
				if len(batch) >= fanSize { break }
			}
		}

		for _, m := range batch {
			tried[m.ID+"|"+m.Provider] = true
		}

		log.Printf("[ROUTER] Attempt %d: Fanning out to %d models: %v", attempt, len(batch), func() []string {
			var names []string
			for _, m := range batch { names = append(names, m.ID+"("+m.Provider+")") }
			return names
		}())

		// ── Launch parallel requests ────────────────────────────────────
		type result struct {
			model engine.ModelCandidate
			resp  *ProxyResponse
		}
		resCh := make(chan result, len(batch))

		for _, m := range batch {
			go func(candidate engine.ModelCandidate) {
				// Global concurrency limit
				select {
				case g.upstreamSem <- struct{}{}: defer func() { <-g.upstreamSem }()
				case <-job.Ctx.Done(): return
				}

				// Per-provider concurrency limit
				sem := g.GetProviderSem(candidate.Provider)
				select {
				case sem <- struct{}{}: defer func() { <-sem }()
				case <-job.Ctx.Done(): return
				}

				log.Printf("[ROUTER] Attempt %d: Sending request to %s(%s)", attempt, candidate.ID, candidate.Provider)
				resp := g.forwardRequestInternal(job.Ctx, g.Client, job.Request, candidate, body, false, nil)

				// Scoring updates
				if resp.Status == 200 {
					candidate.Score *= 2.0
					if candidate.Score < 1.0 { candidate.Score = 1.0 }
				} else if resp.Status == 429 {
					log.Printf("[ROUTER] Provider %s hit rate limit (429), cooling down.", candidate.Provider)
					candidate.Score *= 0.8
					g.applyProviderCooldown(candidate.Provider, 10*time.Second)
				} else if resp.Status >= 400 && resp.Status < 500 {
					log.Printf("[ROUTER] Provider %s returned permanent error (%d), disabling model.", candidate.Provider, resp.Status)
					candidate.Disabled = true
					candidate.Score = 0.0
				} else {
					candidate.Score *= 0.5
					if candidate.Score < 0.1 { candidate.Score = 0.1 }
				}
				if candidate.Disabled { candidate.Score = 0.0 }
				
				resCh <- result{model: candidate, resp: resp}
			}(m)
		}

		// ── Result Collection ───────────────────────────────────────────
		var winner *result
		var bestQuality float64 = -1
		batchDeadline := time.After(20 * time.Second)
		responsesReceived := 0
		fanActual := len(batch)

		for responsesReceived < fanActual {
			select {
			case res := <-resCh:
				responsesReceived++
				if res.resp.Err != nil || res.resp.Status >= 400 {
					log.Printf("[ROUTER] Attempt %d: %s(%s) failed: %v (Status %d)", attempt, res.model.ID, res.model.Provider, res.resp.Err, res.resp.Status)
					if res.resp.Status == 401 || res.resp.Status == 403 || res.resp.Status == 404 { g.DemoteModel(res.model.ID) }
					continue
				}

				q := QualityScore(res.model)
				if winner == nil {
					winner = &res
					bestQuality = q
					winDeadline := time.After(1 * time.Second)
				WindowWait:
					for {
						select {
						case r2 := <-resCh:
							responsesReceived++
							if r2.resp.Err == nil && r2.resp.Status < 400 {
								q2 := QualityScore(r2.model)
								if q2 > bestQuality {
									if winner.resp.Stream != nil { winner.resp.Stream.Close() }
									winner = &r2
									bestQuality = q2
								} else {
									if r2.resp.Stream != nil { r2.resp.Stream.Close() }
								}
							}
						case <-winDeadline: break WindowWait
						case <-batchDeadline: break WindowWait
						}
					}
					break
				}
			case <-batchDeadline: goto BatchDone
			case <-job.Ctx.Done(): return
			}
			if winner != nil { break }
		}

	BatchDone:
		if winner != nil {
			g.onSuccess(job, winner.model, winner.resp, body)
			return
		}
		time.Sleep(200 * time.Millisecond)
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
		paramBonus := float64(m.Parameters) / 100_000_000_000.0 // Normalize to 100B
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
