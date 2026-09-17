package storage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func candidateParams() CreateCandidateParams {
	confidence := 0.9
	return CreateCandidateParams{
		EventID: "event-1", SpaceID: "space-1", MemoryType: "fact", Content: "candidate searchable only after confirmation",
		Confidence: &confidence, Sensitivity: "normal", SuggestionKind: "new", ProcessorIdentity: "processor/test",
		ProviderIdentity: stringPointer("provider/test"), ModelIdentity: stringPointer("model/test"), ProcessingVersion: "candidate-v1",
		Evidence: directEvidence("event-1"),
	}
}

func createTestCandidate(t *testing.T, repository *Repository) domain.Candidate {
	t.Helper()
	candidate, duplicate, err := repository.CreateCandidate(context.Background(), candidateParams())
	if err != nil || duplicate {
		t.Fatalf("CreateCandidate() = %+v, duplicate=%t, error=%v", candidate, duplicate, err)
	}
	return candidate
}

func candidateIdentity(key, action, candidateID string, params ReviewCandidateParams) WriteIdentity {
	value := struct {
		CandidateID string
		ReviewCandidateParams
	}{candidateID, params}
	return memoryIdentity(key, "/v1/candidates/{candidate_id}/"+action, value)
}

func TestCandidatePersistsIndependentlyDeduplicatesAndIsImmutable(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	if candidate.ReviewState != "pending" || len(candidate.Evidence) != 1 || candidate.ProcessorIdentity != "processor/test" || candidate.SpecificationHash == "" {
		t.Fatalf("candidate = %+v", candidate)
	}
	duplicate, replay, err := repository.CreateCandidate(context.Background(), candidateParams())
	if err != nil || !replay || !reflect.DeepEqual(duplicate, candidate) {
		t.Fatalf("duplicate candidate = %+v, replay=%t, error=%v", duplicate, replay, err)
	}
	for table, want := range map[string]int{"candidates": 1, "candidate_evidence": 1, "memories": 0, "memory_projections": 0, "memory_fts": 0} {
		var got int
		if err := repository.store.reader.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count = %d, %v; want %d", table, got, err, want)
		}
	}
	if _, err := repository.store.writer.Exec(`UPDATE candidates SET content='mutated' WHERE candidate_id=?`, candidate.CandidateID); err == nil {
		t.Fatal("candidate specification update succeeded")
	}
	if _, err := repository.store.writer.Exec(`DELETE FROM candidate_evidence WHERE candidate_id=?`, candidate.CandidateID); err == nil {
		t.Fatal("candidate evidence delete succeeded")
	}
}

