package storage

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func evolutionParams(action string, memories ...domain.Memory) CreateEvolutionProposalParams {
	start, end := 0, 6
	content := "updated searchable memory"
	targets := make([]domain.EvolutionTarget, len(memories))
	for i, memory := range memories {
		targets[i] = domain.EvolutionTarget{MemoryID: memory.MemoryID, ExpectedRevisionID: memory.CurrentRevision.RevisionID}
	}
	return CreateEvolutionProposalParams{EventID: "event-1", SpaceID: "space-1", Action: action, ProposedContent: &content, Sensitivity: "normal", ProcessorIdentity: "processor/evolution", ProcessingVersion: "evolution-v1", Targets: targets, Evidence: []domain.RelationEvidence{{EvidenceType: "raw_event_range", EventID: stringPointer("event-1"), StartOffset: &start, EndOffset: &end}}}
}

func createEvolutionTest(t *testing.T, r *Repository, key string, p CreateEvolutionProposalParams) domain.EvolutionProposal {
	t.Helper()
	v, _, err := r.CreateEvolutionProposal(context.Background(), memoryIdentity(key, "/v1/evolution-proposals", p), p)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func confirmEvolutionTest(t *testing.T, r *Repository, key string, p domain.EvolutionProposal, mode *string) domain.EvolutionProposal {
	t.Helper()
	review := ReviewEvolutionProposalParams{ExpectedTargets: p.Targets, ReplacementMode: mode, ReasonCode: "reviewed"}
	v, _, err := r.ConfirmEvolutionProposal(context.Background(), memoryIdentity(key, "/v1/evolution-proposals/{proposal_id}/confirm", review), p.ProposalID, review)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEvolutionSupplementConfirmAtomic(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	memory := createTestMemory(t, r, "evolve-supplement-memory")
	p := createEvolutionTest(t, r, "evolve-supplement", evolutionParams("supplement", memory))
	confirmed := confirmEvolutionTest(t, r, "evolve-supplement-confirm", p, nil)
	if confirmed.ReviewState != "confirmed" || len(confirmed.Reviews) != 1 || len(confirmed.Reviews[0].Results) != 2 {
		t.Fatalf("supplement confirmation proposal=%+v", confirmed)
	}
	var revisions, evidence, reviews, projections, jobs int
	err := r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_revisions WHERE memory_id=?),(SELECT count(*) FROM memory_evidence me JOIN memory_revisions mr ON mr.revision_id=me.revision_id WHERE mr.memory_id=?),(SELECT count(*) FROM evolution_proposal_reviews WHERE proposal_id=?),(SELECT count(*) FROM memory_projections WHERE memory_id=?),(SELECT count(*) FROM jobs WHERE job_type='rebuild_fts')`, memory.MemoryID, memory.MemoryID, p.ProposalID, memory.MemoryID).Scan(&revisions, &evidence, &reviews, &projections, &jobs)
	if err != nil || revisions != 2 || evidence != 2 || reviews != 1 || projections != 1 || jobs != 1 {
		t.Fatalf("supplement confirmation rows=%d/%d/%d/%d/%d, error=%v", revisions, evidence, reviews, projections, jobs, err)
	}
}

func TestEvolutionReplacementModesAndSupersedes(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	old := createTestMemory(t, r, "replace-old")
	p := createEvolutionTest(t, r, "replace-proposal", evolutionParams("replacement", old))
	mode := "new_memory"
	confirmed := confirmEvolutionTest(t, r, "replace-confirm", p, &mode)
	var relations, memories int
	r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_relations WHERE relation_type='supersedes'),(SELECT count(*) FROM memories)`).Scan(&relations, &memories)
	if relations != 1 || memories != 2 || len(confirmed.Reviews[0].Results) != 3 {
		t.Fatalf("replacement confirmation relations=%d memories=%d proposal=%+v", relations, memories, confirmed)
	}
}

