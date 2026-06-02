package engine

import (
	"fmt"
	"sync"
	"time"
)

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

type EventLogger struct {
	mu     sync.Mutex
	Logs   []LogEntry
	MaxLen int
}

func NewEventLogger(maxLen int) *EventLogger {
	return &EventLogger{
		MaxLen: maxLen,
		Logs:   make([]LogEntry, 0),
	}
}

func (l *EventLogger) Log(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := LogEntry{
		Timestamp: time.Now(),
		Message:   message,
	}
	l.Logs = append(l.Logs, entry)
	if len(l.Logs) > l.MaxLen {
		l.Logs = l.Logs[1:]
	}
	fmt.Printf("[%s] %s\n", entry.Timestamp.Format("15:04:05"), message)
}

func (l *EventLogger) GetLogs() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Return a copy
	res := make([]LogEntry, len(l.Logs))
	copy(res, l.Logs)
	return res
}
