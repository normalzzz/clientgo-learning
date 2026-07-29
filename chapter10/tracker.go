package main

import (
	"sync"
	"time"
)

// EventTracker gives every controller the same zero point for an informer
// event, making elapsed values in their logs directly comparable.
type EventTracker struct {
	mu     sync.RWMutex
	events map[string]event
}

type event struct {
	at              time.Time
	resourceVersion string
}

func NewEventTracker() *EventTracker {
	return &EventTracker{events: make(map[string]event)}
}

func (t *EventTracker) Mark(key, resourceVersion string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events[key] = event{at: time.Now(), resourceVersion: resourceVersion}
}

func (t *EventTracker) Since(key string) (time.Duration, string) {
	t.mu.RLock()
	current, ok := t.events[key]
	t.mu.RUnlock()
	if !ok {
		return 0, ""
	}
	return time.Since(current.at), current.resourceVersion
}
