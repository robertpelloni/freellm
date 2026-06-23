package tokdiet

import (
	"net"
	"net/http"
	"time"
)

// tokdietPort is the local port tokdiet listens on.
const tokdietPort = "127.0.0.1:7787"

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
	if host == tokdietPort || host == "localhost:7787" {
		return next.RoundTrip(req)
	}

	// Capture the original base URL (e.g., https://api.openai.com)
	base := req.URL.Scheme + "://" + req.URL.Host
	req.Header.Set("x-ctxgov-upstream", base)

	// Re-route to the local tokdiet proxy
	req.URL.Scheme = "http"
	req.URL.Host = tokdietPort

	return next.RoundTrip(req)
}

// NewClient returns an http.Client pre-configured to route through tokdiet.
// If tokdiet is not reachable at 127.0.0.1:7787, the transport is disabled
// and requests go directly to upstream providers.
func NewClient(timeout time.Duration) *http.Client {
	enabled := probeTokdiet()
	if !enabled {
		// Return a direct client without tokdiet routing
		return &http.Client{
			Timeout:   timeout,
			Transport: &RoundTripper{Next: http.DefaultTransport, Enabled: false},
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &RoundTripper{Next: http.DefaultTransport, Enabled: true},
	}
}

// probeTokdiet checks if tokdiet is listening on its port.
func probeTokdiet() bool {
	conn, err := net.DialTimeout("tcp", tokdietPort, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
