package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func candidateRevisionFor(content string, subjects []string, attrs map[string]string, validFrom, validUntil *time.Time, evidence []domain.RevisionEvidence) domain.MemoryRevision {
	revision := domain.MemoryRevision{
		Kind:             domain.MemoryKindFact,
		CanonicalContent: content,
		CanonicalHash:    canonicalContentHash(content),
		SubjectEntityIDs: subjects,
		Attributes:       attrs,
		ValidFrom:        validFrom,
		ValidUntil:       validUntil,
		Evidence:         evidence,
	}
	if len(evidence) > 0 {
		revision.Confidence = 0.9
	}
	return revision
}

func directRevisionEvidence(eventID string) []domain.RevisionEvidence {
	start, end := int64(1), int64(5)
	return []domain.RevisionEvidence{{EvidenceID: "evidence-" + eventID, EventID: eventID, Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}}
}

func TestConsolidationRecallIsScopedAndBounded(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	now := "2026-08-21T11:00:00Z"
	for _, space := range []struct{ id, parent, external string }{
		{"space-ancestor", "space-1", "ancestor"},
		{"space-child", "space-ancestor", "child"},
		{"space-sibling", "space-1", "sibling"},
	} {
		if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES (?,?, 'project', ?, ?, ?)`, space.id, space.parent, space.external, space.external, now); err != nil {
			t.Fatal(err)
		}
	}
	makeMemory := func(key, spaceID string, subject string) domain.Memory {
		params := CreateMemoryParams{SpaceID: spaceID, Kind: "fact", Content: "shared canonical " + key, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "scope"}
		memory, replay, err := repository.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
		if err != nil || replay {
			t.Fatalf("CreateMemory(%s) = %+v replay=%t err=%v", key, memory, replay, err)
		}
		if _, err := repository.store.writer.Exec(`INSERT INTO revision_subject_entities(revision_id, entity_order, entity_id) VALUES (?,0,?)`, memory.CurrentRevision.RevisionID, subject); err != nil {
			t.Fatal(err)
		}
		return memory
	}
	makeMemory("root", "space-1", "alice")
	makeMemory("ancestor", "space-ancestor", "alice")
	makeMemory("sibling", "space-sibling", "alice")
	candidate := candidateRevisionFor("shared canonical child", []string{"alice"}, nil, nil, nil, directRevisionEvidence("event-2"))
	tx, err := repository.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	neighbors, err := repository.boundedConsolidationRecall(context.Background(), tx, "space-child", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(neighbors) != 0 {
		t.Fatalf("recall leaked ancestor or sibling memories: %+v", neighbors)
	}
}

func TestConsolidationAttachEvidenceIsAtomicDeduplicatedAndFrozenSafe(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, repository, "attach-evidence")
	revisionID := memory.CurrentRevision.RevisionID
	tx, err := repository.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	start, end := int64(0), int64(6)
	evidence := []EvidenceParams{{EventID: "event-2", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}}
	inserted, err := repository.attachConsolidationEvidence(context.Background(), tx, "space-1", memory.MemoryID, revisionID, evidence, formatTime(repository.runtime.Now()))
	if err != nil || inserted != 1 {
		t.Fatalf("first attach = %d, %v; want 1", inserted, err)
	}
	inserted, err = repository.attachConsolidationEvidence(context.Background(), tx, "space-1", memory.MemoryID, revisionID, evidence, formatTime(repository.runtime.Now()))
	if err != nil || inserted != 0 {
		t.Fatalf("duplicate attach = %d, %v; want 0", inserted, err)
	}
	if _, err := repository.attachConsolidationEvidence(context.Background(), tx, "space-1", memory.MemoryID, "stale-revision", evidence, formatTime(repository.runtime.Now())); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision attach error = %v; want ErrRevisionConflict", err)
	}
	if _, err := tx.Exec(`UPDATE memories SET frozen=1 WHERE memory_id=?`, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.attachConsolidationEvidence(context.Background(), tx, "space-1", memory.MemoryID, revisionID, evidence, formatTime(repository.runtime.Now())); !errors.Is(err, ErrFrozenRevision) {
		t.Fatalf("frozen attach error = %v; want ErrFrozenRevision", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func confirmStructuredMemory(t *testing.T, repository *Repository, key, content string, subjects []string, attrs map[string]string, from, until *time.Time) domain.Memory {
	t.Helper()
	confidence := 0.9
	structured := domain.MemoryRevision{
		Kind: domain.MemoryKindFact, DisplayContent: content, CanonicalContent: content,
		SubjectEntityIDs: subjects, Attributes: attrs, ValidFrom: from, ValidUntil: until,
		Confidence: confidence, Sensitivity: "normal",
		Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-" + key, EventID: "event-1", Role: domain.EvidenceRoleDirect}},
	}
	params := candidateParams()
	params.Revision = structured
	params.Evidence = directEvidence("event-1")
	candidate, duplicate, err := repository.CreateCandidate(context.Background(), params)
	if err != nil || duplicate {
		t.Fatalf("CreateCandidate(%s) = %+v dup=%t err=%v", key, candidate, duplicate, err)
	}
	review := ReviewCandidateParams{ReasonCode: "accepted"}
	memory, _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("structured-"+key, "confirm", candidate.CandidateID, review), candidate.CandidateID, review)
	if err != nil {
		t.Fatalf("ConfirmCandidate(%s) error = %v", key, err)
	}
	return memory
}

func TestConsolidationConfirmAttachesEvidenceWithoutNewMemory(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	base := createTestMemory(t, repository, "attach-base")
	params := candidateParams()
	params.Revision.DisplayContent = base.CurrentRevision.DisplayContent
	params.Revision.CanonicalContent = base.CurrentRevision.DisplayContent
	params.Evidence = directEvidence("event-2")
	candidate, _, err := repository.CreateCandidate(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	review := ReviewCandidateParams{ReasonCode: "accepted"}
	memory, _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("attach-confirm", "confirm", candidate.CandidateID, review), candidate.CandidateID, review)
	if err != nil {
		t.Fatal(err)
	}
	if memory.MemoryID != base.MemoryID {
		t.Fatalf("attach returned memory %s; want %s", memory.MemoryID, base.MemoryID)
	}
	var memories, evidence int
	if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memories), (SELECT count(*) FROM memory_evidence WHERE revision_id=?)`, base.CurrentRevision.RevisionID).Scan(&memories, &evidence); err != nil || memories != 1 || evidence != 2 {
		t.Fatalf("attach rows memories=%d evidence=%d err=%v", memories, evidence, err)
	}
	decision, err := repository.GetConsolidationDecision(context.Background(), candidate.CandidateID)
	if err != nil || decision.Action != domain.ConsolidationAttachEvidence || decision.TargetMemoryID == nil || *decision.TargetMemoryID != base.MemoryID {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
}

func TestConsolidationConfirmRecordsCreateDecision(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	candidate := createTestCandidate(t, repository)
	review := ReviewCandidateParams{ReasonCode: "accepted"}
	memory, _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("create-decision", "confirm", candidate.CandidateID, review), candidate.CandidateID, review)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := repository.GetConsolidationDecision(context.Background(), candidate.CandidateID)
	if err != nil || decision.Action != domain.ConsolidationCreate || decision.TargetMemoryID == nil || *decision.TargetMemoryID != memory.MemoryID {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
	var audits int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action='confirm_candidate'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("confirm audits = %d, %v", audits, err)
	}
}

