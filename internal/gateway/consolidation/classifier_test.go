package consolidation

import (
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func rulesFixture() Rules {
	return Rules{Version: "consolidation-v1", ExactHashEnabled: true, SameSourceEventCosine: 0.90, SameSubjectKindCosine: 0.94}
}

func candidateRevisionFor(content string, subjects []string, attrs map[string]string, validFrom, validUntil *time.Time, evidence []domain.RevisionEvidence) domain.MemoryRevision {
	revision := domain.MemoryRevision{
		Kind:             domain.MemoryKindFact,
		CanonicalContent: content,
		CanonicalHash:    CanonicalContentHash(content),
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

func neighborFor(content string, subjects []string, attrs map[string]string, validFrom, validUntil *time.Time, events ...string) *Neighbor {
	neighbor := &Neighbor{
		MemoryID: "memory-target", RevisionID: "revision-target", Kind: domain.MemoryKindFact,
		CanonicalContent: content, CanonicalHash: CanonicalContentHash(content),
		Subjects: subjects, Attributes: attrs, ValidFrom: validFrom, ValidUntil: validUntil,
	}
	start, end := int64(10), int64(20)
	for _, event := range events {
		neighbor.EvidenceRanges = append(neighbor.EvidenceRanges, EvidenceRange{EventID: event, StartOffset: &start, EndOffset: &end})
		neighbor.EvidenceEventIDs = append(neighbor.EvidenceEventIDs, event)
	}
	return neighbor
}

func neighborWithRange(content, event string, start, end int64) *Neighbor {
	neighbor := neighborFor(content, nil, nil, nil, nil)
	neighbor.EvidenceRanges = []EvidenceRange{{EventID: event, StartOffset: &start, EndOffset: &end}}
	return neighbor
}

func directRevisionEvidence(eventID string) []domain.RevisionEvidence {
	start, end := int64(1), int64(5)
	return []domain.RevisionEvidence{{EvidenceID: "evidence-" + eventID, EventID: eventID, Role: domain.EvidenceRoleDirect, StartOffset: &start, EndOffset: &end}}
}

func TestConsolidationExactRules(t *testing.T) {
	t.Parallel()
	rules := rulesFixture()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	laterEnd := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		candidate  domain.MemoryRevision
		neighbor   *Neighbor
		sim        Similarity
		wantAction domain.ConsolidationAction
		wantReason string
	}{
		{
			name:       "exact canonical hash attaches non-duplicate evidence",
			candidate:  candidateRevisionFor("alice prefers tea", nil, nil, nil, nil, directRevisionEvidence("event-new")),
			neighbor:   neighborFor("alice prefers tea", nil, nil, nil, nil, "event-old"),
			wantAction: domain.ConsolidationAttachEvidence, wantReason: "exact_canonical_hash",
		},
		{
			name:       "exact canonical hash rejects duplicate evidence",
			candidate:  candidateRevisionFor("alice prefers tea", nil, nil, nil, nil, directRevisionEvidence("event-old")),
			neighbor:   neighborWithRange("alice prefers tea", "event-old", 1, 5),
			wantAction: domain.ConsolidationReject, wantReason: "redundant_evidence",
		},
		{
			name:       "exact canonical hash with attribute delta refines",
			candidate:  candidateRevisionFor("alice prefers tea", nil, map[string]string{"preference": "tea"}, nil, nil, directRevisionEvidence("event-new")),
			neighbor:   neighborFor("alice prefers tea", nil, map[string]string{"preference": "coffee"}, nil, nil, "event-old"),
			wantAction: domain.ConsolidationRefine, wantReason: "exact_hash_attribute_delta",
		},
		{
			name:       "overlapping subject kind attribute conflict contests",
			candidate:  candidateRevisionFor("alice role engineer", []string{"alice"}, map[string]string{"role": "engineer"}, &start, &end, directRevisionEvidence("event-new")),
			neighbor:   neighborFor("alice role manager", []string{"alice"}, map[string]string{"role": "manager"}, &start, &end, "event-old"),
			wantAction: domain.ConsolidationContest, wantReason: "overlapping_attribute_conflict",
		},
		{
			name:       "ordered subject kind attribute conflict supersedes",
			candidate:  candidateRevisionFor("alice role engineer", []string{"alice"}, map[string]string{"role": "engineer"}, &later, &laterEnd, directRevisionEvidence("event-new")),
			neighbor:   neighborFor("alice role manager", []string{"alice"}, map[string]string{"role": "manager"}, &start, &end, "event-old"),
			wantAction: domain.ConsolidationSupersede, wantReason: "ordered_attribute_conflict",
		},
		{
			name:       "same subject kind without similarity does not merge",
			candidate:  candidateRevisionFor("alice role engineer", []string{"alice"}, map[string]string{"role": "engineer"}, &start, &end, directRevisionEvidence("event-new")),
			neighbor:   neighborFor("alice role manager", []string{"alice"}, map[string]string{"role": "engineer"}, &start, &end, "event-old"),
			wantAction: domain.ConsolidationCreate, wantReason: "novel",
		},
		{
			name:       "same source event above threshold attaches",
			candidate:  candidateRevisionFor("alice prefers tea", nil, nil, nil, nil, directRevisionEvidence("shared")),
			neighbor:   neighborFor("alice likes tea", nil, nil, nil, nil, "shared"),
			sim:        Similarity{Cosine: 0.95, OK: true},
			wantAction: domain.ConsolidationAttachEvidence, wantReason: "same_source_event",
		},
		{
			name:       "same source event below threshold creates",
			candidate:  candidateRevisionFor("alice prefers tea", nil, nil, nil, nil, directRevisionEvidence("shared")),
			neighbor:   neighborFor("alice likes tea", nil, nil, nil, nil, "shared"),
			sim:        Similarity{Cosine: 0.80, OK: true},
			wantAction: domain.ConsolidationCreate, wantReason: "novel",
		},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			action, reason := ClassifyDeterministic(rules, test.candidate, test.neighbor, test.sim)
			if action != test.wantAction || reason != test.wantReason {
				t.Fatalf("ClassifyDeterministic() = %s/%s; want %s/%s", action, reason, test.wantAction, test.wantReason)
			}
		})
	}
}

