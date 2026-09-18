package retrieval

import (
	"encoding/base64"
	"testing"

	"github.com/N0rthHXD/dMem/internal/gateway"
	core "github.com/N0rthHXD/dMem/internal/retrieval"
)

func testCursorParams() RecallParams {
	return RecallParams{
		Query: "status", SpaceIDs: []string{"space-1"}, States: []string{"active"},
		ReferenceTime: "2026-09-18T12:00:00Z", Channels: []string{"keyword"}, EffectiveChannels: []string{"keyword"},
		Limit: 5, MaxChars: 100, MaxContextTokens: 100, Plan: core.BuildQueryPlan("status", core.PlanOptions{}),
	}
}

func testCursorBinding() CursorBinding {
	return CursorBinding{Schema: "schema", QueryPlan: "plan", Membership: "membership", Ranking: "ranking", Graph: "graph", Context: "context", Feedback: "feedback"}
}

func TestCursorRoundTripBindsAllGenerations(t *testing.T) {
	service := NewService(Ports{}, Defaults{})
	params, binding := testCursorParams(), testCursorBinding()
	encoded, err := service.EncodeCursor(params, binding, CursorPosition{MemoryID: "memory-1", RevisionID: "revision-1", Score: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeCursor(encoded)
	if err != nil || validateCursor(state, params, binding) != nil {
		t.Fatalf("round trip state=%+v error=%v", state, err)
	}
	changes := []struct {
		name string
		set  func(*CursorBinding)
	}{
		{"schema", func(v *CursorBinding) { v.Schema = "changed" }},
		{"query plan", func(v *CursorBinding) { v.QueryPlan = "changed" }},
		{"membership", func(v *CursorBinding) { v.Membership = "changed" }},
		{"ranking", func(v *CursorBinding) { v.Ranking = "changed" }},
		{"graph", func(v *CursorBinding) { v.Graph = "changed" }},
		{"context", func(v *CursorBinding) { v.Context = "changed" }},
		{"feedback", func(v *CursorBinding) { v.Feedback = "changed" }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			next := binding
			change.set(&next)
			if err := validateCursor(state, params, next); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCursorDecoderRejectsMalformedAndUnknownFields(t *testing.T) {
	for _, value := range []string{"not-base64", base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"unknown":true}`))} {
		if _, err := decodeCursor(value); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
			t.Fatalf("decode %q error = %v", value, err)
		}
	}
}

func TestCursorStartRequiresExactRankedAnchor(t *testing.T) {
	service := NewService(Ports{}, Defaults{})
	params, binding := testCursorParams(), testCursorBinding()
	encoded, err := service.EncodeCursor(params, binding, CursorPosition{MemoryID: "memory-1", RevisionID: "revision-1", Score: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := decodeCursor(encoded)
	start, err := cursorStart([]Candidate{{MemoryID: "memory-1", RevisionID: "revision-1", Score: 0.5}, {MemoryID: "memory-2", RevisionID: "revision-2", Score: 0.4}}, state)
	if err != nil || start != 1 {
		t.Fatalf("start=%d error=%v", start, err)
	}
	if _, err := cursorStart([]Candidate{{MemoryID: "memory-1", RevisionID: "revision-1", Score: 0.4}}, state); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("changed anchor error = %v", err)
	}
}