func TestEvolutionConfirmIdempotentReplay(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	m := createTestMemory(t, r, "replay-memory")
	p := createEvolutionTest(t, r, "replay-proposal", evolutionParams("supplement", m))
	review := ReviewEvolutionProposalParams{ExpectedTargets: p.Targets, ReasonCode: "reviewed"}
	identity := memoryIdentity("evolve-confirm-replay", "/v1/evolution-proposals/{proposal_id}/confirm", review)
	first, replay, err := r.ConfirmEvolutionProposal(context.Background(), identity, p.ProposalID, review)
	if err != nil || replay {
		t.Fatal(err)
	}
	second, replay, err := r.ConfirmEvolutionProposal(context.Background(), identity, p.ProposalID, review)
	if err != nil || !replay || first.Reviews[0].ReviewID != second.Reviews[0].ReviewID {
		t.Fatalf("evolution confirmation replay=%v, error=%v", replay, err)
	}
}

func TestEvolutionStaleGenerationCannotCreateProposal(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	m := createTestMemory(t, r, "fence-memory")
	now := formatTime(r.runtime.Now())
	_, err := r.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,lease_owner,lease_until,lease_generation,created_at) VALUES ('evolve-job','op','extract_memory','raw_event','event-1','evolution-v1','running',3,?,'worker',?,2,?)`, now, "2099-01-01T00:00:00Z", now)
	if err != nil {
		t.Fatal(err)
	}
	claim := ClaimedJob{Job: domain.Job{JobID: "evolve-job"}, LeaseOwner: "worker", LeaseGeneration: 1}
	if _, _, err = r.CreateEvolutionProposalClaimed(context.Background(), claim, evolutionParams("supplement", m)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale generation proposal error=%v, want stale generation", err)
	}
	var count int
	r.store.reader.QueryRow(`SELECT count(*) FROM evolution_proposals`).Scan(&count)
	if count != 0 {
		t.Fatalf("stale generation wrote %d proposals", count)
	}
}

func TestEvolutionTransactionFailureLeavesNoAuthority(t *testing.T) {
	r, runtime := newMemoryTestRepository(t)
	m := createTestMemory(t, r, "failure-memory")
	p := createEvolutionTest(t, r, "failure-proposal", evolutionParams("supplement", m))
	runtime.mu.Lock()
	runtime.failAt = runtime.id + 1
	runtime.mu.Unlock()
	review := ReviewEvolutionProposalParams{ExpectedTargets: p.Targets, ReasonCode: "reviewed"}
	if _, _, err := r.ConfirmEvolutionProposal(context.Background(), memoryIdentity("failure-confirm", "/v1/evolution-proposals/{proposal_id}/confirm", review), p.ProposalID, review); err == nil {
		t.Fatal("expected injected transaction failure")
	}
	got, _ := r.GetEvolutionProposal(context.Background(), p.ProposalID)
	if got.ReviewState != "pending" || getMemoryRevisionCount(t, r, m.MemoryID) != 1 {
		t.Fatalf("transaction failure left proposal authority=%+v", got)
	}
}

func TestEvolutionContextMemoryWithoutEvidenceRejected(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	m := createTestMemory(t, r, "evidence-memory")
	p := evolutionParams("supplement", m)
	p.Evidence = []domain.RelationEvidence{{EvidenceType: "human_confirmation"}}
	proposal := createEvolutionTest(t, r, "evidence-proposal", p)
	review := ReviewEvolutionProposalParams{ExpectedTargets: proposal.Targets, ReasonCode: "reviewed"}
	if _, _, err := r.ConfirmEvolutionProposal(context.Background(), memoryIdentity("evidence-confirm", "/v1/evolution-proposals/{proposal_id}/confirm", review), proposal.ProposalID, review); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("context memory without evidence error=%v, want invalid evidence", err)
	}
}

func getMemoryRevisionCount(t *testing.T, r *Repository, id string) int {
	t.Helper()
	var n int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM memory_revisions WHERE memory_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConfirmConflictAtomic(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, r, "conflict-a"), createTestMemory(t, r, "conflict-b")
	p := createEvolutionTest(t, r, "conflict-proposal", evolutionParams("conflict", a, b))
	confirmed := confirmEvolutionTest(t, r, "conflict-confirm", p, nil)
	var relations, cases, participants, reviews int
	r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_relations WHERE relation_type='contradicts'),(SELECT count(*) FROM conflict_cases),(SELECT count(*) FROM conflict_case_participants),(SELECT count(*) FROM evolution_proposal_reviews)`).Scan(&relations, &cases, &participants, &reviews)
	gotA, err := r.GetMemory(context.Background(), a.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := r.GetMemory(context.Background(), b.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if relations != 1 || cases != 1 || participants != 2 || reviews != 1 || confirmed.ReviewState != "confirmed" || gotA.CurrentState != "contested" || gotB.CurrentState != "contested" {
		t.Fatalf("confirmed conflict rows=%d/%d/%d/%d, want 1/1/2/1", relations, cases, participants, reviews)
	}
}

func TestSearchContestedConflictRefs(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, r, "search-conflict-a"), createTestMemory(t, r, "search-conflict-b")
	p := createEvolutionTest(t, r, "search-conflict", evolutionParams("conflict", a, b))
	confirmEvolutionTest(t, r, "search-conflict-confirm", p, nil)
	response, err := r.SearchMemories(context.Background(), SearchParams{Query: "searchable", SpaceIDs: []string{"space-1"}, States: []string{"contested"}, Mode: "keyword", Limit: 10, MaxChars: 10000})
	if err != nil || len(response.Results) != 2 {
		t.Fatalf("contested conflict search response=%+v, error=%v", response, err)
	}
	for _, result := range response.Results {
		if len(result.ConflictRefs) != 1 {
			t.Fatalf("missing refs: %+v", result)
		}
	}
}

