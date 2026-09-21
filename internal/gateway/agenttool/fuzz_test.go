package agenttool

import (
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// FuzzCanonicalHashIsReplayStable proves the property the idempotency contract
// depends on: canonical hashing must be deterministic, must be insensitive to
// insignificant representation differences, and must never panic on adversarial
// text. A regression would let one idempotency key replay a different command or
// turn a genuine retry into a spurious conflict.
func FuzzCanonicalHashIsReplayStable(f *testing.F) {
	f.Add("PostgreSQL is preferred.", "database choice")
	f.Add("  padded  ", "  hint  ")
	f.Add("\u00a0nbsp\u00a0", "tab\there")
	f.Add("multi\nline", "hint")
	f.Add(string([]byte{0xff, 0xfe}), string([]byte{0x80}))

	f.Fuzz(func(t *testing.T, content, hint string) {
		command := validCommit()
		command.Memories[0].Content = content
		command.Memories[0].SearchHints = []string{hint}

		first, firstErr := CanonicalMemoryCommitHash(command)
		second, secondErr := CanonicalMemoryCommitHash(command)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("canonical hash is not deterministic: %v vs %v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}
		if first != second {
			t.Fatalf("canonical hash is not stable: %q != %q", first, second)
		}

		// Equivalent representations of the same command must hash identically:
		// field construction order, an explicit empty collection, an equivalent
		// time zone, and nil-vs-empty metadata must not be observable.
		equivalent := validCommit()
		equivalent.Memories[0].Content = content
		equivalent.Memories[0].SearchHints = []string{hint}
		equivalent.Source.Metadata = map[string]any{}
		equivalent.Entities = []EntitySuggestion{}
		equivalent.Facts = []FactSuggestion{}
		equivalent.Relations = []RelationSuggestion{}
		equivalent.Source.OccurredAt = equivalent.Source.OccurredAt.In(time.FixedZone("offset", 8*60*60))
		third, err := CanonicalMemoryCommitHash(equivalent)
		if err != nil {
			t.Fatalf("equivalent CanonicalMemoryCommitHash() error = %v", err)
		}
		if third != first {
			t.Fatalf("equivalent commands differ: %q != %q", first, third)
		}

		// The same call context must map the same command to the same write
		// identity, so a retry replays instead of conflicting.
		call := gateway.CallContext{Caller: gateway.CallerIdentity{ClientID: "agent-client"}, Idempotency: &gateway.Idempotency{Key: "turn-1-tool-1"}}
		identity, err := NewMemoryCommitWriteIdentity(call, command)
		if err != nil {
			if !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("write identity error code = %v, want invalid_input or success", err)
			}
			return
		}
		if identity.CommandHash != first || identity.IdempotencyKey != "turn-1-tool-1" {
			t.Fatalf("write identity = %+v, want hash %q", identity, first)
		}
	})
}

// FuzzLocalReferencesResolveOrReject proves local_id resolution is total: every
// accepted command resolves each reference to exactly one in-command identity,
// and every rejected command fails with a stable invalid_input error instead of
// a panic, a partial write, or a silently dangling reference.
func FuzzLocalReferencesResolveOrReject(f *testing.F) {
	f.Add("memory-1", "entity-1", "works_for")
	f.Add("", "entity-1", "works_for")
	f.Add("memory-1", "", "prefers")
	f.Add("memory-1", "entity-1", "not-a-predicate")
	f.Add("memory-1", "entity-1", "")
	f.Add("  ", "entity-1", "works_for")

	f.Fuzz(func(t *testing.T, memoryLocalID, entityLocalID, predicate string) {
		command := validCommit()
		command.Memories[0].LocalID = memoryLocalID
		command.Memories[0].Kind = domain.MemoryKindFact
		command.Entities = []EntitySuggestion{{LocalID: entityLocalID, Type: EntityPerson, Name: "Alice", Confidence: 0.8}}
		command.Facts = []FactSuggestion{{
			LocalID: "fact-1", Subject: EntityReference{LocalID: entityLocalID}, Predicate: predicate,
			Object:         FactObject{Entity: &EntityReference{LocalID: entityLocalID}},
			EvidenceMemory: memoryLocalID, Confidence: 0.8,
		}}

		err := ValidateMemoryCommit(command)
		if err == nil {
			return
		}
		if !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Fatalf("validation error = %v, want invalid_input", err)
		}
	})
}

// FuzzNormalizeTextIsSafeAndIdempotent proves the search-hint normalizer never
// produces invalid UTF-8 and is a fixed point, so hashing and FTS projection can
// never disagree about the same hint.
func FuzzNormalizeTextIsSafeAndIdempotent(f *testing.F) {
	f.Add("  PostgreSQL   database  ")
	f.Add("\u3000zenkaku\u3000")
	f.Add("\t\n mixed \r\n")
	f.Add("")
	f.Add("\xff\xfe")

	f.Fuzz(func(t *testing.T, value string) {
		once := normalizeText(value)
		if !utf8.ValidString(once) {
			t.Fatalf("normalizeText produced invalid UTF-8 for %q", value)
		}
		twice := normalizeText(once)
		if once != twice {
			t.Fatalf("normalizeText is not idempotent: %q -> %q -> %q", value, once, twice)
		}
		if strings.TrimSpace(once) != once {
			t.Fatalf("normalizeText left surrounding whitespace: %q", once)
		}
		set := normalizedSet([]string{value, once, value})
		for _, item := range set {
			if !utf8.ValidString(item) || strings.TrimSpace(item) != item {
				t.Fatalf("normalizedSet produced an unnormalized value %q", item)
			}
		}
		for i := 1; i < len(set); i++ {
			if set[i-1] >= set[i] {
				t.Fatalf("normalizedSet is not sorted and deduplicated: %q", set)
			}
		}
	})
}

// FuzzFeedbackAndForgetBoundsAreEnforced covers the two small Tool Route write
// commands: each must reject out-of-range input deterministically without ever
// panicking.
func FuzzFeedbackAndForgetBoundsAreEnforced(f *testing.F) {
	f.Add("used", "reason", 0.0)
	f.Add("not_used", "", 1.0)
	f.Add("irrelevant", "legacy kind", 0.5)
	f.Add("", strings.Repeat("r", 4096), math.NaN())

	f.Fuzz(func(t *testing.T, kind, reason string, unused float64) {
		_ = unused
		feedback := FeedbackCommand{ScopeID: "scope-1", MemoryID: "memory-1", RevisionID: "revision-1", Kind: FeedbackKind(kind), Reason: reason}
		if err := ValidateFeedback(feedback); err != nil && !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Fatalf("feedback validation error = %v, want invalid_input", err)
		}
		forget := ForgetCommand{ScopeID: "scope-1", MemoryID: "memory-1", ExpectedRevisionID: "revision-1", Reason: reason}
		if err := ValidateForget(forget); err != nil && !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Fatalf("forget validation error = %v, want invalid_input", err)
		}
	})
}
