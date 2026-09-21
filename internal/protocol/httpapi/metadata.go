package httpapi

import (
	"crypto/rand"
	"fmt"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Protocol header names. They are part of the wire contract and are never
// Agent-specific.
const (
	requestIDHeader     = "X-Request-ID"
	correlationHeader   = "X-Correlation-ID"
	callerSubjectHeader = "X-Caller-Subject-ID"
	callerClientHeader  = "X-Caller-Client-ID"
	idempotencyHeader   = "Idempotency-Key"
	replayHeader        = "Idempotency-Replayed"
	maxOpaqueTokenBytes = 128
	maxIdentifierRunes  = 128
	maxCursorRunes      = 512
	minPageLimit        = 1
	maxPageLimit        = 200
	defaultPageLimit    = 50
)

// validOpaqueToken reports whether value is 1..128 visible ASCII bytes.
func validOpaqueToken(value string) bool {
	if len(value) == 0 || len(value) > maxOpaqueTokenBytes {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

// validIdentifier reports whether a path identifier is a non-empty string of at
// most 128 characters with no control characters.
func validIdentifier(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > maxIdentifierRunes {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// requestIDCounter is the fallback source used only if the cryptographic random
// source fails, which keeps request identification total.
var requestIDCounter uint64

// newRequestID returns a UUIDv4-shaped request identifier.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		n := atomic.AddUint64(&requestIDCounter, 1)
		stamp := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(stamp >> (8 * i))
			b[8+i] = byte(n >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
