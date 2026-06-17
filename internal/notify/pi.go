// Package notify ships freellm chain events ([TOKDIET] / [ROUTER] / [PROXY] /
// [SMOKE] / [SESSION] / …) to a running pi-coding-agent session so the events
// appear in the agent's transcript instead of (or in addition to) the
// freellm log files.
//
// Wiring is a one-liner in main.go:
//
//	log.SetOutput(notify.NewWriter(os.Stderr))
//
// When a pi session opens, the next-step-analyzer extension writes
// `<freellm-dir>/.pi-session.json` with the random port its HTTP receiver is
// listening on. This package reads that file (best-effort, refreshed
// periodically) and POSTs each parsed chain event to
// `http://127.0.0.1:<port>/event`. The original writer is also written to so
// the watchdog's stderr log keeps working unchanged.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SessionFile is the well-known file the pi extension writes to advertise
// the port of its HTTP receiver. Both sides agree on this absolute path so
// the freellm binary (which may be launched from a different cwd than the
// extension) can find it.
const SessionFile = `C:\Users\hyper\workspace\freellm\.pi-session.json`

const (
	httpTimeout     = 150 * time.Millisecond
	refreshInterval = 2 * time.Second
	queueDepth      = 256
)

// sessionInfo is the JSON shape written by the pi extension.
type sessionInfo struct {
	Port      int    `json:"port"`
	SessionID string `json:"sessionId"`
	PID       int    `json:"pid"`
	TS        int64  `json:"ts"`
}

// event is the per-line payload we POST to the pi session.
type event struct {
	Tag     string `json:"tag"`
	Message string `json:"message"`
	TS      int64  `json:"ts"`
}

// chainLineRE matches freellm chain log lines. Accepts both the timestamped
// form (Go's log default) and the bare form:
//
//	2026/06/17 11:14:40 [TOKDIET] Watchdog: port 7787 is back up
//	[ROUTER] Fan-out 4 models …
var chainLineRE = regexp.MustCompile(
	`^(?:\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}\s+)?\[([A-Z][A-Z0-9_]*)\]\s+(.*)$`,
)

// Writer is an io.Writer that tees its input to an inner writer (typically
// os.Stderr) and, for any line that looks like a freellm chain event, also
// enqueues it for async delivery to the pi session receiver.
type Writer struct {
	inner    io.Writer
	notifier *Notifier
}

// NewWriter returns a Writer that forwards to inner and notifies the pi
// session on chain events. The returned writer is safe for concurrent use
// (it's what the Go log package expects).
func NewWriter(inner io.Writer) *Writer {
	n := newNotifier(inner)
	return &Writer{inner: inner, notifier: n}
}

// Write implements io.Writer.
func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if n > 0 {
		// Strip trailing newline(s) so the regex doesn't get confused.
		line := strings.TrimRight(string(p[:n]), "\r\n")
		if m := chainLineRE.FindStringSubmatch(line); m != nil {
			w.notifier.enqueue(m[1], m[2])
		}
	}
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// Close flushes the in-flight queue and stops the background sender. The
// inner writer is not closed; callers retain ownership of it.
func (w *Writer) Close() error {
	return w.notifier.Close()
}

// Notifier is the async sender behind Writer.
type Notifier struct {
	inner     io.Writer
	mu        sync.RWMutex
	endpoint  string
	client    *http.Client
	queue     chan event
	closeOnce sync.Once
	closed    atomic.Bool
}

func newNotifier(inner io.Writer) *Notifier {
	n := &Notifier{
		inner:  inner,
		client: &http.Client{Timeout: httpTimeout},
		queue:  make(chan event, queueDepth),
	}
	// Prime the endpoint right away so the first event after a session
	// start doesn't race the file being written.
	n.refresh()
	go n.loop()
	// Background re-reader in case the session file is written *after*
	// freellm starts up (e.g. user opens pi after freellm is already
	// running, or the previous session ended and a new one just started).
	go n.refreshLoop()
	return n
}

func (n *Notifier) refreshLoop() {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for !n.closed.Load() {
		<-ticker.C
		if n.closed.Load() {
			return
		}
		n.refresh()
	}
}

func (n *Notifier) refresh() {
	data, err := os.ReadFile(SessionFile)
	if err != nil {
		n.setEndpoint("")
		return
	}
	var info sessionInfo
	if err := json.Unmarshal(data, &info); err != nil || info.Port <= 0 {
		n.setEndpoint("")
		return
	}
	n.setEndpoint(fmt.Sprintf("http://127.0.0.1:%d/event", info.Port))
}

func (n *Notifier) setEndpoint(ep string) {
	n.mu.Lock()
	n.endpoint = ep
	n.mu.Unlock()
}

func (n *Notifier) currentEndpoint() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.endpoint
}

func (n *Notifier) enqueue(tag, message string) {
	if n.closed.Load() {
		return
	}
	ev := event{Tag: tag, Message: message, TS: time.Now().UnixMilli()}
	select {
	case n.queue <- ev:
	default:
		// Queue full — drop the event so a slow pi session can't
		// back up the log writer. The watchdog log file still has it.
	}
}

func (n *Notifier) loop() {
	for ev := range n.queue {
		n.send(ev)
	}
}

func (n *Notifier) send(ev event) {
	endpoint := n.currentEndpoint()
	if endpoint == "" {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		// Receiver probably went away — drop the cached endpoint so
		// the next refresh tick tries the file again. We don't
		// hammer it from the hot path.
		n.setEndpoint("")
		return
	}
	resp.Body.Close()
}

// Close stops the background goroutines and discards any queued events.
func (n *Notifier) Close() error {
	n.closeOnce.Do(func() {
		n.closed.Store(true)
		close(n.queue)
	})
	return nil
}

// Ensure the package compiles even when the file is referenced via a
// go:generate directive elsewhere.
var _ = filepath.Separator
