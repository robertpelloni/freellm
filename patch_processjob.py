import sys

content = open("internal/proxy/server.go", "rb").read()

old_start = b"func (g *Gateway) processJob(job *RequestJob) {"
old_end_sig = b"func (g *Gateway) onSuccess(job *RequestJob, model engine.ModelCandidate, proxyResp *ProxyResponse, body []byte) {"

start_idx = content.find(old_start)
end_idx = content.find(old_end_sig)

if start_idx < 0 or end_idx < 0:
    print(f"ERROR: start={start_idx}, end={end_idx}")
    sys.exit(1)

print(
    f"Replacing processJob: chars {start_idx} to {end_idx} ({end_idx - start_idx} chars)"
)

new_func = b"""func (g *Gateway) processJob(job *RequestJob) {
\tg.mu.RLock()
\tallModels := g.RankedModels
\tg.mu.RUnlock()

\tif len(allModels) == 0 {
\t\tjob.Response <- &ProxyResponse{Err: fmt.Errorf("no models available")}
\t\treturn
\t}

\tbody, err := io.ReadAll(job.Request.Body)
\tif err != nil {
\t\tjob.Response <- &ProxyResponse{Err: fmt.Errorf("read body: %v", err)}
\t\treturn
\t}

\thasTools, toolModels, plainModels := g.classifyRequest(body)

\tmodels := g.filterCandidates(allModels)
\tif len(models) == 0 {
\t\tlog.Println("[ROUTER] All models circuit-broken, auto-recovering...")
\t\tg.autoRecoverCircuitBreakers()
\t\tmodels = allModels
\t}

\t// Build ordered attempt list
\tvar attemptOrder []engine.ModelCandidate
\tif hasTools && len(toolModels) > 0 {
\t\tattemptOrder = append(attemptOrder, toolModels...)
\t\tif len(attemptOrder) < 5 {
\t\t\tattemptOrder = append(attemptOrder, plainModels...)
\t\t}
\t} else {
\t\tattemptOrder = models
\t}

\tif len(attemptOrder) == 0 {
\t\tjob.Response <- &ProxyResponse{Err: fmt.Errorf("no models available")}
\t\treturn
\t}

\t// -- Phase 1: Concurrent Fan-Out --------------------------------
\t// Pick N diverse models (different providers preferred), send requests
\t// simultaneously, and race for the first success.
\tfanOut := g.FanOutCount
\tif fanOut < 1 {
\t\tfanOut = 1
\t}
\tif fanOut > len(attemptOrder) {
\t\tfanOut = len(attemptOrder)
\t}

\t// Round-robin rotation: offset starting position each request
\t// so traffic distributes evenly across the model pool.
\toffset := int(atomic.AddUint64(&g.rrCounter, 1) % uint64(len(attemptOrder)))
\tfanOutModels := g.selectDiverseModels(attemptOrder, offset, fanOut)

\ttype fanResult struct {
\t\tmodel engine.ModelCandidate
\t\tresp  *ProxyResponse
\t}
\tfanCh := make(chan fanResult, fanOut)
\tfanCtx, fanCancel := context.WithCancel(context.Background())
\tdefer fanCancel()

\tfor _, m := range fanOutModels {
\t\tgo func(model engine.ModelCandidate) {
\t\t\t// Check if another goroutine already won
\t\t\tselect {
\t\t\tcase <-fanCtx.Done():
\t\t\t\tfanCh <- fanResult{model: model, resp: &ProxyResponse{Err: fmt.Errorf("cancelled")}}
\t\t\t\treturn
\t\t\tdefault:
\t\t\t}
\t\t\tsanitized := g.sanitizeRequestBody(model.Provider, body, hasTools, model.ID)
\t\t\tmc := &http.Client{Timeout: 45 * time.Second}
\t\t\tproxyResp := g.forwardRequest(mc, job.Request, model, sanitized)
\t\t\tfanCh <- fanResult{model: model, resp: proxyResp}
\t\t}(m)
\t}

\t// Collect fan-out results: return on first success
\tvar fanErrors []error
\tfor i := 0; i < fanOut; i++ {
\t\tresult := <-fanCh
\t\tif result.resp.Err == nil && result.resp.Status < 400 {
\t\t\t// Winner! Cancel remaining fan-out goroutines
\t\t\tfanCancel()
\t\t\t// Drain remaining results
\t\t\tgo func() {
\t\t\t\tfor j := i + 1; j < fanOut; j++ {
\t\t\t\t\t<-fanCh
\t\t\t\t}
\t\t\t}()
\t\t\tg.onSuccess(job, result.model, result.resp, body)
\t\t\treturn
\t\t}
\t\t// Record failure for circuit breaker tracking
\t\tif g.DB != nil {
\t\t\tdb.RecordFailure(g.DB, result.model.ID)
\t\t}
\t\tif result.resp.Err != nil {
\t\t\tfanErrors = append(fanErrors, result.resp.Err)
\t\t} else {
\t\t\tfanErrors = append(fanErrors, fmt.Errorf("%s: status %d", result.model.ID, result.resp.Status))
\t\t}
\t}

\t// -- Phase 2: Sequential Fallback --------------------------------
\t// If all fan-out models failed, try remaining models sequentially.
\ttried := make(map[string]bool, fanOut)
\tfor _, m := range fanOutModels {
\t\ttried[m.ID+m.Provider] = true
\t}
\tvar fallbackOrder []engine.ModelCandidate
\tfor _, m := range attemptOrder {
\t\tif !tried[m.ID+m.Provider] {
\t\t\tfallbackOrder = append(fallbackOrder, m)
\t\t}
\t}

\tclient := g.Client
\tif client == nil {
\t\tclient = &http.Client{Timeout: 120 * time.Second}
\t}

\tvar lastErr error
\tmaxFallback := minInt(len(fallbackOrder), 5)
\tfor i := 0; i < maxFallback; i++ {
\t\tmodel := fallbackOrder[i]
\t\tsanitized := g.sanitizeRequestBody(model.Provider, body, hasTools, model.ID)
\t\tproxyResp := g.forwardRequest(client, job.Request, model, sanitized)
\t\tif proxyResp.Err == nil && proxyResp.Status < 400 {
\t\t\tg.onSuccess(job, model, proxyResp, body)
\t\t\treturn
\t\t}
\t\tif g.DB != nil {
\t\t\tdb.RecordFailure(g.DB, model.ID)
\t\t}
\t\tif proxyResp.Err != nil {
\t\t\tlastErr = proxyResp.Err
\t\t} else {
\t\t\tlastErr = fmt.Errorf("%s: status %d", model.ID, proxyResp.Status)
\t\t}
\t\tif i < maxFallback-1 {
\t\t\ttime.Sleep(50 * time.Millisecond)
\t\t}
\t}

\t// -- Phase 3: Circuit Breaker Recovery + Last Resort -------------
\tif time.Since(g.cbLogTime) > 5*time.Minute {
\t\tlog.Println("[ROUTER] All models failed in fan-out + fallback, recovering and retrying...")
\t}
\tg.autoRecoverCircuitBreakers()
\tretryModels := g.filterCandidates(allModels)
\tif len(retryModels) == 0 {
\t\tretryModels = allModels
\t}
\tmaxRetry := minInt(len(retryModels), 3)
\tfor i := 0; i < maxRetry; i++ {
\t\tmodel := retryModels[i]
\t\tsanitized := g.sanitizeRequestBody(model.Provider, body, hasTools, model.ID)
\t\tproxyResp := g.forwardRequest(client, job.Request, model, sanitized)
\t\tif proxyResp.Err == nil && proxyResp.Status < 400 {
\t\t\tg.onSuccess(job, model, proxyResp, body)
\t\t\treturn
\t\t}
\t\tlastErr = proxyResp.Err
\t\tif proxyResp.Err == nil {
\t\t\tlastErr = fmt.Errorf("%s: status %d", model.ID, proxyResp.Status)
\t\t}
\t}

\tif job.DBID > 0 {
\t\tdb.DequeueRequest(g.DB, job.DBID)
\t}
\ttotal := fanOut + maxFallback + maxRetry
\tif lastErr != nil {
\t\tjob.Response <- &ProxyResponse{Err: fmt.Errorf("all %d models failed (fan=%d, fallback=%d, retry=%d): %v",
\t\t\ttotal, fanOut, maxFallback, maxRetry, lastErr)}
\t} else {
\t\tjob.Response <- &ProxyResponse{Err: fmt.Errorf("all %d models failed: fan-out errors: %v",
\t\t\ttotal, fanErrors)}
\t}
}

// selectDiverseModels picks N models starting at offset, preferring different providers.
// This ensures the fan-out hits different backends, avoiding sending all requests
// to the same rate-limited provider.
func (g *Gateway) selectDiverseModels(candidates []engine.ModelCandidate, offset, count int) []engine.ModelCandidate {
\tif count >= len(candidates) {
\t\treturn candidates
\t}
\tseen := make(map[string]bool)
\tvar result []engine.ModelCandidate
\tn := len(candidates)
\tfor i := 0; i < n && len(result) < count; i++ {
\t\tidx := (offset + i) % n
\t\tm := candidates[idx]
\t\tprovKey := m.Provider
\t\tif !seen[provKey] || len(result) < (count+1)/2 {
\t\t\tresult = append(result, m)
\t\t\tseen[provKey] = true
\t\t}
\t}
\t// If we don't have enough diverse providers, fill with remaining
\tif len(result) < count {
\t\tfor i := 0; i < n && len(result) < count; i++ {
\t\t\tidx := (offset + i) % n
\t\t\tm := candidates[idx]
\t\t\tfound := false
\t\t\tfor _, r := range result {
\t\t\t\tif r.ID == m.ID && r.Provider == m.Provider {
\t\t\t\t\tfound = true
\t\t\t\t\tbreak
\t\t\t\t}
\t\t\t}
\t\t\tif !found {
\t\t\t\tresult = append(result, m)
\t\t\t}
\t\t}
\t}
\treturn result
}

"""

content = content[:start_idx] + new_func + content[end_idx:]
open("internal/proxy/server.go", "wb").write(content)
print(f"Patched! New function is {len(new_func)} bytes")