func TestCandidateRejectsCrossSpaceAndOutOfRangeEvidence(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	now := "2026-08-21T11:00:00Z"
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES ('space-sibling', 'space-1', 'project', 'candidate-sibling', 'Sibling', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO raw_contents(content_id, content_kind, content_text, media_type, content_hash, size_bytes, created_at) VALUES ('content-sibling', 'text', 'sibling event', 'text/plain', 'candidate-sibling-hash', 13, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-sibling', 'candidate-sibling-op', 'space-sibling', 'content-sibling', 'manual', 'test', 'seed', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	start, end := int64(0), int64(11)
	for _, test := range []struct {
		name     string
		evidence []EvidenceParams
	}{
		{"cross-space", []EvidenceParams{{EventID: "event-sibling"}}},
		{"out-of-range", []EvidenceParams{{EventID: "event-1", StartOffset: &start, EndOffset: &end}}},
	} {
		params := candidateParams()
		params.Content = test.name
		params.Evidence = test.evidence
		if _, _, err := repository.CreateCandidate(context.Background(), params); !errors.Is(err, ErrInvalidEvidence) {
			t.Fatalf("CreateCandidate(%s) error = %v; want ErrInvalidEvidence", test.name, err)
		}
	}
}

func TestCandidatePersistsStructuredRevisionThroughConfirmation(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	eventStart := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	eventEnd := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
	validFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	validUntil := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	confidence := 0.9
	structured := domain.MemoryRevision{
		Kind: domain.MemoryKindFact, DisplayContent: "Alice prefers tea", CanonicalContent: "alice prefers tea",
		SubjectEntityIDs: []string{"alice"}, ObjectEntityIDs: []string{"tea"}, EventType: "preference",
		Attributes:     map[string]string{"preference": "tea"},
		EventTimeStart: &eventStart, EventTimeEnd: &eventEnd, TimePrecision: domain.TimePrecisionExact, TimeSource: "relative_anchor",
		ValidFrom: &validFrom, ValidUntil: &validUntil, SourceSpeaker: "alice", Language: "en",
		Sensitivity: "normal", Specificity: 0.7, Confidence: 0.9,
		Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-1", EventID: "event-1", Role: domain.EvidenceRoleDirect}},
	}
	params := candidateParams()
	params.Content = "Alice prefers tea"
	params.Confidence = &confidence
	params.StructuredRevision = &structured
	candidate, duplicate, err := repository.CreateCandidate(context.Background(), params)
	if err != nil || duplicate {
		t.Fatalf("CreateCandidate() = %+v, duplicate=%t, error=%v", candidate, duplicate, err)
	}
	var stored string
	if err := repository.store.reader.QueryRow(`SELECT structured_revision_json FROM candidates WHERE candidate_id=?`, candidate.CandidateID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || stored == "{}" {
		t.Fatalf("structured revision was not persisted: %q", stored)
	}
	review := ReviewCandidateParams{ReasonCode: "accepted"}
	identity := candidateIdentity("structured-confirm", "confirm", candidate.CandidateID, review)
	memory, _, err := repository.ConfirmCandidate(context.Background(), identity, candidate.CandidateID, review)
	if err != nil {
		t.Fatal(err)
	}
	revision := memory.CurrentRevision
	if revision.CanonicalContent != "alice prefers tea" || len(revision.SubjectEntityIDs) != 1 || revision.SubjectEntityIDs[0] != "alice" || len(revision.ObjectEntityIDs) != 1 || revision.ObjectEntityIDs[0] != "tea" {
		t.Fatalf("canonical structured fields lost: %+v", revision)
	}
	if revision.EventTimeStart == nil || revision.EventTimeEnd == nil || revision.TimePrecision != domain.TimePrecisionExact || revision.TimeSource != "relative_anchor" {
		t.Fatalf("event time lost: %+v", revision)
	}
	if revision.SourceSpeaker != "alice" || revision.Language != "en" || revision.Attributes["preference"] != "tea" {
		t.Fatalf("speaker/language/attributes lost: %+v", revision)
	}
	if len(revision.Evidence) != 1 || revision.Evidence[0].Role != domain.EvidenceRoleDirect {
		t.Fatalf("evidence role lost: %+v", revision.Evidence)
	}
}

func TestCandidateConfirmCreatesExactlyOneActiveMemoryAtomicallyAndReplays(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	params := ReviewCandidateParams{ReasonCode: "accepted", Note: stringPointer("reviewed")}
	identity := candidateIdentity("confirm", "confirm", candidate.CandidateID, params)
	memory, replay, err := repository.ConfirmCandidate(context.Background(), identity, candidate.CandidateID, params)
	if err != nil || replay {
		t.Fatalf("ConfirmCandidate() = %+v, replay=%t, error=%v", memory, replay, err)
	}
	if memory.CurrentState != "active" || memory.CurrentRevision.DisplayContent != candidate.Content || !reflect.DeepEqual(memory.CurrentRevision.Confidence, *candidate.Confidence) || len(memory.CurrentRevision.Evidence) != len(candidate.Evidence) {
		t.Fatalf("confirmed memory = %+v", memory)
	}
	confirmed, err := repository.GetCandidate(context.Background(), candidate.CandidateID)
	if err != nil || confirmed.ReviewState != "confirmed" || confirmed.ConfirmedMemoryID == nil || *confirmed.ConfirmedMemoryID != memory.MemoryID || len(confirmed.Reviews) != 1 || confirmed.Reviews[0].ToState != "confirmed" {
		t.Fatalf("confirmed candidate = %+v, %v", confirmed, err)
	}
	for table, want := range map[string]int{"memories": 1, "memory_revisions": 1, "memory_evidence": 1, "memory_state_events": 1, "memory_projections": 1, "memory_fts": 1, "candidate_reviews": 1} {
		var got int
		if err := repository.store.reader.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count = %d, %v; want %d", table, got, err, want)
		}
	}
	replayed, replay, err := repository.ConfirmCandidate(context.Background(), identity, candidate.CandidateID, params)
	if err != nil || !replay || !reflect.DeepEqual(replayed, memory) {
		t.Fatalf("confirm replay = %+v, replay=%t, error=%v", replayed, replay, err)
	}
	if _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("second-confirm", "confirm", candidate.CandidateID, params), candidate.CandidateID, params); !errors.Is(err, ErrCandidateReviewed) {
		t.Fatalf("second confirmation error = %v", err)
	}
}

func TestConcurrentCandidateConfirmsCreateOneMemory(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	params := ReviewCandidateParams{ReasonCode: "accepted"}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func(key string) {
			defer workers.Done()
			<-start
			_, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity(key, "confirm", candidate.CandidateID, params), candidate.CandidateID, params)
			results <- err
		}(string(rune('a' + i)))
	}
	close(start)
	workers.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrCandidateReviewed) {
			conflicts++
		} else {
			t.Fatalf("confirm race error = %v", err)
		}
	}
	var memories, revisions, reviews int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memories), (SELECT count(*) FROM memory_revisions), (SELECT count(*) FROM candidate_reviews)`).Scan(&memories, &revisions, &reviews); err != nil || winners != 1 || conflicts != 1 || memories != 1 || revisions != 1 || reviews != 1 {
		t.Fatalf("confirm race winners=%d conflicts=%d rows=%d/%d/%d err=%v", winners, conflicts, memories, revisions, reviews, err)
	}
}

func TestCandidateConfirmDoesNotChangeFrozenRelatedMemory(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	related := createTestMemory(t, repository, "related")
	freeze := ChangeMemoryStateParams{ExpectedRevisionID: related.CurrentRevision.RevisionID, Action: "freeze", Reason: "protect"}
	frozen, _, err := repository.ChangeMemoryState(context.Background(), memoryIdentity("freeze-related", "/v1/memories/{memory_id}/state", freeze), related.MemoryID, freeze)
	if err != nil {
		t.Fatal(err)
	}
	params := candidateParams()
	params.RelatedMemoryIDs = []string{related.MemoryID}
	candidate, _, err := repository.CreateCandidate(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	review := ReviewCandidateParams{ReasonCode: "accepted"}
	if _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("confirm-related", "confirm", candidate.CandidateID, review), candidate.CandidateID, review); err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetMemory(context.Background(), related.MemoryID)
	if err != nil || !reflect.DeepEqual(got, frozen) {
		t.Fatalf("related memory changed: got=%+v want=%+v err=%v", got, frozen, err)
	}
}

func TestCandidateConfirmRevalidatesEvidence(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	now := "2026-08-21T11:00:00Z"
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES ('space-sibling', 'space-1', 'project', 'sibling', 'Sibling', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO raw_contents(content_id, content_kind, content_text, media_type, content_hash, size_bytes, created_at) VALUES ('content-sibling', 'text', 'other scope', 'text/plain', 'hash-sibling', 11, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-sibling', 'op-sibling', 'space-sibling', 'content-sibling', 'manual', 'test', 'seed', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO candidates(candidate_id,event_id,space_id,memory_type,content,content_hash,confidence,sensitivity,suggestion_kind,processor_identity,processing_version,specification_hash,created_at) VALUES ('invalid-candidate','event-1','space-1','fact','invalid evidence','hash',0.5,'normal','new','processor/test','v1','invalid-spec',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO candidate_evidence(evidence_id,candidate_id,event_id,created_at) VALUES ('invalid-evidence','invalid-candidate','event-sibling',?)`, now); err != nil {
		t.Fatal(err)
	}
	review := ReviewCandidateParams{ReasonCode: "accepted"}
	if _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("invalid-confirm", "confirm", "invalid-candidate", review), "invalid-candidate", review); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("ConfirmCandidate() error = %v; want ErrInvalidEvidence", err)
	}
	var memories int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memories`).Scan(&memories); err != nil || memories != 0 {
		t.Fatalf("memory count = %d, %v", memories, err)
	}
}

func TestPendingCandidateExcludedFromMemoryListAndAllSearchModes(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	configureSemanticTest(t, repository, &semanticTestProvider{})
	baseline := createMemoryWithContent(t, repository, "candidate-exclusion-baseline", "unrelated baseline memory")
	candidate := createTestCandidate(t, repository)
	page, err := repository.ListMemories(context.Background(), MemoryFilter{SpaceID: candidate.SpaceID, IncludeAncestors: true, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].MemoryID != baseline.MemoryID {
		t.Fatalf("memory list = %+v, %v", page, err)
	}
	if err := repository.RebuildSemantic(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, channels := range [][]string{{"keyword"}, {"semantic"}, {"keyword", "semantic", "graph"}} {
		result, err := repository.SearchMemories(context.Background(), SearchParams{Query: "searchable", SpaceIDs: []string{"space-1"}, IncludeAncestors: true, States: []string{"active"}, Channels: channels, Limit: 10, MaxChars: 1000})
		if err != nil {
			t.Fatalf("%v search = %+v, %v", channels, result, err)
		}
		for _, item := range result.Results {
			if item.Memory.CurrentRevision.DisplayContent == candidate.Content {
				t.Fatalf("%v search returned pending candidate: %+v", channels, item)
			}
		}
	}
}

func TestCandidateRejectCreatesNoMemoryAndReplays(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	params := ReviewCandidateParams{ReasonCode: "unsupported"}
	identity := candidateIdentity("reject", "reject", candidate.CandidateID, params)
	rejected, replay, err := repository.RejectCandidate(context.Background(), identity, candidate.CandidateID, params)
	if err != nil || replay || rejected.ReviewState != "rejected" || len(rejected.Reviews) != 1 {
		t.Fatalf("RejectCandidate() = %+v, replay=%t, error=%v", rejected, replay, err)
	}
	var memories, reviews, audits int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memories), (SELECT count(*) FROM candidate_reviews), (SELECT count(*) FROM audit_events WHERE action='reject_candidate')`).Scan(&memories, &reviews, &audits); err != nil || memories != 0 || reviews != 1 || audits != 1 {
		t.Fatalf("reject rows = memories %d reviews %d audits %d, %v", memories, reviews, audits, err)
	}
	replayed, replay, err := repository.RejectCandidate(context.Background(), identity, candidate.CandidateID, params)
	if err != nil || !replay || !reflect.DeepEqual(replayed, rejected) {
		t.Fatalf("reject replay = %+v, replay=%t, error=%v", replayed, replay, err)
	}
}

