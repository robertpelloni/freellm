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
	failedProviders := make(map[string]int)

	// Retry indefinitely until successful. This loop NEVER exits except on success
	// or client disconnect.
	for attempt := 1; ; attempt++ {

		// ── Periodic history reset ──────────────────────────────────────
		// Every 10 attempts, clear the tried map so models that were on
		// cooldown get another chance. This is the key to never giving up.
		if attempt > 1 && attempt%10 == 0 {
			log.Printf("[ROUTER] Attempt %d: Resetting tried list to allow re-evaluation of all models", attempt)
			g.refreshTriedList(&tried, &failedProviders)
		}
		// Reset failed-providers counter periodically too
		if len(failedProviders) >= 50 {
			log.Printf("[ROUTER] Attempt %d: Resetting failed providers list (%d failures) to allow retry", attempt, len(failedProviders))
			failedProviders = make(map[string]int)
		}

		// ── Parameter filter relaxation ─────────────────────────────────
		// Gradually relax the parameter filter so smaller models are tried
		// if larger ones keep failing.
		filter := g.MinParamsFilter
		if attempt > 1 {
			filter = g.MinParamsFilter / 2
		}
		if attempt > 3 {
			filter = 0
		}

		// ── Candidate selection ─────────────────────────────────────────
		candidates := g.filterCandidatesWithOverride(allModels, filter)
		var fresh []engine.ModelCandidate
		g.cooldownMu.Lock()
		for _, m := range candidates {
			// Skip models already tried in this cycle
			if tried[m.ID+"|"+m.Provider] {
				continue
			}
			// Skip models on cooldown (only enforce in early attempts;
			// after 5 attempts we let everything through)
			if attempt <= 5 {
				if until, onCooldown := g.providerCooldown[m.Provider]; onCooldown && time.Now().Before(until) {
					continue
				}
			}
			// Proactive Health Check: skip providers that are currently unreachable
			if !g.PreFlightCheck(m) {
				continue
			}
			fresh = append(fresh, m)
		}
		g.cooldownMu.Unlock()

		if len(fresh) == 0 {
			log.Printf("[ROUTER] Attempt %d: No fresh candidates available. All models tried or on cooldown. Waiting for cooldowns to expire...", attempt)
			// Wait a moment then loop again - cooldowns will expire
			time.Sleep(2 * time.Second)
			// Reset tried map so models get re-tried after cooldown
			tried = make(map[string]bool)
			continue
		}

		// ── Fan-out selection with provider diversity ───────────────────
		fanSize := g.FanOutSize
		if fanSize < 2 {
			fanSize = 2
		}
		if fanSize > len(fresh) {
			fanSize = len(fresh)
		}

		var batch []engine.ModelCandidate
		providerUsed := make(map[string]bool)
		for _, m := range fresh {
			if !providerUsed[m.Provider] {
				batch = append(batch, m)
				providerUsed[m.Provider] = true
				if len(batch) >= fanSize {
					break
				}
			}
		}

		for _, m := range batch {
			tried[m.ID+"|"+m.Provider] = true
		}

		log.Printf("[ROUTER] Attempt %d: Fanning out to %d models: %v", attempt, len(batch), func() []string {
			var names []string
			for _, m := range batch {
				names = append(names, m.ID+"("+m.Provider+")")
			}
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
				case g.upstreamSem <- struct{}{}:
					defer func() { <-g.upstreamSem }()
				case <-job.Ctx.Done():
					return
				}

				// Per-provider concurrency limit
				sem := g.GetProviderSem(candidate.Provider)
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-job.Ctx.Done():
					return
				}

				log.Printf("[ROUTER] Attempt %d: Sending request to %s(%s)", attempt, candidate.ID, candidate.Provider)
				resp := g.forwardRequestInternal(g.Client, job.Request, candidate, body, false, nil)

				// Update score based on response success
				if resp.Status == 200 {
					candidate.Score *= 2.0
					if candidate.Score < 1.0 {
						candidate.Score = 1.0
					}
				} else if resp.Status >= 400 && resp.Status < 500 {
					log.Printf("[ROUTER] Provider %s returned 4xx (%d), disabling model.", candidate.Provider, resp.Status)
					candidate.Disabled = true
					candidate.Score = 0.0
				} else {
					candidate.Score *= 0.5
					if candidate.Score < 0.1 {
						candidate.Score = 0.1
					}
				}
				// Force disabled models to 0.0
				if candidate.Disabled {
					candidate.Score = 0.0
				}

				if resp.Status == 413 {
					log.Printf("[ROUTER] Provider %s rejected payload (413), cooling down for 30s", candidate.Provider)
					g.applyProviderCooldown(candidate.Provider, 30*time.Second)
				}

				resCh <- result{
					model: candidate,
					resp:  resp,
				}
			}(m)
		}

		// ── Collect results with smart switching ────────────────────────
		var winner *result
		var bestQuality float64 = -1

		// Batch timeout - how long to wait for ALL responses before moving on
		batchDeadline := time.After(20 * time.Second)
		responsesReceived := 0
		fanActual := len(batch)

		for responsesReceived < fanActual {
			select {
			case res := <-resCh:
				responsesReceived++

				if res.resp.Err != nil || res.resp.Status >= 400 {
					log.Printf("[ROUTER] Attempt %d: %s(%s) failed: %v (Status %d)",
						attempt, res.model.ID, res.model.Provider, res.resp.Err, res.resp.Status)
					if g.DB != nil {
						db.RecordFailure(g.DB, res.model.ID)
					}

					g.cooldownMu.Lock()
					failedProviders[res.model.Provider]++
					g.cooldownMu.Unlock()

					cooldown := 5 * time.Second
					if res.resp.Status == http.StatusUnauthorized ||
						res.resp.Status == http.StatusPaymentRequired ||
						res.resp.Status == http.StatusForbidden {
						cooldown = 5 * time.Minute
						g.DemoteModel(res.model.ID)
					} else if res.resp.Status == http.StatusTooManyRequests {
						cooldown = 10 * time.Second
					} else if res.resp.Status >= 500 {
						cooldown = 15 * time.Second
					}
					g.applyProviderCooldown(res.model.Provider, cooldown)
					continue
				}

				q := QualityScore(res.model)
				log.Printf("[ROUTER] Attempt %d: Received response from %s(%s), quality %.1f",
					attempt, res.model.ID, res.model.Provider, q)

				if winner == nil {
					winner = &res
					bestQuality = q

					// Smart switching window: wait a bit to see if a better model responds
					window := g.SmartSwitchDelay
					if bestQuality < 2.0 {
						window = 1 * time.Second
					}
					log.Printf("[ROUTER] Attempt %d: Got first response from %s(%.1f), waiting %v for potentially better models...",
						attempt, res.model.ID, bestQuality, window)

					winDeadline := time.After(window)
				WindowWait:
					for {
						select {
						case r2 := <-resCh:
							responsesReceived++
							if r2.resp.Err == nil && r2.resp.Status < 400 {
								q2 := QualityScore(r2.model)
								if q2 > bestQuality {
									log.Printf("[ROUTER] SMART SWITCH: %s(%.1f) > %s(%.1f) - switching!",
										r2.model.ID, q2, winner.model.ID, bestQuality)
									if winner.resp.Stream != nil {
										winner.resp.Stream.Close()
									}
									winner = &r2
									bestQuality = q2
									if bestQuality >= 2.5 {
										break WindowWait
									}
								} else {
									// Close the inferior response
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
							log.Printf("[ROUTER] Client disconnected during fan-out")
							if winner != nil && winner.resp.Stream != nil {
								winner.resp.Stream.Close()
							}
							return
						}
					}
					break // break out of the responsesReceived loop
				}
			case <-batchDeadline:
				log.Printf("[ROUTER] Attempt %d: Batch timeout reached", attempt)
				goto BatchDone
			case <-job.Ctx.Done():
				log.Printf("[ROUTER] Client disconnected while waiting for responses")
				return
			}
			if winner != nil {
				break
			}
		}

	BatchDone:
		if winner != nil {
			log.Printf("[ROUTER] Attempt %d: WINNER = %s(%s) with quality %.1f",
				attempt, winner.model.ID, winner.model.Provider, bestQuality)
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

		// No winner this batch - log and retry
		log.Printf("[ROUTER] Attempt %d: No winner from batch of %d models. Waiting before retry...", attempt, len(batch))
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
