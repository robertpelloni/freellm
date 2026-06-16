package proxy

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/robertpelloni/freellm/internal/db"
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

// processJob runs the routing loop with fan-out and smart switching.
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

	tried := make(map[string]bool)
	failedProviders := make(map[string]int)

	// Total request deadline (ensure we don't hang forever)
	requestDeadline := time.Now().Add(g.RequestTimeout)
	if g.RequestTimeout == 0 {
		requestDeadline = time.Now().Add(60 * time.Second)
	}

	// Try up to 5 batches. This prevents infinite loops and respects typical client timeouts.
	for attempt := 1; attempt <= 5; attempt++ {
		// Check global context or deadline
		select {
		case <-job.Ctx.Done():
			return
		default:
			if time.Now().After(requestDeadline) {
				job.Response <- &ProxyResponse{Status: 504, Err: fmt.Errorf("router deadline exceeded after %d attempts", attempt-1)}
				return
			}
		}

		// Relax filter on later attempts
		filter := g.MinParamsFilter
		if attempt > 1 {
			filter = g.MinParamsFilter / 2
		}
		if attempt > 3 {
			filter = 0
		}

		candidates := g.filterCandidatesWithOverride(allModels, filter)
		var fresh []engine.ModelCandidate
		g.cooldownMu.Lock()
		for _, m := range candidates {
			// Skip models tried in THIS request
			if tried[m.ID+"|"+m.Provider] {
				continue
			}
			// Skip models on cooldown
			if until, onCooldown := g.providerCooldown[m.Provider]; onCooldown && time.Now().Before(until) {
				continue
			}
			fresh = append(fresh, m)
		}
		g.cooldownMu.Unlock()

		if len(fresh) == 0 {
			log.Printf("[ROUTER] Attempt %d: Exhausted all candidate models", attempt)
			break
		}

		// Fan-out: try multiple models in parallel with provider diversity
		fanSize := g.FanOutSize
		if fanSize < 2 {
			fanSize = 2
		}
		if fanSize > len(fresh) {
			fanSize = len(fresh)
		}
		
		var batch []engine.ModelCandidate
		providerCount := make(map[string]int)
		for _, m := range fresh {
			if providerCount[m.Provider] == 0 {
				batch = append(batch, m)
				providerCount[m.Provider]++
				if len(batch) >= fanSize {
					break
				}
			}
		}
		if len(batch) < fanSize {
			for _, m := range fresh {
				alreadyInBatch := false
				for _, bm := range batch {
					if bm.ID == m.ID && bm.Provider == m.Provider {
						alreadyInBatch = true
						break
					}
				}
				if !alreadyInBatch {
					batch = append(batch, m)
					if len(batch) >= fanSize {
						break
					}
				}
			}
		}

		for _, m := range batch {
			tried[m.ID+"|"+m.Provider] = true
		}

		log.Printf("[ROUTER] Attempt %d: Fanning out to %d models", attempt, len(batch))

		type result struct {
			model engine.ModelCandidate
			resp  *ProxyResponse
		}
		resCh := make(chan result, len(batch))

		for _, m := range batch {
			go func(candidate engine.ModelCandidate) {
				// 1. Global limit
				select {
				case g.upstreamSem <- struct{}{}:
					defer func() { <-g.upstreamSem }()
				case <-job.Ctx.Done():
					return
				}

				// 2. Per-provider limit
				sem := g.GetProviderSem(candidate.Provider)
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-job.Ctx.Done():
					return
				}

				// Note: Use a sub-context for the individual request that honors the overall job context
				resCh <- result{
					model: candidate,
					resp:  g.forwardRequestInternal(g.Client, job.Request, candidate, body, false, nil),
				}
			}(m)
		}

		var winner *result
		var bestQuality float64 = -1

		// Batch-specific timeout (ensure we don't wait too long for one slow provider)
		batchDeadline := time.After(20 * time.Second)
		responsesReceived := 0
		fanActual := len(batch)
		
		for responsesReceived < fanActual {
			select {
			case res := <-resCh:
				responsesReceived++
				if res.resp.Err != nil || res.resp.Status >= 400 {
					g.LogRouterEvent("Model %s(%s) failed: %v (Status %d)", res.model.ID, res.model.Provider, res.resp.Err, res.resp.Status)
					if g.DB != nil {
						db.RecordFailure(g.DB, res.model.ID)
					}

					g.cooldownMu.Lock()
					failedProviders[res.model.Provider]++
					g.cooldownMu.Unlock()

					cooldown := 5 * time.Second
					if res.resp.Status == http.StatusUnauthorized || res.resp.Status == http.StatusPaymentRequired || res.resp.Status == http.StatusForbidden {
						cooldown = 1 * time.Hour
						g.DemoteModel(res.model.ID)
					} else if res.resp.Status == http.StatusTooManyRequests {
						cooldown = 30 * time.Second
					}
					g.applyProviderCooldown(res.model.Provider, cooldown)
					continue
				}

				q := QualityScore(res.model)
				g.LogRouterEvent("Received response from %s(%s), quality %.1f", res.model.ID, res.model.Provider, q)

				if winner == nil {
					winner = &res
					bestQuality = q
					
					window := g.SmartSwitchDelay
					if bestQuality < 2.0 {
						window = 1 * time.Second // Tighten switch window for lower quality models
					}
					
					winDeadline := time.After(window)
					
				WindowWait:
					for {
						select {
						case r2 := <-resCh:
							responsesReceived++
							if r2.resp.Err == nil && r2.resp.Status < 400 {
								q2 := QualityScore(r2.model)
								if q2 > bestQuality {
									g.LogRouterEvent("SMART SWITCH: Found better model %s(%s) with quality %.1f > %.1f", r2.model.ID, r2.model.Provider, q2, bestQuality)
									if winner.resp.Stream != nil {
										winner.resp.Stream.Close()
									}
									winner = &r2
									bestQuality = q2
									if bestQuality >= 2.5 {
										break WindowWait
									}
								} else {
									if r2.resp.Stream != nil {
										r2.resp.Stream.Close()
									}
								}
							}
						case <-winDeadline:
							break WindowWait
						case <-batchDeadline:
							break WindowWait
						case <-job.Ctx.Done():
							if winner != nil && winner.resp.Stream != nil {
								winner.resp.Stream.Close()
							}
							return
						}
					}
					break // break out of the responsesReceived loop
				}
			case <-batchDeadline:
				log.Printf("[ROUTER] Batch timeout reached")
				goto BatchDone
			case <-job.Ctx.Done():
				return
			}
			if winner != nil {
				break
			}
		}

	BatchDone:
		if winner != nil {
			g.onSuccess(job, winner.model, winner.resp, body)
			g.LockModelForSession(winner.model.ID, winner.model.Provider)
			
			// Clean up other pending successful streams in background
			go func() {
				for i := responsesReceived; i < fanActual; i++ {
					select {
					case res := <-resCh:
						if res.resp.Stream != nil {
							res.resp.Stream.Close()
						}
					case <-time.After(5 * time.Second):
						return
					}
				}
			}()
			return
		}

		if len(failedProviders) >= 20 {
			log.Printf("[ROUTER] Too many distinct provider failures (%d), aborting routing.", len(failedProviders))
			job.Response <- &ProxyResponse{Status: 503, Err: fmt.Errorf("too many provider failures (%d)", len(failedProviders))}
			return
		}
		
		// Small delay between batches to allow cooldowns to potentially start expiring or just back off
		time.Sleep(500 * time.Millisecond)
	}

	job.Response <- &ProxyResponse{Status: 503, Err: fmt.Errorf("all models exhausted after 5 batches")}
}

// QualityScore calculates a score based on ranking, parameter count, and context window
func QualityScore(m engine.ModelCandidate) float64 {
	score := m.Score
	if score < 0 {
		return -1
	}

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
	}

	ctxBonus := 0.0
	switch {
	case m.ContextLength >= 128000:
		ctxBonus = 0.3
	case m.ContextLength >= 64000:
		ctxBonus = 0.2
	case m.ContextLength >= 32000:
		ctxBonus = 0.1
	}

	return score + sizeBonus + ctxBonus
}
