package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// TestSearchCursorTruncatesWhenRankedWindowFull pins the window pagination
// contract: the ranked window is retained in full, a full window produces a
// NextCursor, and the following page continues past the window anchor.
func TestSearchCursorTruncatesWhenRankedWindowFull(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	createMatchingMemories(t, repository, "window-full", "windowword", minSelectionWindow+1)
	params := SearchParams{Query: "windowword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword"}, Limit: 2, MaxChars: 1000}
	first, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != selectionWindow(params.Limit) || !first.Truncated || first.NextCursor == nil {
		t.Fatalf("first page results=%d truncated=%t cursor=%v", len(first.Results), first.Truncated, first.NextCursor)
	}
	params.Cursor = *first.NextCursor
	second, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Results) != 1 || second.Results[0].Memory.MemoryID == first.Results[0].Memory.MemoryID {
		t.Fatalf("second page = %+v", second.Results)
	}
}

// TestContextSelectionBackfillsRejectedHeadFromWindow proves the selector can
// fill MaxResults from candidates ranked past the first MaxResults. The head is
// a duplicate pair that collapses to one representative; later candidates must
// backfill the remaining slots.
func TestContextSelectionBackfillsRejectedHeadFromWindow(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	now := "2026-08-21T11:00:00Z"
	for i := 0; i < 3; i++ {
		if _, err := repository.store.writer.Exec(`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("backfill-event-%d", i), fmt.Sprintf("backfill-op-%d", i), "space-1", "content-1", "manual", "test", "seed", now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	create := func(key, content, eventID string) domain.Memory {
		start, end := int64(1), int64(5)
		params := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: content, Sensitivity: "normal", Evidence: []EvidenceParams{{EventID: eventID, StartOffset: &start, EndOffset: &end}}, Reason: "backfill"}
		memory, replay, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
		if err != nil || replay {
			t.Fatalf("create backfill memory = %+v replay=%t err=%v", memory, replay, err)
		}
		return memory
	}
	create("backfill-dup-a", "zephyr backfill", "backfill-event-0")
	create("backfill-dup-b", "zephyr backfill", "backfill-event-0")
	later := create("backfill-later", "zephyr backfill later concept", "backfill-event-1")
	last := create("backfill-last", "zephyr backfill last concept", "backfill-event-2")

	params := selectionSearchParams("zephyr backfill", false)
	params.Limit = 2
	response, err := repository.SearchMemories(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) <= params.Limit {
		t.Fatalf("ranked window = %d, want more than MaxResults %d", len(response.Results), params.Limit)
	}
	if len(response.Context.Items) != params.Limit {
		t.Fatalf("selected context = %d items, want %d: %+v", len(response.Context.Items), params.Limit, response.Context.Items)
	}
	backfilled := map[string]bool{later.MemoryID: true, last.MemoryID: true}
	var found bool
	for _, item := range response.Context.Items {
		if backfilled[item.MemoryID] {
			found = true
		}
	}
	if !found {
		t.Fatalf("selected context did not backfill a later-ranked candidate: %+v", response.Context.Items)
	}
}
