package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

func autoConfirmPolicyFor(minConfidence float64, kinds []string, versions []string, maxPerHour int) review.AutoConfirmPolicy {
	return review.NewAutoConfirmPolicy(true, review.DefaultAutoConfirmVersion, minConfidence, kinds, versions, maxPerHour)
}

func autoConfirmInTransaction(t *testing.T, repository *Repository, candidateID string) bool {
	t.Helper()
	tx, err := repository.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	published, err := repository.autoConfirmCandidate(context.Background(), tx, candidateID)
	if err != nil {
		t.Fatalf("autoConfirmCandidate() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return published
}

func autoConfirmCandidateParams(eventID, content string, confidence float64, sensitivity string, kind domain.MemoryKind, roles ...domain.EvidenceRole) CreateCandidateParams {
	if len(roles) == 0 {
		roles = []domain.EvidenceRole{domain.EvidenceRoleDirect}
	}
	evidence := make([]domain.RevisionEvidence, 0, len(roles))
	params := make([]EvidenceParams, 0, len(roles))
	for index, role := range roles {
		start, end := int64(1), int64(5)
		if index > 0 {
			start, end = int64(3+3*index), int64(6+3*index)
		}
		evidence = append(evidence, domain.RevisionEvidence{
			EvidenceID: "auto-evidence-" + string(rune('a'+index)), EventID: eventID, Role: role,
			StartOffset: &start, EndOffset: &end,
		})
		params = append(params, EvidenceParams{EventID: eventID, Role: role, StartOffset: &start, EndOffset: &end})
	}
	return CreateCandidateParams{
		EventID: eventID, SpaceID: "space-1", ProcessorIdentity: "processor/test",
		ProcessingVersion: "test-v1",
		Evidence:          params,
		Revision: domain.MemoryRevision{
			Kind: kind, DisplayContent: content, CanonicalContent: content,
			Sensitivity: sensitivity, Confidence: confidence, Specificity: 0.5,
			Evidence: evidence,
		},
	}
}

func createAutoConfirmCandidate(t *testing.T, repository *Repository, params CreateCandidateParams) domain.Candidate {
	t.Helper()
	candidate, duplicate, err := repository.CreateCandidate(context.Background(), params)
	if err != nil || duplicate {
		t.Fatalf("CreateCandidate() = %+v duplicate=%t error=%v", candidate, duplicate, err)
	}
	return candidate
}

func candidateState(t *testing.T, repository *Repository, candidateID string) (state string, memoryID *string) {
	t.Helper()
	var confirmed *string
	if err := repository.store.reader.QueryRow(`SELECT review_state, confirmed_memory_id FROM candidates WHERE candidate_id=?`, candidateID).Scan(&state, &confirmed); err != nil {
		t.Fatal(err)
	}
	return state, confirmed
}

func countRows(t *testing.T, repository *Repository, query string, args ...any) int {
	t.Helper()
	var count int
	if err := repository.store.reader.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestAutoConfirmPublishesEligibleCandidateWithSystemReviewIdentity(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	repository.SetAutoConfirmPolicy(autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20))

	claim, input := claimProcessingJob(t, repository, 3)
	result := domain.ProcessingResult{
		Code:             domain.ProcessingCandidatesExtracted,
		Proposals:        []domain.MemoryCandidateProposal{processingCandidateWith("auto confirmed memory", 0.97, "normal", domain.MemoryKindFact)},
		ProviderIdentity: "provider/test", ModelIdentity: "model-test",
	}
	if err := commitProcessing(t, repository, claim, input, result); err != nil {
		t.Fatalf("commitProcessing() error = %v", err)
	}

	var candidateID, state, confirmedMemory string
	var autoReviewVersion *string
	if err := repository.store.reader.QueryRow(`SELECT candidate_id, review_state, confirmed_memory_id, auto_review_version FROM candidates`).Scan(&candidateID, &state, &confirmedMemory, &autoReviewVersion); err != nil {
		t.Fatal(err)
	}
	if state != "confirmed" || confirmedMemory == "" {
		t.Fatalf("candidate state = %q memory = %q; want confirmed authority", state, confirmedMemory)
	}
	if autoReviewVersion == nil || *autoReviewVersion != review.DefaultAutoConfirmVersion {
		t.Fatalf("auto_review_version = %v; want %q", autoReviewVersion, review.DefaultAutoConfirmVersion)
	}
	stored, err := repository.GetCandidate(context.Background(), candidateID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AutoReviewVersion == nil || *stored.AutoReviewVersion != review.DefaultAutoConfirmVersion {
		t.Fatalf("read candidate auto review version = %v", stored.AutoReviewVersion)
	}
	if got := countRows(t, repository, `SELECT count(*) FROM memories`); got != 1 {
		t.Fatalf("memories = %d; want 1", got)
	}
	var reviewer, reason, note, fromState, toState string
	if err := repository.store.reader.QueryRow(`SELECT reviewer_id, reason_code, note, from_state, to_state FROM candidate_reviews WHERE candidate_id=?`, candidateID).Scan(&reviewer, &reason, &note, &fromState, &toState); err != nil {
		t.Fatal(err)
	}
	if reviewer != review.AutoConfirmReviewerID || reason != review.AutoConfirmReasonCode || fromState != "pending" || toState != "confirmed" {
		t.Fatalf("review = %q/%q %s->%s", reviewer, reason, fromState, toState)
	}
	if !strings.Contains(note, review.DefaultAutoConfirmVersion) || !strings.Contains(note, "min_confidence=0.9500") || !strings.Contains(note, "confidence=0.9700") {
		t.Fatalf("review note = %q; want the policy version and every threshold used", note)
	}
	var caller, action string
	if err := repository.store.reader.QueryRow(`SELECT caller_id, action FROM audit_events WHERE action='confirm_candidate'`).Scan(&caller, &action); err != nil {
		t.Fatal(err)
	}
	if caller != review.AutoConfirmReviewerID {
		t.Fatalf("audit caller = %q; want the system reviewer", caller)
	}
	var createdBy, decisionAction string
	if err := repository.store.reader.QueryRow(`SELECT created_by FROM memory_revisions`).Scan(&createdBy); err != nil {
		t.Fatal(err)
	}
	if createdBy != review.AutoConfirmReviewerID {
		t.Fatalf("revision created_by = %q; want the system reviewer", createdBy)
	}
	if err := repository.store.reader.QueryRow(`SELECT action FROM consolidation_decisions WHERE candidate_id=?`, candidateID).Scan(&decisionAction); err != nil {
		t.Fatal(err)
	}
	if decisionAction != string(domain.ConsolidationCreate) {
		t.Fatalf("consolidation action = %q; want create", decisionAction)
	}
}

func TestAutoConfirmLeavesIneligibleCandidatesPending(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		policy  review.AutoConfirmPolicy
		params  CreateCandidateParams
		reason  string
		publish bool
	}{
		{
			name: "disabled policy", policy: review.AutoConfirmPolicy{},
			params: autoConfirmCandidateParams("event-1", "disabled policy memory", 0.99, "normal", domain.MemoryKindFact),
			reason: "a zero policy must never publish",
		},
		{
			name: "kind outside the closed set", policy: autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20),
			params: autoConfirmCandidateParams("event-1", "kind outside policy memory", 0.99, "normal", domain.MemoryKindEvent),
			reason: "event is not in the configured kind set",
		},
		{
			name: "sensitive content", policy: autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20),
			params: autoConfirmCandidateParams("event-1", "sensitive policy memory", 0.99, "sensitive", domain.MemoryKindFact),
			reason: "sensitive content always needs a human decision",
		},
		{
			name: "below the confidence floor", policy: autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20),
			params: autoConfirmCandidateParams("event-1", "low confidence policy memory", 0.94, "normal", domain.MemoryKindFact),
			reason: "0.94 is below the 0.95 floor",
		},
		{
			name: "no direct evidence", policy: autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20),
			params: autoConfirmCandidateParams("event-1", "derived only policy memory", 0.99, "normal", domain.MemoryKindFact, domain.EvidenceRoleDerived, domain.EvidenceRoleContext),
			reason: "authority needs at least one direct span",
		},
		{
			name: "processing version outside the allowlist", policy: autoConfirmPolicyFor(0.95, []string{"fact"}, []string{"other-v1"}, 20),
			params: autoConfirmCandidateParams("event-1", "version gated policy memory", 0.99, "normal", domain.MemoryKindFact),
			reason: "the stored processing version is test-v1",
		},
		{
			name: "eligible control", policy: autoConfirmPolicyFor(0.95, []string{"fact"}, []string{"test-v1"}, 20),
			params: autoConfirmCandidateParams("event-1", "eligible control memory", 0.99, "normal", domain.MemoryKindFact),
			reason: "control case must publish", publish: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, _ := newMemoryTestRepository(t)
			repository.SetAutoConfirmPolicy(test.policy)
			candidate := createAutoConfirmCandidate(t, repository, test.params)
			if published := autoConfirmInTransaction(t, repository, candidate.CandidateID); published != test.publish {
				t.Fatalf("published = %t; want %t (%s)", published, test.publish, test.reason)
			}
			state, memoryID := candidateState(t, repository, candidate.CandidateID)
			if test.publish {
				if state != "confirmed" || memoryID == nil {
					t.Fatalf("state = %q memory = %v; want confirmed authority", state, memoryID)
				}
				return
			}
			if state != "pending" || memoryID != nil {
				t.Fatalf("state = %q memory = %v; want pending review", state, memoryID)
			}
			for table, query := range map[string]string{
				"candidate_reviews":       `SELECT count(*) FROM candidate_reviews`,
				"consolidation_decisions": `SELECT count(*) FROM consolidation_decisions`,
				"memories":                `SELECT count(*) FROM memories`,
				"audit confirm events":    `SELECT count(*) FROM audit_events WHERE action='confirm_candidate'`,
			} {
				if got := countRows(t, repository, query); got != 0 {
					t.Fatalf("%s = %d; want 0 when the policy refuses (%s)", table, got, test.reason)
				}
			}
		})
	}
}