func TestConflictConcurrentResolutionOneTerminalEvent(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, r, "resolve-a"), createTestMemory(t, r, "resolve-b")
	p := createEvolutionTest(t, r, "resolve-proposal", evolutionParams("conflict", a, b))
	confirmed := confirmEvolutionTest(t, r, "resolve-confirm", p, nil)
	caseID := confirmed.Reviews[0].Results[1].ResultID
	params := ResolveConflictParams{ResolutionType: "keep_both", ExpectedTargets: p.Targets, ResultStates: map[string]string{a.MemoryID: "active", b.MemoryID: "active"}, Reason: "reviewed"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := r.ResolveConflict(context.Background(), memoryIdentity("resolution-"+string(rune('a'+i)), "/v1/conflict-cases/{case_id}/resolve", params), caseID, params)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflictResolved) {
			t.Fatal(err)
		}
	}
	var events int
	r.store.reader.QueryRow(`SELECT count(*) FROM conflict_resolution_events WHERE case_id=?`, caseID).Scan(&events)
	if success != 1 || events != 1 {
		t.Fatalf("concurrent conflict resolutions succeeded=%d terminal events=%d, want 1/1", success, events)
	}
}

func TestConflictMultiTargetRevisionChangedNoPartialUpdate(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, r, "stale-a"), createTestMemory(t, r, "stale-b")
	p := createEvolutionTest(t, r, "stale-conflict", evolutionParams("conflict", a, b))
	revise := ReviseMemoryParams{ExpectedRevisionID: b.CurrentRevision.RevisionID, Content: "changed", Evidence: directEvidence("event-1"), Reason: "changed"}
	if _, _, err := r.ReviseMemory(context.Background(), memoryIdentity("stale-revise", "/v1/memories/{memory_id}", revise), b.MemoryID, revise); err != nil {
		t.Fatal(err)
	}
	review := ReviewEvolutionProposalParams{ExpectedTargets: p.Targets, ReasonCode: "reviewed"}
	if _, _, err := r.ConfirmEvolutionProposal(context.Background(), memoryIdentity("stale-confirm", "/v1/evolution-proposals/{proposal_id}/confirm", review), p.ProposalID, review); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("changed target revision resolution error=%v, want revision conflict", err)
	}
	var cases, relations int
	r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM conflict_cases),(SELECT count(*) FROM memory_relations)`).Scan(&cases, &relations)
	gotA, err := r.GetMemory(context.Background(), a.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if cases != 0 || relations != 0 || gotA.CurrentState != "active" {
		t.Fatalf("partial rows=%d/%d", cases, relations)
	}
}

func TestConflictFrozenParticipantPreservesProposal(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, r, "frozen-a"), createTestMemory(t, r, "frozen-b")
	p := createEvolutionTest(t, r, "frozen-conflict", evolutionParams("conflict", a, b))
	_, err := r.store.writer.Exec(`UPDATE memories SET frozen=1 WHERE memory_id=?`, b.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	review := ReviewEvolutionProposalParams{ExpectedTargets: p.Targets, ReasonCode: "reviewed"}
	if _, _, err = r.ConfirmEvolutionProposal(context.Background(), memoryIdentity("frozen-confirm", "/v1/evolution-proposals/{proposal_id}/confirm", review), p.ProposalID, review); !errors.Is(err, ErrFrozenRevision) {
		t.Fatalf("frozen participant resolution error=%v, want frozen memory", err)
	}
	got, _ := r.GetEvolutionProposal(context.Background(), p.ProposalID)
	if got.ReviewState != "pending" {
		t.Fatalf("proposal state=%s", got.ReviewState)
	}
}

func TestConflictAutomaticDestructiveResolutionUnsupported(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b := createTestMemory(t, r, "unsupported-a"), createTestMemory(t, r, "unsupported-b")
	p := createEvolutionTest(t, r, "unsupported-conflict", evolutionParams("conflict", a, b))
	confirmed := confirmEvolutionTest(t, r, "unsupported-confirm", p, nil)
	caseID := confirmed.Reviews[0].Results[1].ResultID
	params := ResolveConflictParams{ResolutionType: "automatic_retract", ExpectedTargets: p.Targets, Reason: "automatic"}
	if _, _, err := r.ResolveConflict(context.Background(), memoryIdentity("unsupported-resolution", "/v1/conflict-cases/{case_id}/resolve", params), caseID, params); !errors.Is(err, ErrUnsupportedAction) {
		t.Fatalf("automatic destructive resolution error=%v, want unsupported operation", err)
	}
	c, _ := r.GetConflictCase(context.Background(), caseID)
	if c.State != "open" {
		t.Fatalf("case state=%s", c.State)
	}
}

func openConflictTest(t *testing.T, r *Repository, key string) (domain.Memory, domain.Memory, domain.EvolutionProposal, string) {
	t.Helper()
	a, b := createTestMemory(t, r, key+"-a"), createTestMemory(t, r, key+"-b")
	p := createEvolutionTest(t, r, key+"-proposal", evolutionParams("conflict", a, b))
	confirmed := confirmEvolutionTest(t, r, key+"-confirm", p, nil)
	return a, b, p, confirmed.Reviews[0].Results[1].ResultID
}

func conflictResolutionEvidence() []domain.MemoryEvidence {
	start, end := int64(0), int64(6)
	return []domain.MemoryEvidence{{EventID: "event-1", EvidenceType: "manual_reference", StartOffset: &start, EndOffset: &end}}
}

func TestConflictResolutionTypesMaterializeResults(t *testing.T) {
	tests := []struct {
		name, resolutionType string
		build                func(domain.Memory, domain.Memory, domain.EvolutionProposal) ResolveConflictParams
		wantMemories         int
		wantRevisions        int
		wantRelations        int
		wantCaseState        string
	}{
		{name: "keep_both", resolutionType: "keep_both", wantMemories: 2, wantRevisions: 2, wantRelations: 1, wantCaseState: "resolved"},
		{name: "dismiss", resolutionType: "dismiss", wantMemories: 2, wantRevisions: 2, wantRelations: 1, wantCaseState: "dismissed"},
		{name: "revise_one", resolutionType: "revise_one", wantMemories: 2, wantRevisions: 3, wantRelations: 1, wantCaseState: "resolved", build: func(a, b domain.Memory, p domain.EvolutionProposal) ResolveConflictParams {
			content := "reviewed revision"
			selected := p.Targets[0]
			return ResolveConflictParams{SelectedTarget: &selected, ProposedContent: &content, Evidence: conflictResolutionEvidence()}
		}},
		{name: "supersede", resolutionType: "supersede", wantMemories: 3, wantRevisions: 3, wantRelations: 2, wantCaseState: "resolved", build: func(a, b domain.Memory, p domain.EvolutionProposal) ResolveConflictParams {
			content, mode := "authoritative replacement", "new_memory"
			selected := p.Targets[0]
			return ResolveConflictParams{SelectedTarget: &selected, ProposedContent: &content, Evidence: conflictResolutionEvidence(), ReplacementMode: &mode}
		}},
		{name: "supersede_same_memory", resolutionType: "supersede", wantMemories: 2, wantRevisions: 3, wantRelations: 2, wantCaseState: "resolved", build: func(a, b domain.Memory, p domain.EvolutionProposal) ResolveConflictParams {
			content, mode := "authoritative child replacement", "same_memory_revision"
			selected := p.Targets[0]
			from, until := "2026-08-22T00:00:00Z", "2026-09-22T00:00:00Z"
			return ResolveConflictParams{SelectedTarget: &selected, ProposedContent: &content, ValidFrom: &from, ValidUntil: &until, Evidence: conflictResolutionEvidence(), ReplacementMode: &mode}
		}},
		{name: "reconcile", resolutionType: "reconcile", wantMemories: 3, wantRevisions: 3, wantRelations: 3, wantCaseState: "resolved", build: func(a, b domain.Memory, p domain.EvolutionProposal) ResolveConflictParams {
			content := "fully reconciled conclusion"
			return ResolveConflictParams{ProposedContent: &content, Evidence: conflictResolutionEvidence()}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newMemoryTestRepository(t)
			a, b, proposal, caseID := openConflictTest(t, r, "resolution-"+tt.name)
			params := ResolveConflictParams{ResolutionType: tt.resolutionType, ExpectedTargets: proposal.Targets, ResultStates: map[string]string{a.MemoryID: "active", b.MemoryID: "active"}, Reason: "reviewed resolution"}
			if tt.build != nil {
				extra := tt.build(a, b, proposal)
				params.SelectedTarget, params.ProposedContent, params.ValidFrom, params.ValidUntil = extra.SelectedTarget, extra.ProposedContent, extra.ValidFrom, extra.ValidUntil
				params.Evidence, params.ReplacementMode = extra.Evidence, extra.ReplacementMode
			}
			if tt.resolutionType == "supersede" {
				params.ResultStates[a.MemoryID] = "superseded"
			}
			if tt.name == "supersede_same_memory" {
				params.ResultStates[a.MemoryID] = "active"
			}
			if tt.resolutionType == "reconcile" {
				params.ResultStates[a.MemoryID], params.ResultStates[b.MemoryID] = "superseded", "superseded"
			}
			resolved, replay, err := r.ResolveConflict(context.Background(), memoryIdentity("resolve-"+tt.name, "/v1/conflict-cases/{case_id}/resolve", params), caseID, params)
			if err != nil || replay || resolved.State != tt.wantCaseState || resolved.Resolution == nil || resolved.Resolution.ResolutionType != tt.resolutionType {
				t.Fatalf("resolution=%+v replay=%v err=%v", resolved, replay, err)
			}
			var memories, revisions, relations, refs int
			err = r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memories),(SELECT count(*) FROM memory_revisions),(SELECT count(*) FROM memory_relations),(SELECT count(*) FROM conflict_resolution_result_refs WHERE resolution_event_id=?)`, resolved.Resolution.ResolutionEventID).Scan(&memories, &revisions, &relations, &refs)
			if err != nil || memories != tt.wantMemories || revisions != tt.wantRevisions || relations != tt.wantRelations || refs == 0 {
				t.Fatalf("rows memories=%d revisions=%d relations=%d refs=%d err=%v", memories, revisions, relations, refs, err)
			}
			if tt.resolutionType == "revise_one" || tt.resolutionType == "supersede" || tt.resolutionType == "reconcile" {
				var evidence, projections int
				if err = r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM conflict_resolution_result_refs WHERE resolution_event_id=? AND result_type='evidence'),(SELECT count(*) FROM conflict_resolution_result_refs WHERE resolution_event_id=? AND result_type='projection')`, resolved.Resolution.ResolutionEventID, resolved.Resolution.ResolutionEventID).Scan(&evidence, &projections); err != nil || evidence == 0 || projections == 0 {
					t.Fatalf("materialized evidence=%d projections=%d err=%v", evidence, projections, err)
				}
			}
			if tt.name == "supersede_same_memory" {
				var parent, validFrom, validUntil string
				if err = r.store.reader.QueryRow(`SELECT parent_revision_id,valid_from,valid_until FROM memory_revisions WHERE content='authoritative child replacement'`).Scan(&parent, &validFrom, &validUntil); err != nil || parent != a.CurrentRevision.RevisionID || validFrom != "2026-08-22T00:00:00Z" || validUntil != "2026-09-22T00:00:00Z" {
					t.Fatalf("replacement parent/time=%q %q %q err=%v", parent, validFrom, validUntil, err)
				}
			}
			second, replay, err := r.ResolveConflict(context.Background(), memoryIdentity("resolve-"+tt.name, "/v1/conflict-cases/{case_id}/resolve", params), caseID, params)
			if err != nil || !replay || second.Resolution == nil || second.Resolution.ResolutionEventID != resolved.Resolution.ResolutionEventID {
				t.Fatalf("idempotent replay=%v err=%v result=%+v", replay, err, second)
			}
		})
	}
}

func TestConflictResolutionStaleParticipantNoPartial(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	a, b, proposal, caseID := openConflictTest(t, r, "resolution-stale")
	revise := ReviseMemoryParams{ExpectedRevisionID: b.CurrentRevision.RevisionID, Content: "concurrent change", Evidence: directEvidence("event-1"), Reason: "changed"}
	if _, _, err := r.ReviseMemory(context.Background(), memoryIdentity("resolution-stale-revise", "/v1/memories/{memory_id}", revise), b.MemoryID, revise); err != nil {
		t.Fatal(err)
	}
	content := "must not be written"
	selected := proposal.Targets[0]
	params := ResolveConflictParams{ResolutionType: "revise_one", ExpectedTargets: proposal.Targets, SelectedTarget: &selected, ProposedContent: &content, Evidence: conflictResolutionEvidence(), ResultStates: map[string]string{a.MemoryID: "active", b.MemoryID: "active"}, Reason: "reviewed"}
	if _, _, err := r.ResolveConflict(context.Background(), memoryIdentity("resolution-stale-resolve", "/v1/conflict-cases/{case_id}/resolve", params), caseID, params); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("error=%v", err)
	}
	var events, written int
	r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM conflict_resolution_events WHERE case_id=?),(SELECT count(*) FROM memory_revisions WHERE content='must not be written')`, caseID).Scan(&events, &written)
	caseValue, err := r.GetConflictCase(context.Background(), caseID)
	if err != nil || caseValue.State != "open" || events != 0 || written != 0 {
		t.Fatalf("case=%+v events=%d written=%d err=%v", caseValue, events, written, err)
	}
}

