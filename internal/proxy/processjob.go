package proxy

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/robertpelloni/freellm/internal/db"
	"github.com/robertpelloni/freellm/internal/engine"
)

func (g *Gateway) processJob(job *RequestJob) {
	g.mu.RLock()
	allModels := g.RankedModels
	g.mu.RUnlock()

	if len(allModels) == 0 {
		job.Response <- &ProxyResponse{Err: fmt.Errorf("no models available")}
		return
	}

	body, err := io.ReadAll(job.Request.Body)
	if err != nil {
		job.Response <- &ProxyResponse{Err: fmt.Errorf("read body: %v", err)}
		return
	}

	hasTools, toolModels, plainModels := g.classifyRequest(body)

	// ── Session-Aware Routing ──────────────────────────────────────
	// Identify the session from conversation content fingerprint.
	// The session tracks the last-known-working model for continuity
	// and continuously explores higher-quality alternatives.
	session := g.Sessions.Lookup(body)

	// Get provider cooldowns
	g.cooldownMu.Lock()
	now := time.Now()
	activeCooldowns := make(map[string]bool)
	for prov, until := range g.providerCooldown {
		if now.Before(until) {
			activeCooldowns[prov] = true
		} else {
			delete(g.providerCooldown, prov)
		}
	}
	g.cooldownMu.Unlock()

	// Filter candidates
	models := g.filterCandidates(allModels)
	if len(models) == 0 {
		log.Println("[ROUTER] All models circuit-broken, auto-recovering...")
		g.autoRecoverCircuitBreakers()
		models = allModels
	}

	// Build candidate pool based on tool requirements
	var candidatePool []engine.ModelCandidate
	if hasTools && len(toolModels) > 0 {
		candidatePool = append(candidatePool, toolModels...)
		if len(candidatePool) < 5 {
			candidatePool = append(candidatePool, plainModels...)
		}
	} else {
		candidatePool = models
	}

	// Get the routing plan: preferred model + quality alternatives
	plan := g.Sessions.GetRoutingPlan(session, candidatePool, activeCooldowns)

	if plan.ModelCount() == 0 {
		for _, m := range candidatePool {
			if !activeCooldowns[m.Provider] && m.Score >= 0 {
				plan.Preferred = m
				break
			}
		}
		if plan.Preferred.ID == "" && len(candidatePool) > 0 {
			plan.Preferred = candidatePool[0]
		}
	}

	log.Printf("[SESSION] %s: preferred=%s(%s) quality=%.1f alts=%d hasTools=%v",
		session.ID, plan.Preferred.ID, plan.Preferred.Provider,
		qualityScore(plan.Preferred), len(plan.Alternatives), hasTools)

	// ── Never-Fail Routing Loop ────────────────────────────────────
	// The proxy never gives up on a request. It keeps trying models
	// until one succeeds, with session-aware prioritization.
	// Cap retries: enough attempts to try all models via fan-out + sequential
	maxAttempts := len(candidatePool) + 5
	if maxAttempts > 100 {
		maxAttempts = 100
	}
	attemptCount := 0

	for attemptCount < maxAttempts {
		attemptCount++

		// Phase 1: Fan-out preferred model + alternatives concurrently
		// This races the session's preferred model against 2-3 quality
		// alternatives. The first successful HIGH-QUALITY response wins.
		fanOutModels := plan.AllModels()
		var availableFanOut []engine.ModelCandidate
		for _, m := range fanOutModels {
			if !activeCooldowns[m.Provider] {
				availableFanOut = append(availableFanOut, m)
			}
		}

		if len(availableFanOut) == 0 {
			log.Printf("[ROUTER] All fan-out models on cooldown, waiting 2s (attempt %d)", attemptCount)
			time.Sleep(2 * time.Second)
			g.cooldownMu.Lock()
			now = time.Now()
			activeCooldowns = make(map[string]bool)
			for prov, until := range g.providerCooldown {
				if now.Before(until) {
					activeCooldowns[prov] = true
				} else {
					delete(g.providerCooldown, prov)
				}
			}
			g.cooldownMu.Unlock()
			plan = g.Sessions.GetRoutingPlan(session, candidatePool, activeCooldowns)
			continue
		}

		fanOutSize := len(availableFanOut)
		altNames := make([]string, 0, len(plan.Alternatives))
		for _, m := range plan.Alternatives {
			altNames = append(altNames, m.ID+"("+m.Provider+")")
		}
		log.Printf("[ROUTER] Fan-out %d models (session-aware): preferred=%s alts=%v",
			fanOutSize, plan.Preferred.ID, altNames)

		type fanResult struct {
			model       engine.ModelCandidate
			resp        *ProxyResponse
			isPreferred bool
			quality     float64
		}

		fanCh := make(chan fanResult, fanOutSize)
		for i := 0; i < fanOutSize; i++ {
			model := availableFanOut[i]
			sanitized := g.sanitizeRequestBody(model.Provider, body, hasTools, model.ID)
			isPref := model.ID == plan.Preferred.ID && model.Provider == plan.Preferred.Provider

			go func(m engine.ModelCandidate, s []byte, isPreferred bool) {
				// Per-provider rate limiting with 429 backoff
				if sem, ok := g.providerSems[m.Provider]; ok {
					select {
					case sem <- struct{}{}:
					case <-time.After(5 * time.Second):
						fanCh <- fanResult{
							model: m, resp: &ProxyResponse{Err: fmt.Errorf("provider %s: semaphore timeout", m.Provider)},
							isPreferred: isPreferred, quality: qualityScore(m),
						}
						return
					}
					defer func() { <-sem }()
				}

				// Quality models get more time to think
				timeout := 30 * time.Second
				if qualityScore(m) >= 2.0 {
					timeout = 60 * time.Second // Large models get more time
				}
				mc := &http.Client{Timeout: timeout}

				fanCh <- fanResult{
					model:       m,
					resp:        g.forwardRequest(mc, job.Request, m, s),
					isPreferred: isPreferred,
					quality:     qualityScore(m),
				}
			}(model, sanitized, isPref)
		}

		// Collect ALL fan-out results, then pick the BEST quality success
		var successes []fanResult
		for i := 0; i < fanOutSize; i++ {
			result := <-fanCh
			log.Printf("[ROUTER] Fan-out result: %s(%s) err=%v status=%d quality=%.1f pref=%v",
				result.model.ID, result.model.Provider, result.resp.Err, result.resp.Status,
				result.quality, result.isPreferred)

			if result.resp.Err != nil || result.resp.Status >= 400 {
				// Scale cooldown by error severity:
				// 401/402/403 = permanent auth failure, long cooldown (10 min)
				// 429 = rate limit, short cooldown (30s)
				// 5xx = server error, medium cooldown (2 min)
				// other = default (1 min)
				cooldown := 1 * time.Minute
				switch {
				case result.resp.Status == 401 || result.resp.Status == 402 || result.resp.Status == 403:
					cooldown = 10 * time.Minute
				case result.resp.Status == 429:
					cooldown = 30 * time.Second
				case result.resp.Status >= 500:
					cooldown = 2 * time.Minute
				}
				g.cooldownMu.Lock()
				g.providerCooldown[result.model.Provider] = time.Now().Add(cooldown)
				g.cooldownMu.Unlock()
				// Invalidate session's preferred model if it's rate-limited or failed
				if result.isPreferred {
					g.Sessions.InvalidatePreferred(session.ID, fmt.Sprintf("status %d", result.resp.Status))
				}
			}

			if result.resp.Err == nil && result.resp.Status < 400 {
				successes = append(successes, result)
			} else {
				if g.DB != nil && !isTransientError(result.resp.Status) {
					db.RecordFailure(g.DB, result.model.ID)
				}
			}
		}

		// ── Quality-First Selection with Continuity Bonus ───────────
		// Among successful responses, pick the highest quality model.
		// The preferred model gets a +0.5 continuity bonus so it's not
		// unnecessarily replaced by a marginally better alternative.
		if len(successes) > 0 {
			sort.Slice(successes, func(i, j int) bool {
				qi := successes[i].quality
				qj := successes[j].quality
				if successes[i].isPreferred {
					qi += 0.5 // Continuity bonus
				}
				if successes[j].isPreferred {
					qj += 0.5
				}
				return qi > qj
			})

			best := successes[0]
			log.Printf("[ROUTER] Selected %s(%s) quality=%.1f pref=%v (from %d successes)",
				best.model.ID, best.model.Provider, best.quality, best.isPreferred, len(successes))

			// Update session tracking
			g.Sessions.UpdatePreferred(session.ID, best.model)
			for _, s := range successes[1:] {
				g.Sessions.RecordExplorationResult(session.ID, s.model, true)
			}

			g.onSuccess(job, best.model, best.resp, body)
			return
		}

		// Phase 2: Sequential fallback through remaining candidates
		// (models not in the fan-out, sorted by quality)
		seenModels := make(map[string]bool)
		for _, m := range availableFanOut {
			seenModels[m.ID+"|"+m.Provider] = true
		}

		var remaining []engine.ModelCandidate
		for _, m := range candidatePool {
			key := m.ID + "|" + m.Provider
			if !seenModels[key] && !activeCooldowns[m.Provider] && m.Score >= 0 {
				remaining = append(remaining, m)
			}
		}
		sort.Slice(remaining, func(i, j int) bool {
			return qualityScore(remaining[i]) > qualityScore(remaining[j])
		})

		for _, model := range remaining {
			if attemptCount >= maxAttempts {
				break
			}
			attemptCount++

			sanitized := g.sanitizeRequestBody(model.Provider, body, hasTools, model.ID)
			timeout := 30 * time.Second
			if qualityScore(model) >= 2.0 {
				timeout = 60 * time.Second
			}
			mc := &http.Client{Timeout: timeout}
			proxyResp := g.forwardRequest(mc, job.Request, model, sanitized)

			if proxyResp.Err == nil && proxyResp.Status < 400 {
				log.Printf("[ROUTER] Sequential fallback succeeded: %s(%s) quality=%.1f",
					model.ID, model.Provider, qualityScore(model))
				g.Sessions.UpdatePreferred(session.ID, model)
				g.onSuccess(job, model, proxyResp, body)
				return
			}

			if proxyResp.Err != nil || proxyResp.Status >= 400 {
				cooldown := 1 * time.Minute
				switch {
				case proxyResp.Status == 401 || proxyResp.Status == 402 || proxyResp.Status == 403:
					cooldown = 10 * time.Minute
				case proxyResp.Status == 429:
					cooldown = 30 * time.Second
				case proxyResp.Status >= 500:
					cooldown = 2 * time.Minute
				}
				g.cooldownMu.Lock()
				g.providerCooldown[model.Provider] = time.Now().Add(cooldown)
				g.cooldownMu.Unlock()
			}

			if g.DB != nil && !isTransientError(proxyResp.Status) {
				db.RecordFailure(g.DB, model.ID)
			}
		}

		// Phase 3: Auto-recover circuit breakers and rebuild plan
		log.Printf("[ROUTER] All models failed in attempt %d, auto-recovering...", attemptCount)
		g.autoRecoverCircuitBreakers()

		g.mu.RLock()
		allModels = g.RankedModels
		g.mu.RUnlock()

		models = g.filterCandidates(allModels)
		if len(models) == 0 {
			models = allModels
		}
		if hasTools && len(toolModels) > 0 {
			candidatePool = append([]engine.ModelCandidate{}, toolModels...)
			if len(candidatePool) < 5 {
				candidatePool = append(candidatePool, plainModels...)
			}
		} else {
			candidatePool = models
		}

		g.cooldownMu.Lock()
		now = time.Now()
		activeCooldowns = make(map[string]bool)
		for prov, until := range g.providerCooldown {
			if now.Before(until) {
				activeCooldowns[prov] = true
			} else {
				delete(g.providerCooldown, prov)
			}
		}
		g.cooldownMu.Unlock()

		plan = g.Sessions.GetRoutingPlan(session, candidatePool, activeCooldowns)
		time.Sleep(1 * time.Second)
	}

	log.Printf("[ROUTER] CRITICAL: Exhausted all attempts for session %s", session.ID)
	job.Response <- &ProxyResponse{Err: fmt.Errorf("all models exhausted after %d attempts", attemptCount)}
}