func TestCandidateConfirmRejectRaceHasOneTerminalReview(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	start := make(chan struct{})
	results := make(chan error, 2)
	params := ReviewCandidateParams{ReasonCode: "race"}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		_, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("race-confirm", "confirm", candidate.CandidateID, params), candidate.CandidateID, params)
		results <- err
	}()
	go func() {
		defer workers.Done()
		<-start
		_, _, err := repository.RejectCandidate(context.Background(), candidateIdentity("race-reject", "reject", candidate.CandidateID, params), candidate.CandidateID, params)
		results <- err
	}()
	close(start)
	workers.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrCandidateReviewed) {
			conflicts++
		} else {
			t.Fatalf("race error = %v", err)
		}
	}
	var reviews, memories int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM candidate_reviews), (SELECT count(*) FROM memories)`).Scan(&reviews, &memories); err != nil {
		t.Fatal(err)
	}
	final, err := repository.GetCandidate(context.Background(), candidate.CandidateID)
	if err != nil || winners != 1 || conflicts != 1 || reviews != 1 || (final.ReviewState == "confirmed") != (memories == 1) {
		t.Fatalf("race final=%+v winners=%d conflicts=%d reviews=%d memories=%d error=%v", final, winners, conflicts, reviews, memories, err)
	}
}

func TestCandidateConfirmFailureRollsBackToPending(t *testing.T) {
	t.Parallel()
	repository, runtime := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	runtime.failAt = runtime.id + 6
	params := ReviewCandidateParams{ReasonCode: "fault"}
	if _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("fault", "confirm", candidate.CandidateID, params), candidate.CandidateID, params); err == nil {
		t.Fatal("confirmation succeeded with injected failure")
	}
	got, err := repository.GetCandidate(context.Background(), candidate.CandidateID)
	if err != nil || got.ReviewState != "pending" || len(got.Reviews) != 0 {
		t.Fatalf("candidate after failure = %+v, %v", got, err)
	}
	var memories int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memories`).Scan(&memories); err != nil || memories != 0 {
		t.Fatalf("memory count after failure = %d, %v", memories, err)
	}
}

func TestCandidateListFiltersAndUsesStableCursor(t *testing.T) {
	t.Parallel()
	repository, clock := newMemoryTestRepository(t)
	first := createTestCandidate(t, repository)
	clock.now = clock.now.Add(1)
	params := candidateParams()
	params.Content = "second candidate"
	second, _, err := repository.CreateCandidate(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListCandidates(context.Background(), CandidateFilter{SpaceID: "space-1", ReviewState: "pending", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].CandidateID != second.CandidateID || page.NextCursor == nil {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := repository.ListCandidates(context.Background(), CandidateFilter{SpaceID: "space-1", ReviewState: "pending", Limit: 1, Cursor: *page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].CandidateID != first.CandidateID {
		t.Fatalf("second page = %+v, %v", next, err)
	}
}
