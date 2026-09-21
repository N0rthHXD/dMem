package mcp

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
)

// inFlightRegistry tracks in-progress requests so a cancellation notification
// can stop the matching application call. Requests are registered by the read
// loop before the worker starts, which removes the race between dispatch and a
// cancellation that arrives immediately afterwards.
type inFlightRegistry struct {
	mutex   sync.Mutex
	entries map[string]*inFlightEntry
}

type inFlightEntry struct {
	cancel    context.CancelFunc
	cancelled bool
}

func newInFlightRegistry() *inFlightRegistry {
	return &inFlightRegistry{entries: map[string]*inFlightEntry{}}
}

// add registers one request. A duplicate identifier replaces the previous entry
// and cancels it so a malformed client cannot leak work.
func (r *inFlightRegistry) add(id string, cancel context.CancelFunc) {
	r.mutex.Lock()
	previous := r.entries[id]
	r.entries[id] = &inFlightEntry{cancel: cancel}
	r.mutex.Unlock()
	if previous != nil {
		previous.cancel()
	}
}

// cancelRequest marks one request cancelled and stops it. Unknown identifiers
// are ignored, which keeps cancellation notifications fire-and-forget.
func (r *inFlightRegistry) cancelRequest(id string) bool {
	r.mutex.Lock()
	entry, ok := r.entries[id]
	if ok {
		entry.cancelled = true
	}
	r.mutex.Unlock()
	if !ok {
		return false
	}
	entry.cancel()
	return true
}

// finish removes one request and reports whether it was cancelled, which means
// no response may be written for it.
func (r *inFlightRegistry) finish(id string) bool {
	r.mutex.Lock()
	entry, ok := r.entries[id]
	if ok {
		delete(r.entries, id)
	}
	r.mutex.Unlock()
	return ok && entry.cancelled
}

// generateID returns a UUIDv4-shaped identifier for MCP session identities.
func generateID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("mcp-%d", 0)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