func TestConsolidationCosineAloneCannotMergeOrSupersede(t *testing.T) {
	t.Parallel()
	rules := rulesFixture()
	candidate := candidateRevisionFor("unrelated claim about weather", []string{"bob"}, nil, nil, nil, directRevisionEvidence("event-new"))
	neighbor := neighborFor("project deadline friday", []string{"alice"}, nil, nil, nil, "event-old")
	action, reason := ClassifyDeterministic(rules, candidate, neighbor, Similarity{Cosine: 0.999, OK: true})
	if action != domain.ConsolidationCreate {
		t.Fatalf("cosine alone produced %s/%s; want create", action, reason)
	}
}

func TestConsolidationProviderRequiresConflictForDestructiveActions(t *testing.T) {
	t.Parallel()
	deterministic := Outcome{Action: domain.ConsolidationCreate, ReasonCode: "novel"}
	supersede := Classification{Action: domain.ConsolidationSupersede, ReasonCode: "semantic", MemoryID: "m", RevisionID: "r"}
	if _, ok := ValidateProviderProposal(deterministic, supersede, false); ok {
		t.Fatal("provider supersede accepted without deterministic conflict")
	}
	if _, ok := ValidateProviderProposal(deterministic, supersede, true); !ok {
		t.Fatal("provider supersede rejected with deterministic conflict")
	}
	refine := Classification{Action: domain.ConsolidationRefine, ReasonCode: "semantic", MemoryID: "m", RevisionID: "r"}
	if _, ok := ValidateProviderProposal(deterministic, refine, false); !ok {
		t.Fatal("provider refine rejected without conflict")
	}
	resolved := Outcome{Action: domain.ConsolidationAttachEvidence, ReasonCode: "exact_canonical_hash"}
	if _, ok := ValidateProviderProposal(resolved, refine, false); ok {
		t.Fatal("provider proposal accepted for already resolved candidate")
	}
}

func TestConsolidationProviderProposalCarriesProviderClassification(t *testing.T) {
	t.Parallel()
	outcome, ok := ValidateProviderProposal(NovelOutcome(), Classification{Action: domain.ConsolidationRefine, ReasonCode: "merge"}, false)
	if !ok || outcome.Classification != domain.ConsolidationProvider || outcome.ReasonCode != "provider_merge" {
		t.Fatalf("outcome = %+v ok=%t", outcome, ok)
	}
}

func TestConflictDetectedRequiresKindSubjectAndAttributeDelta(t *testing.T) {
	t.Parallel()
	candidate := candidateRevisionFor("alice role engineer", []string{"alice"}, map[string]string{"role": "engineer"}, nil, nil, nil)
	conflicting := Neighbor{Kind: domain.MemoryKindFact, Subjects: []string{"alice"}, Attributes: map[string]string{"role": "manager"}}
	if !ConflictDetected(candidate, []Neighbor{conflicting}) {
		t.Fatal("expected an attribute conflict")
	}
	if ConflictDetected(candidate, []Neighbor{{Kind: domain.MemoryKindFact, Subjects: []string{"alice"}, Attributes: map[string]string{"role": "engineer"}}}) {
		t.Fatal("equal attributes must not be a conflict")
	}
	if ConflictDetected(candidate, []Neighbor{{Kind: domain.MemoryKindPreference, Subjects: []string{"alice"}, Attributes: map[string]string{"role": "manager"}}}) {
		t.Fatal("a different kind must not be a conflict")
	}
}

func TestCanonicalContentHashIsStable(t *testing.T) {
	t.Parallel()
	if CanonicalContentHash("alice") == "" || CanonicalContentHash("alice") != CanonicalContentHash("alice") {
		t.Fatal("canonical content hash is not stable")
	}
	if CanonicalContentHash("alice") == CanonicalContentHash("bob") {
		t.Fatal("distinct content produced the same hash")
	}
}