func TestConflictResolutionInjectedFailureNoPartial(t *testing.T) {
	r, runtime := newMemoryTestRepository(t)
	a, b, proposal, caseID := openConflictTest(t, r, "resolution-failure")
	content := "rolled back reconciliation"
	params := ResolveConflictParams{ResolutionType: "reconcile", ExpectedTargets: proposal.Targets, ProposedContent: &content, Evidence: conflictResolutionEvidence(), ResultStates: map[string]string{a.MemoryID: "superseded", b.MemoryID: "superseded"}, Reason: "reviewed"}
	runtime.mu.Lock()
	runtime.failAt = runtime.id + 10
	runtime.mu.Unlock()
	if _, _, err := r.ResolveConflict(context.Background(), memoryIdentity("resolution-failure-resolve", "/v1/conflict-cases/{case_id}/resolve", params), caseID, params); err == nil {
		t.Fatal("expected injected failure")
	}
	var memories, revisions, events int
	r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memories),(SELECT count(*) FROM memory_revisions WHERE content='rolled back reconciliation'),(SELECT count(*) FROM conflict_resolution_events WHERE case_id=?)`, caseID).Scan(&memories, &revisions, &events)
	caseValue, err := r.GetConflictCase(context.Background(), caseID)
	if err != nil || caseValue.State != "open" || memories != 2 || revisions != 0 || events != 0 {
		t.Fatalf("case=%+v memories=%d revisions=%d events=%d err=%v", caseValue, memories, revisions, events, err)
	}
}
