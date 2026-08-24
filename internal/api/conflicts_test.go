package api

import (
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

func TestConflictResolutionDestructivePayloadPreserved(t *testing.T) {
	content, mode := "reviewed replacement", "new_memory"
	selected := domain.EvolutionTarget{MemoryID: "11111111-1111-4111-8111-111111111111", ExpectedRevisionID: "22222222-2222-4222-8222-222222222222"}
	start, end := int64(0), int64(6)
	req := domain.ConflictResolutionCommand{
		ResolutionType:  "supersede",
		ExpectedTargets: []domain.EvolutionTarget{selected},
		SelectedTarget:  &selected,
		ProposedContent: &content,
		Evidence:        []domain.MemoryEvidence{{EventID: "33333333-3333-4333-8333-333333333333", EvidenceType: "manual_reference", StartOffset: &start, EndOffset: &end}},
		ReplacementMode: &mode,
		ResultStates:    map[string]string{selected.MemoryID: "superseded"},
		Reason:          "reviewed",
	}
	if err := validateConflictResolution(&req); err != nil {
		t.Fatal(err)
	}
	params := storage.ResolveConflictParams(req)
	if params.SelectedTarget == nil || params.SelectedTarget.MemoryID != selected.MemoryID || params.ProposedContent == nil || *params.ProposedContent != content || len(params.Evidence) != 1 || params.ReplacementMode == nil || *params.ReplacementMode != mode {
		t.Fatalf("payload not preserved: %+v", params)
	}
}
