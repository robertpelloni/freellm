//go:build !windows

package tray

// No-op tray implementation for Linux/macOS (headless).

import "github.com/robertpelloni/freellm/internal/proxy"

// Config holds display preferences.
type Config struct {
	ShowOnStart bool
}

// Event describes a single activity event for the tray log.
type Event struct {
	Tag     string
	Message string
}

// Run is a no-op on non-Windows platforms.
// Returns a channel of events for logging (unused on non-Windows).
func Run(events <-chan proxy.RouterEvent, _ Config) <-chan Event {
	// Drain and discard events on non-Windows
	go func() {
		for range events {
		}
	}()
	return nil
}
