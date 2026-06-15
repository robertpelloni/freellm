package proxy

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"time"

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

	// ── Never-Fail Routing Loop ────────────────────────────────────
	maxAttempts := len(allModels) + 5
	if maxAttempts > 100 {
		maxAttempts = 100
	}
	attemptCount := 0
	// Track unique provider failures for this request
	failedProviders := make(map[string]int)

	for attemptCount < maxAttempts {
		attemptCount++

		// Filter candidates dynamically based on current cooldowns/circuit breakers
		models := g.filterCandidates(allModels)
		if len(models) == 0 {
			log.Println("[ROUTER] All models circuit-broken, auto-recovering...")
			g.autoRecoverCircuitBreakers()
			models = allModels
		}

		hasTools, toolModels, plainModels := g.classifyRequest(body, models)

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

		// Phase 1: Survival-of-the-fittest Fan-out
		// Select N random models from the top 10 for parallel execution.
		topCount := 10
		if len(candidatePool) < topCount {
			topCount = len(candidatePool)
		}
		topPool := candidatePool[:topCount]
		
		// Randomly shuffle topPool to pick random models
		shuffledTop := make([]engine.ModelCandidate, len(topPool))
		copy(shuffledTop, topPool)
		for i := len(shuffledTop) - 1; i > 0; i-- {
			j := time.Now().UnixNano() % int64(i+1)
			shuffledTop[i], shuffledTop[j] = shuffledTop[j], shuffledTop[i]
		}
		
		fanOutSize := g.FanOutSize
		if fanOutSize > len(shuffledTop) {
			fanOutSize = len(shuffledTop)
		}
		availableFanOut := shuffledTop[:fanOutSize]

		if len(availableFanOut) == 0 {
			log.Printf("[ROUTER] No models available, auto-recovering...")
			g.autoRecoverCircuitBreakers()
			time.Sleep(5 * time.Second)
			continue
		}

		log.Printf("[ROUTER] Survival-of-the-fittest: Fan-out %d models randomly from top %d",
			len(availableFanOut), topCount)

		type fanResult struct {
			model       engine.ModelCandidate
			resp        *ProxyResponse
			quality     float64
		}

		// Collect remaining models for alternatives
		seenInFanOut := make(map[string]bool)
		for _, m := range availableFanOut {
			seenInFanOut[m.ID+"|"+m.Provider] = true
		}
		var remainingForAlts []engine.ModelCandidate
		for _, m := range candidatePool {
			if !seenInFanOut[m.ID+"|"+m.Provider] {
				remainingForAlts = append(remainingForAlts, m)
			}
		}

		fanCh := make(chan fanResult, len(availableFanOut))
		for i := 0; i < len(availableFanOut); i++ {
			model := availableFanOut[i]
			sanitized := g.sanitizeRequestBody(model.Provider, body, hasTools, model.ID)
			
			// Build alternatives for this fan-out model: other fan-out models + remaining models
			var alts []engine.ModelCandidate
			for j, other := range availableFanOut {
				if i != j {
					alts = append(alts, other)
				}
			}
			alts = append(alts, remainingForAlts...)

			go func(m engine.ModelCandidate, s []byte, alternatives []engine.ModelCandidate) {
				// Per-provider rate limiting
				if sem, ok := g.providerSems[m.Provider]; ok {
					select {
					case sem <- struct{}{}:
					case <-time.After(5 * time.Second):
						fanCh <- fanResult{
							model: m, resp: &ProxyResponse{Err: fmt.Errorf("provider %s: semaphore timeout", m.Provider)},
							quality: qualityScore(m),
						}
						return
					}
					defer func() { <-sem }()
				}

				// Streaming requests should NOT have a global client timeout
				// as they can last for minutes. We rely on context for header timeout.
				timeout := 0 * time.Second
				if !job.IsStream {
					timeout = 30 * time.Second
					if qualityScore(m) >= 2.0 {
						timeout = 60 * time.Second
					}
				}
				var transport *http.Transport
				if t, ok := http.DefaultTransport.(*http.Transport); ok {
					transport = t.Clone()
				} else {
					transport = &http.Transport{}
				}
				headerTimeout := 15 * time.Second
				if len(s) > 100000 {
					headerTimeout = 60 * time.Second
				} else if len(s) > 30000 {
					headerTimeout = 30 * time.Second
				}
				transport.ResponseHeaderTimeout = headerTimeout
				mc := &http.Client{
					Timeout:   timeout,
					Transport: transport,
				}

				resp := g.forwardRequestInternal(mc, job.Request, m, s, false, alternatives)
				
				// Track auth failures
				if resp.Status == 401 || resp.Status == 402 || resp.Status == 403 {
					log.Printf("[ROUTER] Auth failure (status %d) for %s(%s)", resp.Status, m.ID, m.Provider)
					g.recordProviderAuthFail(m.Provider)
				}

				if resp.Err != nil || resp.Status >= 400 {
					log.Printf("[ROUTER-DEBUG] Attempt failed for %s(%s): status=%d err=%v", m.ID, m.Provider, resp.Status, resp.Err)
				}

				fanCh <- fanResult{
					model:       m,
					resp:        resp,
					quality:     qualityScore(m),
				}
			}(model, sanitized, alts)
		}

		var successes []fanResult
		finishedCount := 0
		for finishedCount < len(availableFanOut) {
			res := <-fanCh
			finishedCount++

			if res.resp.Err == nil && res.resp.Status < 400 {
				if job.IsStream {
					// First success wins for streams
					if len(successes) == 0 {
						successes = append(successes, res)
						// For streams, we want to start immediately, but we must
						// drain the rest of the channel in the background to avoid leaks.
						go func(rem int) {
							for k := 0; k < rem; k++ {
								r := <-fanCh
								if r.resp != nil && r.resp.Stream != nil {
									r.resp.Stream.Close()
								}
							}
						}(len(availableFanOut) - finishedCount)
						break
					}
				} else {
					successes = append(successes, res)
				}
			} else {
				// Track provider failure for early abort
				failedProviders[res.model.Provider]++
				// Record failure for cooldown
				cooldown := 5 * time.Second
				if res.resp.Status == 429 {
					cooldown = 30 * time.Second
				} else if res.resp.Status == 503 || res.resp.Status == 504 {
					cooldown = 10 * time.Second
				}
				g.cooldownMu.Lock()
				g.providerCooldown[res.model.Provider] = time.Now().Add(cooldown)
				g.cooldownMu.Unlock()
			}
		}

		if len(successes) > 0 {
			sort.Slice(successes, func(i, j int) bool {
				return successes[i].quality > successes[j].quality
			})

			best := successes[0]
			log.Printf("[ROUTER] Selected winner: %s(%s) quality=%.1f (from %d successes)",
				best.model.ID, best.model.Provider, best.quality, len(successes))

			if g.ShuffleEnabled {
				g.ShuffleModels(best.model, availableFanOut)
			}

			g.onSuccess(job, best.model, best.resp, body)
			return
		}

		// Phase 2: Fallback Fan-out through remaining candidates
		sort.Slice(remainingForAlts, func(i, j int) bool {
			return qualityScore(remainingForAlts[i]) > qualityScore(remainingForAlts[j])
		})

		for i := 0; i < len(remainingForAlts); i += g.FanOutSize {
			if attemptCount >= maxAttempts {
				break
			}
			
			end := i + g.FanOutSize
			if end > len(remainingForAlts) {
				end = len(remainingForAlts)
			}
			batch := remainingForAlts[i:end]
			attemptCount += len(batch)
			
			log.Printf("[ROUTER] Fallback Fan-out: racing %d models", len(batch))
			
			fbCh := make(chan fanResult, len(batch))
			for _, model := range batch {
				sanitized := g.sanitizeRequestBody(model.Provider, body, hasTools, model.ID)
				
				// Alternatives for this fallback: rest of the batch + everything after
				var alts []engine.ModelCandidate
				for _, other := range batch {
					if other.ID != model.ID || other.Provider != model.Provider {
						alts = append(alts, other)
					}
				}
				if end < len(remainingForAlts) {
					alts = append(alts, remainingForAlts[end:]...)
				}

				go func(m engine.ModelCandidate, s []byte, a []engine.ModelCandidate) {
					timeout := 30 * time.Second
					if qualityScore(m) >= 2.0 {
						timeout = 60 * time.Second
					}
					var transport *http.Transport
					if t, ok := http.DefaultTransport.(*http.Transport); ok {
						transport = t.Clone()
					} else {
						transport = &http.Transport{}
					}
					headerTimeout := 15 * time.Second
					if len(s) > 100000 {
						headerTimeout = 60 * time.Second
					} else if len(s) > 30000 {
						headerTimeout = 30 * time.Second
					}
					transport.ResponseHeaderTimeout = headerTimeout
					mc := &http.Client{
						Timeout:   timeout,
						Transport: transport,
					}
					fbCh <- fanResult{
						model:   m,
						resp:    g.forwardRequestInternal(mc, job.Request, m, s, false, a),
						quality: qualityScore(m),
					}
				}(model, sanitized, alts)
			}
			
			var fbSuccesses []fanResult
			for j := 0; j < len(batch); j++ {
				res := <-fbCh
				if res.resp.Err == nil && res.resp.Status < 400 {
					fbSuccesses = append(fbSuccesses, res)
					if job.IsStream {
						break
					}
				} else {
					// Track provider failure for early abort
					failedProviders[res.model.Provider]++
					// Handle cooldowns for fallback failures
					cooldown := 5 * time.Second
					if res.resp.Status == 429 {
						cooldown = 30 * time.Second
					}
					g.cooldownMu.Lock()
					g.providerCooldown[res.model.Provider] = time.Now().Add(cooldown)
					g.cooldownMu.Unlock()
				}
			}
			
			if len(fbSuccesses) > 0 {
				sort.Slice(fbSuccesses, func(i, j int) bool {
					return fbSuccesses[i].quality > fbSuccesses[j].quality
				})
				
				best := fbSuccesses[0]
				log.Printf("[ROUTER] Fallback success: %s(%s)", best.model.ID, best.model.Provider)
				
				if g.ShuffleEnabled {
					g.ShuffleModels(best.model, batch)
				}
				
				g.onSuccess(job, best.model, best.resp, body)
				return
			}

			// Exit fallback loop after one batch to allow cooldowns to expire in outer loop sleep
			log.Printf("[ROUTER] Fallback batch failed, exiting to wait for cooldowns...")
			break
		}

		// Early abort if too many providers have failed
		if len(failedProviders) >= 20 {
			log.Printf("[ROUTER] Too many distinct provider failures (%d), aborting routing.", len(failedProviders))
			job.Response <- &ProxyResponse{Err: fmt.Errorf("too many provider failures (%d)", len(failedProviders))}
			return
		}

		log.Printf("[ROUTER] All models failed in attempt %d, auto-recovering...", attemptCount)
		g.autoRecoverCircuitBreakers()
		time.Sleep(5 * time.Second)
	}

	job.Response <- &ProxyResponse{Err: fmt.Errorf("all models exhausted after %d attempts", attemptCount)}
}
