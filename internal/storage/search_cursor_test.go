package storage

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func testSearchIdentities() searchIdentities {
	return searchIdentities{
		Schema:     "test-schema",
		QueryPlan:  "test-query-plan",
		Membership: "test-membership",
		Ranking:    "test-ranking",
		Graph:      "test-graph",
		Context:    "test-context",
		Feedback:   "test-feedback",
	}
}

func testSearchParams() SearchParams {
	return SearchParams{Limit: 5, MaxChars: 100, ReferenceTime: "2026-08-21T10:00:00Z"}
}

func decodeCursorPayload(t *testing.T, cursor searchCursor) error {
	t.Helper()
	encoded, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = decodeSearchCursor(base64.RawURLEncoding.EncodeToString(encoded))
	return err
}

func TestSearchCursorRoundTripBindsEveryIdentity(t *testing.T) {
	identities := testSearchIdentities()
	params := testSearchParams()
	candidate := searchCandidate{memoryID: "memory-1", revisionID: "revision-1", score: 0.5}
	cursor, err := decodeSearchCursor(encodeSearchCursor("query-hash", params, identities, candidate))
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Version != 5 {
		t.Fatalf("cursor version = %d, want 5", cursor.Version)
	}
	if cursor.SchemaIdentity != identities.Schema || cursor.QueryPlanIdentity != identities.QueryPlan || cursor.MembershipIdentity != identities.Membership || cursor.RankingIdentity != identities.Ranking || cursor.GraphIdentity != identities.Graph || cursor.ContextIdentity != identities.Context || cursor.FeedbackIdentity != identities.Feedback {
		t.Fatalf("cursor identities = %+v, want %+v", cursor, identities)
	}
	if cursor.QueryHash != "query-hash" || cursor.MemoryID != "memory-1" || cursor.RevisionID != "revision-1" || cursor.Limit != params.Limit || cursor.MaxChars != params.MaxChars || cursor.ReferenceTime != params.ReferenceTime {
		t.Fatalf("cursor position = %+v", cursor)
	}
	if !searchCursorMatches(cursor, "query-hash", params, identities) {
		t.Fatal("round-tripped cursor did not match its own identities")
	}
}

func TestSearchCursorRejectsUnknownFields(t *testing.T) {
	valid := searchCursor{
		Version: 5, SchemaIdentity: "test-schema", QueryHash: "query-hash", QueryPlanIdentity: "test-query-plan",
		MembershipIdentity: "test-membership", RankingIdentity: "test-ranking", GraphIdentity: "test-graph",
		ContextIdentity: "test-context", FeedbackIdentity: "test-feedback", Limit: 5, MaxChars: 100,
		ScoreBits: "3fe0000000000000", MemoryID: "memory-1", RevisionID: "revision-1", ReferenceTime: "2026-08-21T10:00:00Z",
	}
	payload, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	fields["unknown"] = true
	payload, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSearchCursor(base64.RawURLEncoding.EncodeToString(payload)); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor with unknown field error = %v, want ErrInvalidCursor", err)
	}
}

func TestSearchCursorRequiresEveryIdentity(t *testing.T) {
	valid := searchCursor{
		Version: 5, SchemaIdentity: "test-schema", QueryHash: "query-hash", QueryPlanIdentity: "test-query-plan",
		MembershipIdentity: "test-membership", RankingIdentity: "test-ranking", GraphIdentity: "test-graph",
		ContextIdentity: "test-context", FeedbackIdentity: "test-feedback", Limit: 5, MaxChars: 100,
		ScoreBits: "3fe0000000000000", MemoryID: "memory-1", RevisionID: "revision-1", ReferenceTime: "2026-08-21T10:00:00Z",
	}
	if err := decodeCursorPayload(t, valid); err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
	missing := []struct {
		name   string
		mutate func(*searchCursor)
	}{
		{"schema", func(c *searchCursor) { c.SchemaIdentity = "" }},
		{"query plan", func(c *searchCursor) { c.QueryPlanIdentity = "" }},
		{"membership", func(c *searchCursor) { c.MembershipIdentity = "" }},
		{"ranking", func(c *searchCursor) { c.RankingIdentity = "" }},
		{"graph", func(c *searchCursor) { c.GraphIdentity = "" }},
		{"context", func(c *searchCursor) { c.ContextIdentity = "" }},
		{"feedback", func(c *searchCursor) { c.FeedbackIdentity = "" }},
	}
	for _, tc := range missing {
		t.Run(tc.name, func(t *testing.T) {
			cursor := valid
			tc.mutate(&cursor)
			if err := decodeCursorPayload(t, cursor); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("cursor without %s identity error = %v, want ErrInvalidCursor", tc.name, err)
			}
		})
	}
}

func TestSearchCursorInvalidatesWhenAnyIdentityChanges(t *testing.T) {
	identities := testSearchIdentities()
	params := testSearchParams()
	cursor, err := decodeSearchCursor(encodeSearchCursor("query-hash", params, identities, searchCandidate{memoryID: "memory-1", revisionID: "revision-1", score: 0.5}))
	if err != nil {
		t.Fatal(err)
	}
	if !searchCursorMatches(cursor, "query-hash", params, identities) {
		t.Fatal("cursor did not match unchanged identities")
	}
	changed := []struct {
		name   string
		mutate func(*searchIdentities)
	}{
		{"schema", func(i *searchIdentities) { i.Schema = "schema-v2" }},
		{"query plan", func(i *searchIdentities) { i.QueryPlan = "query-plan-v2" }},
		{"membership", func(i *searchIdentities) { i.Membership = "membership-v2" }},
		{"ranking", func(i *searchIdentities) { i.Ranking = "ranking-v2" }},
		{"graph", func(i *searchIdentities) { i.Graph = "graph-v2" }},
		{"context", func(i *searchIdentities) { i.Context = "context-v2" }},
		{"feedback", func(i *searchIdentities) { i.Feedback = "feedback-v2" }},
	}
	for _, tc := range changed {
		t.Run(tc.name, func(t *testing.T) {
			next := identities
			tc.mutate(&next)
			if searchCursorMatches(cursor, "query-hash", params, next) {
				t.Fatalf("cursor remained valid after %s identity change", tc.name)
			}
		})
	}
}

func TestSearchCursorRejectsNonFiniteScore(t *testing.T) {
	cursor := searchCursor{
		Version: 5, SchemaIdentity: "test-schema", QueryHash: "query-hash", QueryPlanIdentity: "test-query-plan",
		MembershipIdentity: "test-membership", RankingIdentity: "test-ranking", GraphIdentity: "test-graph",
		ContextIdentity: "test-context", FeedbackIdentity: "test-feedback", Limit: 5, MaxChars: 100,
		ScoreBits: "not-hex", MemoryID: "memory-1", RevisionID: "revision-1", ReferenceTime: "2026-08-21T10:00:00Z",
	}
	if err := decodeCursorPayload(t, cursor); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor with bad score bits error = %v, want ErrInvalidCursor", err)
	}
}
