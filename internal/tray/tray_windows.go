//go:build windows

package tray

import (
	_ "embed"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
	"github.com/robertpelloni/freellm/internal/proxy"
)

//go:embed green.ico
var icoGreen []byte

//go:embed gray.ico
var icoGray []byte

//go:embed red.ico
var icoRed []byte

//go:embed yellow.ico
var icoYellow []byte

// Config holds display preferences.
type Config struct {
	ShowOnStart bool
}

// Event describes a single activity event for the tray log.
type Event struct {
	Tag     string
	Message string
	Time    time.Time
}

// Run starts the system tray icon. Blocks until the tray exits.
// events is the Gateway's router event channel.
// Returns a channel of all seen events (for external logging).
func Run(events <-chan proxy.RouterEvent, cfg Config) <-chan Event {
	eventLog := make(chan Event, 512)

	go func() {
		systray.Run(
			func() { onReady(events, eventLog, cfg) },
			func() { close(eventLog) },
		)
	}()

	return eventLog
}

var (
	txCount     atomic.Int64
	txCounter   int64
	isActive    atomic.Bool
	lastTag     string
	lastMsg     string
	lastEventMu sync.RWMutex

	currentState string // "idle", "tx", "rx", "error"

	eventBuf   []Event
	eventBufMu sync.RWMutex
	maxEvents  = 500
)

func onReady(events <-chan proxy.RouterEvent, eventLog chan<- Event, cfg Config) {
	systray.SetIcon(icoGray)
	systray.SetTooltip("FreeLLM Proxy — Idle")

	// Menu items
	mShowLog := systray.AddMenuItem("Show Activity Log", "View recent events")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop FreeLLM")

	// Activity processing loop
	go func() {
		for ev := range events {
			now := time.Now()
			tag := ev.Tag
			msg := ev.Message

			event := Event{Tag: tag, Message: msg, Time: now}
			select {
			case eventLog <- event:
			default:
			}

			eventBufMu.Lock()
			eventBuf = append(eventBuf, event)
			if len(eventBuf) > maxEvents {
				eventBuf = eventBuf[len(eventBuf)-maxEvents:]
			}
			eventBufMu.Unlock()

			isTX := containsAny(msg, "Sending request", "fanning out", "Attempt")
			isRX := containsAny(msg, "Got response", "success", "Routed to")
			isErr := containsAny(msg, "failed", "error", "timeout", "cooldown", "rate-limited")
			isIdle := containsAny(msg, "No fresh", "Resetting")

			lastEventMu.Lock()
			lastTag = tag
			lastMsg = msg
			lastEventMu.Unlock()

			switch {
			case isTX:
				txCounter++
				txCount.Store(int64(txCounter))
				isActive.Store(true)
				currentState = "tx"
				setIcon(icoGreen)
				systray.SetTooltip(fmt.Sprintf("FreeLLM — TX %d", txCounter))

			case isRX:
				isActive.Store(false)
				currentState = "rx"
				setIcon(icoYellow)

			case isErr:
				currentState = "error"
				setIcon(icoRed)
				systray.SetTooltip(fmt.Sprintf("FreeLLM — Error: %s", truncate(msg, 45)))

			case isIdle:
				isActive.Store(false)
				currentState = "idle"
				setIcon(icoGray)
				systray.SetTooltip(fmt.Sprintf("FreeLLM — Idle (TX: %d)", txCounter))
			}
		}
	}()

	// Menu action handlers
	go func() {
		for {
			select {
			case <-mShowLog.ClickedCh:
				eventBufMu.RLock()
				count := len(eventBuf)
				last := "no events"
				if count > 0 {
					e := eventBuf[count-1]
					last = fmt.Sprintf("[%s] %s: %s", e.Time.Format("15:04:05"), e.Tag, truncate(e.Message, 60))
				}
				eventBufMu.RUnlock()
				systray.SetTooltip(fmt.Sprintf("FreeLLM — %d events | Last: %s", count, last))
				log.Printf("[TRAY] Activity: %d events, latest: %s", count, last)

			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

var currentIcon []byte

func setIcon(ico []byte) {
	if len(currentIcon) > 0 && len(currentIcon) == len(ico) {
		return // skip redundant updates
	}
	currentIcon = ico
	systray.SetIcon(ico)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strContains(s, sub) {
			return true
		}
	}
	return false
}

func strContains(s, substr string) bool {
	return len(s) >= len(substr) && containsSlow(s, substr)
}

func containsSlow(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