func TestAutoConfirmRefusesDestructiveConsolidationOutcome(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	existing := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: "duplicate policy memory", Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "seed"}
	if _, _, err := repository.CreateMemory(context.Background(), memoryIdentity("seed-authority", "/v1/memories", existing), existing); err != nil {
		t.Fatal(err)
	}
	repository.SetAutoConfirmPolicy(autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20))
	candidate := createAutoConfirmCandidate(t, repository, autoConfirmCandidateParams("event-1", "duplicate policy memory", 0.99, "normal", domain.MemoryKindFact))
	if published := autoConfirmInTransaction(t, repository, candidate.CandidateID); published {
		t.Fatal("a redundant-evidence candidate was published; the policy must never publish a reject outcome")
	}
	if state, memoryID := candidateState(t, repository, candidate.CandidateID); state != "pending" || memoryID != nil {
		t.Fatalf("state = %q memory = %v; want pending review", state, memoryID)
	}
	if got := countRows(t, repository, `SELECT count(*) FROM memories`); got != 1 {
		t.Fatalf("memories = %d; want the seeded memory only", got)
	}
}

func TestAutoConfirmPublishesAttachEvidenceOutcome(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	existing := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: "attachable policy memory", Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "seed"}
	seeded, _, err := repository.CreateMemory(context.Background(), memoryIdentity("seed-attach", "/v1/memories", existing), existing)
	if err != nil {
		t.Fatal(err)
	}
	repository.SetAutoConfirmPolicy(autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20))
	candidate := createAutoConfirmCandidate(t, repository, autoConfirmCandidateParams("event-2", "attachable policy memory", 0.99, "normal", domain.MemoryKindFact))
	if published := autoConfirmInTransaction(t, repository, candidate.CandidateID); !published {
		t.Fatal("an attach_evidence candidate was not published")
	}
	if got := countRows(t, repository, `SELECT count(*) FROM memories`); got != 1 {
		t.Fatalf("memories = %d; want the evidence attached to the existing memory", got)
	}
	if got := countRows(t, repository, `SELECT count(*) FROM memory_evidence WHERE revision_id=?`, seeded.CurrentRevision.RevisionID); got != 2 {
		t.Fatalf("evidence rows on the target revision = %d; want 2", got)
	}
	state, memoryID := candidateState(t, repository, candidate.CandidateID)
	if state != "confirmed" || memoryID == nil || *memoryID != seeded.MemoryID {
		t.Fatalf("state = %q memory = %v; want the seeded memory confirmed", state, memoryID)
	}
}

