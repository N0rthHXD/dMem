package agenttool

import (
	"math"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

func validCommit() MemoryCommitCommand {
	return MemoryCommitCommand{
		ScopeID:  "scope-1",
		Source:   Source{Type: "agent_tool", Name: "assistant", ConversationID: "conv-1", TurnID: "turn-1", OccurredAt: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)},
		Memories: []MemorySuggestion{{LocalID: "memory-1", Kind: domain.MemoryKindPreference, Content: "PostgreSQL is preferred.", Sensitivity: SensitivityNormal, Confidence: 0.9, SearchHints: []string{"database choice"}}},
	}
}

func TestValidateMemoryCommit(t *testing.T) {
	if err := ValidateMemoryCommit(validCommit()); err != nil {
		t.Fatalf("valid commit: %v", err)
	}
	tests := map[string]func(*MemoryCommitCommand){
		"forbidden source":                   func(c *MemoryCommitCommand) { c.Source.Type = "provider" },
		"missing memory":                     func(c *MemoryCommitCommand) { c.Memories = nil },
		"confidence":                         func(c *MemoryCommitCommand) { c.Memories[0].Confidence = math.NaN() },
		"aggregate remains valid suggestion": func(c *MemoryCommitCommand) { c.Memories[0].Kind = domain.MemoryKindScene },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			command := validCommit()
			mutate(&command)
			err := ValidateMemoryCommit(command)
			if name == "aggregate remains valid suggestion" {
				if err != nil {
					t.Fatalf("aggregate must remain a proposal-eligible suggestion: %v", err)
				}
				return
			}
			if !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("expected invalid_input, got %v", err)
			}
		})
	}
}

func TestValidateLocalReferences(t *testing.T) {
	command := validCommit()
	command.Entities = []EntitySuggestion{{LocalID: "entity-1", Type: EntityProduct, Name: "PostgreSQL", Confidence: 0.8}}
	command.Facts = []FactSuggestion{{
		LocalID: "fact-1", Subject: EntityReference{EntityID: "entity-user"}, Predicate: "prefers",
		Object: FactObject{Entity: &EntityReference{LocalID: "entity-1"}}, EvidenceMemory: "memory-1", Confidence: 0.8,
	}}
	if err := ValidateMemoryCommit(command); err != nil {
		t.Fatalf("valid references: %v", err)
	}
	command.Facts[0].EvidenceMemory = "missing"
	if err := ValidateMemoryCommit(command); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("expected invalid reference, got %v", err)
	}
}

func TestCanonicalHashNormalizesEquivalentCommands(t *testing.T) {
	first := validCommit()
	first.Memories[0].SearchHints = []string{" PostgreSQL   database ", "database choice", "database choice"}
	first.Source.Metadata = nil
	second := validCommit()
	second.Memories[0].SearchHints = []string{"database choice", "PostgreSQL database"}
	second.Source.Metadata = map[string]any{}
	second.Entities = []EntitySuggestion{}
	second.Facts = []FactSuggestion{}
	second.Relations = []RelationSuggestion{}
	zone := time.FixedZone("offset", 8*60*60)
	second.Source.OccurredAt = first.Source.OccurredAt.In(zone)
	firstHash, err := CanonicalMemoryCommitHash(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := CanonicalMemoryCommitHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("equivalent commands differ: %s != %s", firstHash, secondHash)
	}
	second.Memories[0].Content = "SQLite is preferred."
	changed, _ := CanonicalMemoryCommitHash(second)
	if changed == firstHash {
		t.Fatal("different content must change the hash")
	}
}

func TestWriteIdentitySupportsReplayAndConflictDetection(t *testing.T) {
	call := gateway.CallContext{Caller: gateway.CallerIdentity{ClientID: "agent-client"}, Idempotency: &gateway.Idempotency{Key: "turn-1-tool-1"}}
	first, err := NewMemoryCommitWriteIdentity(call, validCommit())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := NewMemoryCommitWriteIdentity(call, validCommit())
	if err != nil {
		t.Fatal(err)
	}
	if first.CommandHash != replay.CommandHash || first.IdempotencyKey != replay.IdempotencyKey {
		t.Fatal("equivalent retries must produce the same write identity")
	}
	changed := validCommit()
	changed.Memories[0].Content = "SQLite is preferred."
	conflict, err := NewMemoryCommitWriteIdentity(call, changed)
	if err != nil {
		t.Fatal(err)
	}
	if first.CommandHash == conflict.CommandHash {
		t.Fatal("same key with a different command must be detectable as an idempotency conflict")
	}
}

func TestFeedbackAndForgetBoundaries(t *testing.T) {
	feedback := FeedbackCommand{ScopeID: "scope", MemoryID: "memory", RevisionID: "revision", Kind: FeedbackNotUsed}
	if err := ValidateFeedback(feedback); err != nil {
		t.Fatal(err)
	}
	feedback.Kind = "irrelevant"
	if err := ValidateFeedback(feedback); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("legacy feedback must be rejected: %v", err)
	}
	forget := ForgetCommand{ScopeID: "scope", MemoryID: "memory", ExpectedRevisionID: "revision", Reason: "requested by user"}
	if err := ValidateForget(forget); err != nil {
		t.Fatal(err)
	}
	forget.Reason = ""
	if err := ValidateForget(forget); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("empty reason must be rejected: %v", err)
	}
}
