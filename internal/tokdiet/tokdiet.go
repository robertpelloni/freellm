package tokdiet

import (
	"net/http"
	"time"
)

// RoundTripper intercepts all outbound requests and routes them through the
// tokdiet proxy (local port 7787), injecting the original destination into
// the x-ctxgov-upstream header.
type RoundTripper struct {
	Next    http.RoundTripper
	Enabled bool
}

func (t *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// If Next is nil, use the default transport
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}

	// Skip if disabled or already going to tokdiet
	if !t.Enabled {
		return next.RoundTrip(req)
	}

	host := req.URL.Host
	if host == "127.0.0.1:7787" || host == "localhost:7787" {
		return next.RoundTrip(req)
	}

	// Capture the original base URL (e.g., https://api.openai.com)
	base := req.URL.Scheme + "://" + req.URL.Host
	req.Header.Set("x-ctxgov-upstream", base)

	// Re-route to the local tokdiet proxy
	req.URL.Scheme = "http"
	req.URL.Host = "127.0.0.1:7787"

	return next.RoundTrip(req)
}

// NewClient returns an http.Client pre-configured to route through tokdiet.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &RoundTripper{Next: http.DefaultTransport, Enabled: true},
	}
}