func TestAutoConfirmRateLimitBoundsPublicationsPerSpace(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	seedSecondSpace(t, repository)
	repository.SetAutoConfirmPolicy(autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 1))

	first := createAutoConfirmCandidate(t, repository, autoConfirmCandidateParams("event-1", "first policy memory", 0.99, "normal", domain.MemoryKindFact))
	if !autoConfirmInTransaction(t, repository, first.CandidateID) {
		t.Fatal("the first publication within budget was refused")
	}
	second := createAutoConfirmCandidate(t, repository, autoConfirmCandidateParams("event-1", "second policy memory", 0.99, "normal", domain.MemoryKindFact))
	if autoConfirmInTransaction(t, repository, second.CandidateID) {
		t.Fatal("the rate limit did not stop a second publication in the same space")
	}
	other := autoConfirmCandidateParams("event-3", "other space policy memory", 0.99, "normal", domain.MemoryKindFact)
	other.SpaceID = "space-2"
	otherCandidate := createAutoConfirmCandidate(t, repository, other)
	if !autoConfirmInTransaction(t, repository, otherCandidate.CandidateID) {
		t.Fatal("a separate space must keep its own publication budget")
	}
	if got := countRows(t, repository, `SELECT count(*) FROM candidate_reviews WHERE reviewer_id=?`, review.AutoConfirmReviewerID); got != 2 {
		t.Fatalf("automatic reviews = %d; want 2", got)
	}
}