func TestConsolidationConfirmRefineCreatesEvolutionProposal(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	base := confirmStructuredMemory(t, repository, "refine-base", "alice account role", []string{"alice"}, map[string]string{"role": "manager"}, &from, nil)
	params := candidateParams()
	params.Evidence = directEvidence("event-2")
	params.Revision = domain.MemoryRevision{
		Kind: domain.MemoryKindFact, DisplayContent: "alice account role", CanonicalContent: "alice account role",
		SubjectEntityIDs: []string{"alice"}, Attributes: map[string]string{"role": "engineer"},
		Confidence: 0.9, Sensitivity: "normal",
		Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-refine", EventID: "event-2", Role: domain.EvidenceRoleDirect}},
	}
	candidate, _, err := repository.CreateCandidate(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	review := ReviewCandidateParams{ReasonCode: "accepted"}
	if _, _, _, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("refine-confirm", "confirm", candidate.CandidateID, review), candidate.CandidateID, review); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetMemory(context.Background(), base.MemoryID); err != nil {
		t.Fatal(err)
	}
	var proposals int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM evolution_proposals WHERE action='supplement' AND review_state='pending'`).Scan(&proposals); err != nil || proposals != 1 {
		t.Fatalf("evolution proposals = %d, %v", proposals, err)
	}
	decision, err := repository.GetConsolidationDecision(context.Background(), candidate.CandidateID)
	if err != nil || decision.Action != domain.ConsolidationRefine {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
}

func TestConsolidationConcurrentRevisionChangeRejectsAttach(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	base := createTestMemory(t, repository, "concurrent-base")
	oldRevision := base.CurrentRevision.RevisionID
	revise := ReviseMemoryParams{ExpectedRevisionID: oldRevision, Content: "revised content", Evidence: directEvidence("event-2"), Reason: "advance"}
	if _, _, err := repository.ReviseMemory(context.Background(), memoryIdentity("concurrent-revise", "/v1/memories/{memory_id}/revisions", revise), base.MemoryID, revise); err != nil {
		t.Fatal(err)
	}
	tx, err := repository.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := repository.attachConsolidationEvidence(context.Background(), tx, "space-1", base.MemoryID, oldRevision, directEvidence("event-2"), formatTime(repository.runtime.Now())); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("attach after concurrent revision error = %v; want ErrRevisionConflict", err)
	}
	if _, err := repository.attachConsolidationEvidence(context.Background(), tx, "space-2", base.MemoryID, base.CurrentRevision.RevisionID, directEvidence("event-2"), formatTime(repository.runtime.Now())); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("cross-space attach error = %v; want ErrInvalidEvidence", err)
	}
}