func TestAutoConfirmPolicyChangeDoesNotPublishBacklog(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	backlog := createAutoConfirmCandidate(t, repository, autoConfirmCandidateParams("event-1", "backlog policy memory", 0.99, "normal", domain.MemoryKindFact))
	repository.SetAutoConfirmPolicy(autoConfirmPolicyFor(0.95, []string{"fact"}, nil, 20))
	fresh := createAutoConfirmCandidate(t, repository, autoConfirmCandidateParams("event-1", "fresh policy memory", 0.99, "normal", domain.MemoryKindFact))
	if !autoConfirmInTransaction(t, repository, fresh.CandidateID) {
		t.Fatal("a candidate created under the enabled policy was not published")
	}
	if state, memoryID := candidateState(t, repository, backlog.CandidateID); state != "pending" || memoryID != nil {
		t.Fatalf("backlog state = %q memory = %v; want untouched pending review", state, memoryID)
	}
}

func TestAutoConfirmPolicyDoesNotBlockHumanReview(t *testing.T) {
	t.Parallel()
	repository, _ := newMemoryTestRepository(t)
	repository.SetAutoConfirmPolicy(autoConfirmPolicyFor(0.99, []string{"fact"}, nil, 20))
	candidate := createAutoConfirmCandidate(t, repository, autoConfirmCandidateParams("event-1", "human reviewed policy memory", 0.90, "normal", domain.MemoryKindFact))
	if published := autoConfirmInTransaction(t, repository, candidate.CandidateID); published {
		t.Fatal("a candidate below the confidence floor was published")
	}
	review := ReviewCandidateParams{ReasonCode: "reviewed"}
	memory, _, replay, err := repository.ConfirmCandidate(context.Background(), candidateIdentity("human-review", "confirm", candidate.CandidateID, review), candidate.CandidateID, review)
	if err != nil || replay {
		t.Fatalf("ConfirmCandidate() replay=%t error=%v", replay, err)
	}
	if memory.MemoryID == "" || memory.CurrentRevision.CreatedBy != "memory-test-caller" {
		t.Fatalf("memory = %+v; want a caller-attributed revision", memory)
	}
	if state, memoryID := candidateState(t, repository, candidate.CandidateID); state != "confirmed" || memoryID == nil {
		t.Fatalf("state = %q memory = %v; want confirmed authority", state, memoryID)
	}
	stored, err := repository.GetCandidate(context.Background(), candidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AutoReviewVersion != nil {
		t.Fatalf("human-reviewed candidate carries auto_review_version %q", *stored.AutoReviewVersion)
	}
}

func seedSecondSpace(t *testing.T, repository *Repository) {
	t.Helper()
	now := "2026-08-21T11:00:00Z"
	statements := []string{
		`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES ('space-2', 'space-1', 'project', 'project-2', 'Second', '` + now + `')`,
		`INSERT INTO raw_contents(content_id, content_kind, content_text, media_type, content_hash, size_bytes, created_at) VALUES ('content-3', 'text', 'source three', 'text/plain', 'hash-3', 12, '` + now + `')`,
		`INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, caller_id, occurred_at, received_at, created_at) VALUES ('event-3', 'seed-op-3', 'space-2', 'content-3', 'manual', 'test', 'seed', '` + now + `', '` + now + `', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := repository.store.writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func processingCandidateWith(content string, confidence float64, sensitivity string, kind domain.MemoryKind) domain.MemoryCandidateProposal {
	start, end := int64(1), int64(5)
	return domain.MemoryCandidateProposal{
		Action: domain.ConsolidationCreate,
		Revision: domain.MemoryRevision{
			Kind: kind, DisplayContent: content, CanonicalContent: content,
			Confidence: confidence, Specificity: 0.5, Sensitivity: sensitivity,
			Evidence: []domain.RevisionEvidence{{EvidenceID: "ev-auto", EventID: "event-1", Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}},
		},
	}
}

var _ = extraction.CommitRequest{}
