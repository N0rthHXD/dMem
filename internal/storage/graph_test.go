package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func enableGraphExtraction(r *Repository) {
	r.SetGraphExtractionEnabled(true)
}

// enableGraphCandidates raises the configured graph trust so graph-only
// direct/path candidates can enter. Production defaults to features_only.
func enableGraphCandidates(r *Repository) {
	r.SetRetrievalCalibration(retrieval.DefaultWeights(), retrieval.DefaultCalibration(), retrieval.GraphDirect, 0, 0, 0)
}

func seedEntity(t *testing.T, r *Repository, entityID, name, typ string) {
	t.Helper()
	now := "2026-08-21T11:00:00Z"
	if _, err := r.store.writer.Exec(`INSERT INTO entities(entity_id, space_id, entity_type, canonical_name, normalized_name, normalization_version, state, created_by, processing_version, created_at, updated_at) VALUES (?, 'space-1', ?, ?, ?, 'v1', 'active', 'test', 'v1', ?, ?)`, entityID, typ, name, normalizeEntityName(name), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.writer.Exec(`INSERT INTO entity_aliases(alias_id, entity_id, alias, normalized_alias, normalization_version, source, state, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, 'v1', 'human_confirmation', 'active', 'test', ?, ?)`, "alias-"+entityID, entityID, name, normalizeEntityName(name), now, now); err != nil {
		t.Fatal(err)
	}
}

func seedMention(t *testing.T, r *Repository, mentionID, entityID, memoryID, revisionID string, start, end int) {
	t.Helper()
	if _, err := r.store.writer.Exec(`INSERT INTO entity_mentions(mention_id, entity_id, memory_id, revision_id, start_offset, end_offset, mention_type, source, surface_text, created_by, processing_version, created_at) VALUES (?, ?, ?, ?, ?, ?, 'explicit', 'human_confirmation', 'surface', 'test', 'v1', '2026-08-21T11:00:00Z')`, mentionID, entityID, memoryID, revisionID, start, end); err != nil {
		t.Fatal(err)
	}
}

func TestGraphExtractionScheduledOnConfirmedRevision(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	memory := createTestMemory(t, r, "graph-schedule")
	var count int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='extract_graph' AND target_type='memory_revision' AND target_id=?`, memory.CurrentRevision.RevisionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("extract_graph job count=%d, err=%v", count, err)
	}
	// scheduling is idempotent for the same revision
	tx, err := r.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.scheduleGraphExtraction(context.Background(), tx, memory.CurrentRevision.RevisionID, "2026-08-21T13:00:00Z"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='extract_graph' AND target_type='memory_revision' AND target_id=?`, memory.CurrentRevision.RevisionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate extract_graph jobs=%d, err=%v", count, err)
	}
}

func TestGraphExtractionNotScheduledWhenDisabled(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	createTestMemory(t, r, "graph-disabled")
	var count int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='extract_graph'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("extract_graph jobs when disabled=%d, err=%v", count, err)
	}
}

func TestUnconfirmedCandidateDoesNotWriteGraphAuthority(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	candidate := CreateCandidateParams{EventID: "event-1", SpaceID: "space-1", ProcessorIdentity: "p", ProcessingVersion: "v", Evidence: directEvidence("event-1"), Revision: domain.MemoryRevision{Kind: domain.MemoryKindFact, DisplayContent: "Alice uses PostgreSQL", CanonicalContent: "Alice uses PostgreSQL", Sensitivity: "normal", Confidence: 0.9, Evidence: []domain.RevisionEvidence{{EvidenceID: "graph-evidence", EventID: "event-1", Role: domain.EvidenceRoleDirect}}}}
	created, _, err := r.CreateCandidate(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if created.ReviewState != "pending" {
		t.Fatal("candidate should be pending")
	}
	var entities, facts int
	if err := r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM entities), (SELECT count(*) FROM entity_facts)`).Scan(&entities, &facts); err != nil || entities != 0 || facts != 0 {
		t.Fatalf("graph authority after unconfirmed candidate entities=%d facts=%d err=%v", entities, facts, err)
	}
}

func graphClaimInput(t *testing.T, r *Repository) (ClaimedJob, domain.GraphExtractionInput) {
	t.Helper()
	// Rebuild jobs are scheduled alongside graph extraction; complete them so the
	// next claim is the extract_graph job.
	if _, err := r.store.writer.Exec(`UPDATE jobs SET status='succeeded', finished_at='2026-08-21T12:00:00Z' WHERE job_type IN ('rebuild_entity_index','rebuild_graph_index','rebuild_relation_index') AND status IN ('pending','waiting','running','retrying')`); err != nil {
		t.Fatal(err)
	}
	claim, err := r.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "graph-worker", LeaseDuration: time.Hour})
	if err != nil || claim.Job.JobType != "extract_graph" {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	input, err := r.PrepareGraphExtraction(context.Background(), claim, ProcessingPreparationParams{ProcessorIdentity: "processor/graph", MaxCandidates: 20, MaxContentChars: 12000})
	if err != nil {
		t.Fatalf("prepare graph: %v", err)
	}
	return claim, input
}

func createGraphMemory(t *testing.T, r *Repository, key, content string) domain.Memory {
	t.Helper()
	params := CreateMemoryParams{SpaceID: "space-1", Kind: "fact", Content: content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "graph extraction"}
	memory, replay, err := r.CreateMemory(context.Background(), memoryIdentity(key, "/v1/memories", params), params)
	if err != nil || replay {
		t.Fatalf("create memory %s: %+v, replay=%t, err=%v", key, memory, replay, err)
	}
	return memory
}

func TestGraphCommitResolvesEntityAndCreatesFactsAndMentions(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	memory := createGraphMemory(t, r, "graph-commit", "Alice participates in Alpha")
	claim, input := graphClaimInput(t, r)

	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{
			{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Aliases: []string{"Alice"}, Confidence: 0.95},
			{LocalID: "e1", CanonicalName: "Alpha", EntityType: "project", Confidence: 0.9},
		},
		Mentions: []domain.GraphMention{{EntityLocalID: "e0", Text: "Alice", StartRune: 0, EndRune: 5, Confidence: 0.9}},
		Facts:    []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "participates_in", ObjectLocalID: "e1", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatalf("commit graph: %v", err)
	}
	var facts int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_facts WHERE subject_entity_id='entity-alice' AND predicate='participates_in' AND object_entity_id='entity-alpha' AND state='active'`).Scan(&facts); err != nil || facts != 1 {
		t.Fatalf("resolved entity facts=%d err=%v", facts, err)
	}
	var mentions int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_mentions WHERE entity_id='entity-alice' AND memory_id=?`, memory.MemoryID).Scan(&mentions); err != nil || mentions != 1 {
		t.Fatalf("mentions=%d err=%v", mentions, err)
	}
}

func TestGraphFactSupersedesOlderFactOnNewRevision(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	memory := createTestMemory(t, r, "fact-evolve")

	// First revision: Alice has active status.
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95}},
		Facts:    []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "has_status", ObjectLiteral: "active", ObjectLiteralType: "string", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	// Second revision: Alice is paused -> conflicting fact stays pending review.
	revised, _, err := r.ReviseMemory(context.Background(), memoryIdentity("fact-evolve-revise", "/v1/memories/{memory_id}/revisions", ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "Alice is paused", Evidence: directEvidence("event-2"), Reason: "update"}), memory.MemoryID, ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "Alice is paused", Evidence: directEvidence("event-2"), Reason: "update"})
	if err != nil {
		t.Fatal(err)
	}
	_ = revised
	claim2, input2 := graphClaimInput(t, r)
	result2 := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95}},
		Facts:    []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "has_status", ObjectLiteral: "paused", ObjectLiteralType: "string", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim2, input2, GraphExtractionCommit{Result: result2}); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_fact_proposals WHERE predicate='has_status' AND review_state='pending'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("conflicting fact pending proposals=%d err=%v", pending, err)
	}
	// Confirming the newer fact supersedes the older active status.
	page, err := r.ListFactProposals(context.Background(), FactProposalFilter{SpaceID: "space-1", ReviewState: "pending", Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("pending fact proposals=%+v err=%v", page, err)
	}
	if _, _, err := r.ConfirmFactProposal(context.Background(), memoryIdentity("fact-evolve-confirm", "/v1/fact-proposals/{proposal_id}/confirm", ReviewFactProposalParams{ReasonCode: "updated"}), page.Items[0].ProposalID, ReviewFactProposalParams{ReasonCode: "updated"}); err != nil {
		t.Fatal(err)
	}
	var active, superseded int
	if err := r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM entity_facts WHERE predicate='has_status' AND state='active' AND object_literal='paused'), (SELECT count(*) FROM entity_facts WHERE predicate='has_status' AND state='superseded' AND object_literal='active')`).Scan(&active, &superseded); err != nil || active != 1 || superseded != 1 {
		t.Fatalf("temporal evolution active=%d superseded=%d err=%v", active, superseded, err)
	}
}

func TestGraphCommitEnqueuesRelationIndexRebuild(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	createGraphMemory(t, r, "graph-rel-source", "Alice works on Alpha")
	createGraphMemory(t, r, "graph-rel-target", "Alpha is a project")

	if _, err := r.store.writer.Exec(`UPDATE jobs SET status='succeeded', finished_at='2026-08-21T12:00:00Z' WHERE job_type IN ('rebuild_entity_index','rebuild_graph_index','rebuild_relation_index') AND status IN ('pending','waiting','running','retrying')`); err != nil {
		t.Fatal(err)
	}
	claim, err := r.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "graph-worker", LeaseDuration: time.Hour})
	if err != nil || claim.Job.JobType != "extract_graph" {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	input, err := r.PrepareGraphExtraction(context.Background(), claim, ProcessingPreparationParams{ProcessorIdentity: "processor/graph", MaxCandidates: 20, MaxContentChars: 12000, MaxContextMemories: 5})
	if err != nil {
		t.Fatalf("prepare graph: %v", err)
	}
	if len(input.ContextMemories) == 0 {
		t.Fatal("expected context memories for relation target")
	}
	target := input.ContextMemories[0]

	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities:  []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95}},
		Relations: []domain.GraphMemoryRelation{{RelationType: "supports", TargetMemoryID: target.MemoryID, TargetRevisionID: target.RevisionID, Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatalf("commit graph: %v", err)
	}
	var count int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE job_type='rebuild_relation_index' AND status IN ('pending','waiting','running','retrying','succeeded')`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("rebuild_relation_index jobs=%d err=%v", count, err)
	}
}

func TestGraphStaleSourceRevisionRejected(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	memory := createTestMemory(t, r, "graph-stale")
	claim, input := graphClaimInput(t, r)
	// Make the revision stale by revising the memory after preparing input.
	if _, _, err := r.ReviseMemory(context.Background(), memoryIdentity("graph-stale-revise", "/v1/memories/{memory_id}/revisions", ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "replaced", Evidence: directEvidence("event-2"), Reason: "revise"}), memory.MemoryID, ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "replaced", Evidence: directEvidence("event-2"), Reason: "revise"}); err != nil {
		t.Fatal(err)
	}
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision commit error=%v, want ErrRevisionConflict", err)
	}
}

func TestEntityResolutionQueryFindsAliasesInNaturalLanguage(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	seedEntity(t, r, "entity-cn", "爱丽丝", "person")
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, resolveErr := resolveQueryEntities(context.Background(), tx, "What did Alice decide about Alpha?", []string{"space-1"})
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if len(resolved) != 2 {
		t.Fatalf("english resolution=%+v", resolved)
	}
	cn, cnErr := resolveQueryEntities(context.Background(), tx, "爱丽丝参与了哪个项目？", []string{"space-1"})
	if cnErr != nil {
		t.Fatal(cnErr)
	}
	if len(cn) != 1 || cn[0].EntityID != "entity-cn" {
		t.Fatalf("chinese resolution=%+v", cn)
	}
}

func TestGraphRecallDirectMentionAndFactAndMemoryRelation(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphCandidates(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	mentionMemory := createTestMemory(t, r, "graph-mention-mem")
	seedMention(t, r, "mention-alice", "entity-alice", mentionMemory.MemoryID, mentionMemory.CurrentRevision.RevisionID, 0, 5)

	// fact memory: source revision supporting an Alice->Alpha participates_in fact
	factMemory := createTestMemory(t, r, "graph-fact-mem")
	if _, err := r.store.writer.Exec(`INSERT INTO entity_facts(fact_id, space_id, subject_entity_id, predicate, object_entity_id, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('fact-alice-alpha', 'space-1', 'entity-alice', 'participates_in', 'entity-alpha', ?, ?, 0.9, 'active', 'auto_review', 'hash', 'test', 'v1', '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, factMemory.MemoryID, factMemory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.writer.Exec(`INSERT INTO entity_fact_evidence(evidence_id,fact_id,evidence_type,created_by,created_at) VALUES ('fact-alice-alpha-evidence','fact-alice-alpha','human_confirmation','test','2026-08-21T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// memory relation: factMemory supports mentionMemory
	if _, err := r.store.writer.Exec(`INSERT INTO memory_relations(relation_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, state, source, confidence, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('rel-supports', 'space-1', 'supports', ?, ?, ?, ?, 'active', 'human_confirmation', 0.8, 'hash', 'test', 'v1', '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, factMemory.MemoryID, factMemory.CurrentRevision.RevisionID, mentionMemory.MemoryID, mentionMemory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}

	result, err := r.SearchMemories(context.Background(), SearchParams{Query: "Alice participates in Alpha", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, Limit: 20, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ResolvedEntities) != 2 {
		t.Fatalf("resolved entities=%+v", result.ResolvedEntities)
	}
	signals := map[string]bool{}
	for _, item := range result.Results {
		for _, signal := range item.GraphSignals {
			signals[signal.Kind] = true
		}
	}
	if !signals["direct_entity_mention"] || !signals["exact_predicate_fact"] {
		t.Fatalf("graph signals=%v results=%+v", signals, result.Results)
	}
}

func TestGraphFactWindowOrdersByConfidenceBeforeLimit(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	seedEntity(t, r, "entity-beta", "Beta", "project")
	high := createGraphMemory(t, r, "graph-window-high", "high confidence fact")
	low := createGraphMemory(t, r, "graph-window-low", "low confidence fact")
	insertFact := func(factID, objectID, memoryID, revisionID string, confidence float64) {
		t.Helper()
		if _, err := r.store.writer.Exec(`INSERT INTO entity_facts(fact_id, space_id, subject_entity_id, predicate, object_entity_id, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, created_at, updated_at) VALUES (?, 'space-1', 'entity-alice', 'participates_in', ?, ?, ?, ?, 'active', 'auto_review', 'hash', 'test', 'v1', '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, factID, objectID, memoryID, revisionID, confidence); err != nil {
			t.Fatal(err)
		}
		if _, err := r.store.writer.Exec(`INSERT INTO entity_fact_evidence(evidence_id,fact_id,evidence_type,created_by,created_at) VALUES (?,?,'human_confirmation','test','2026-08-21T11:00:00Z')`, factID+"-evidence", factID); err != nil {
			t.Fatal(err)
		}
	}
	insertFact("fact-window-high", "entity-alpha", high.MemoryID, high.CurrentRevision.RevisionID, 0.95)
	insertFact("fact-window-low", "entity-beta", low.MemoryID, low.CurrentRevision.RevisionID, 0.55)
	if err := r.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	params := SearchParams{SpaceIDs: []string{"space-1"}, States: []string{"active"}, ReferenceTime: "2026-08-21T12:00:00Z"}
	policy := graphRecallPolicy{Policy: retrieval.GraphDirect, MinEntityConfidence: 0, MinFactConfidence: 0, AllowNewCandidates: true, Primary: map[string]struct{}{}}
	resolved := []domain.ResolvedEntity{{EntityID: "entity-alice", EntityType: "person", Alias: "Alice", Confidence: 0.9, MatchKind: "alias"}}
	graph, err := r.graphChannelRecall(context.Background(), tx, params, []string{"space-1"}, policy, resolved, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.items) != 1 || graph.items[0].memoryID != high.MemoryID {
		t.Fatalf("graph fact window = %+v, want only the high-confidence fact", graph.items)
	}
}

func TestGraphIndexGenerationChangeInvalidatesCursor(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	createGraphMemory(t, r, "graph-cursor-a", "graphcursor one")
	createGraphMemory(t, r, "graph-cursor-b", "graphcursor two")
	createMatchingMemories(t, r, "graph-cursor", "graphcursor", minSelectionWindow)
	params := SearchParams{Query: "graphcursor", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, Limit: 1, MaxChars: 1000}
	first, err := r.SearchMemories(context.Background(), params)
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err := r.store.writer.Exec(`UPDATE graph_index_metadata SET generation=generation+1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	params.Cursor = *first.NextCursor
	if _, err := r.SearchMemories(context.Background(), params); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor after graph generation change error=%v", err)
	}
}

func TestFactProposalConcurrentConfirm(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	memory := createTestMemory(t, r, "fact-concurrent")
	conf := 0.9
	lit, typ := "active", "string"
	p := CreateFactProposalParams{EventID: "event-1", SpaceID: "space-1", SubjectEntityID: "entity-alice", Predicate: "has_status", ObjectLiteral: &lit, ObjectLiteralType: &typ, SourceMemoryID: memory.MemoryID, SourceRevisionID: memory.CurrentRevision.RevisionID, Confidence: &conf, Evidence: []domain.EntityFactEvidence{{EvidenceType: "human_confirmation"}}, ProcessorIdentity: "p", ProcessingVersion: "v"}
	proposal, _, err := r.CreateFactProposal(context.Background(), memoryIdentity("fact-concurrent-create", "/v1/fact-proposals", p), p)
	if err != nil {
		t.Fatal(err)
	}
	review := ReviewFactProposalParams{ReasonCode: "confirmed"}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, _, err := r.ConfirmFactProposal(context.Background(), memoryIdentity("fact-confirm-"+string(rune('a'+i)), "/v1/fact-proposals/{proposal_id}/confirm", review), proposal.ProposalID, review)
			results <- err
		}()
	}
	wins, reviewed := 0, 0
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			wins++
		case errors.Is(err, ErrFactProposalReviewed):
			reviewed++
		default:
			t.Fatalf("concurrent confirm: %v", err)
		}
	}
	if wins != 1 || reviewed != 1 {
		t.Fatalf("concurrent confirm wins=%d reviewed=%d", wins, reviewed)
	}
	var facts int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_facts WHERE subject_entity_id='entity-alice' AND predicate='has_status' AND state='active'`).Scan(&facts); err != nil || facts != 1 {
		t.Fatalf("facts after concurrent confirm=%d err=%v", facts, err)
	}
}

func TestAmbiguousAliasIsNotResolvedByUUIDOrder(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-z", "Jordan", "person")
	seedEntity(t, r, "entity-a", "Jordan", "person")
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveQueryEntities(context.Background(), tx, "What did Jordan say?", []string{"space-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 {
		t.Fatalf("ambiguous resolution = %+v", resolved)
	}
	for _, entity := range resolved {
		if !entity.Ambiguous || entity.MatchKind != "alias_ambiguous" {
			t.Fatalf("ambiguous alias resolved without marker: %+v", entity)
		}
		if len(entity.Alternatives) != 1 {
			t.Fatalf("alternatives = %v", entity.Alternatives)
		}
	}
}

func TestUnresolvedAmbiguityDisablesGraphExpansion(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphCandidates(r)
	seedEntity(t, r, "entity-z", "Jordan", "person")
	seedEntity(t, r, "entity-a", "Jordan", "person")
	memory := createMemoryWithContent(t, r, "ambiguous-mention", "unrelated content body")
	seedMention(t, r, "mention-ambiguous", "entity-z", memory.MemoryID, memory.CurrentRevision.RevisionID, 0, 6)
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveQueryEntities(context.Background(), tx, "Jordan", []string{"space-1"})
	if err != nil {
		t.Fatal(err)
	}
	params := r.planQuery(context.Background(), SearchParams{Query: "Jordan", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Limit: 10, ReferenceTime: formatTime(r.runtime.Now())})
	graph, err := r.graphChannelRecall(context.Background(), tx, params, []string{"space-1"}, graphRecallPolicy{
		Policy: retrieval.GraphDirect, MinEntityConfidence: 0.6, MinFactConfidence: 0.7,
		AllowNewCandidates: true, Primary: map[string]struct{}{},
	}, resolved, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.items) != 0 {
		t.Fatalf("ambiguous mention produced graph candidates: %+v", graph.items)
	}
}

func TestGraphChannelIsFailClosedWhenUnready(t *testing.T) {
	for _, state := range []string{"stale", "rebuilding", "degraded"} {
		t.Run(state, func(t *testing.T) {
			r, _ := newMemoryTestRepository(t)
			createMemoryWithContent(t, r, "graph-state-"+state, "graphstate word")
			if _, err := r.store.writer.Exec(`UPDATE graph_index_metadata SET state=? WHERE singleton=1`, state); err != nil {
				t.Fatal(err)
			}
			result, err := r.SearchMemories(context.Background(), SearchParams{Query: "graphstate", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, Limit: 10, MaxChars: 10000})
			if err != nil {
				t.Fatal(err)
			}
			if contains(result.ExecutedChannels, "graph") {
				t.Fatalf("graph remained executed in state %q", state)
			}
			if !result.Degraded || !contains(result.DegradationReasons, "graph_"+state) {
				t.Fatalf("degradation not reported: %+v", result)
			}
		})
	}
}

func TestGraphChannelIsFailClosedWhenPartialOrDependencyUnready(t *testing.T) {
	for _, test := range []struct {
		name, setup, reason string
	}{
		{"partial", `UPDATE graph_index_metadata SET source_entity_count=1 WHERE singleton=1`, "graph_partial"},
		{"entity", `UPDATE entity_index_metadata SET state='degraded' WHERE singleton=1`, "graph_entity_degraded"},
		{"relation", `UPDATE relation_index_metadata SET state='rebuilding' WHERE singleton=1`, "graph_relation_rebuilding"},
		{"incompatible", `UPDATE entity_index_metadata SET normalization_version='v0' WHERE singleton=1`, "graph_incompatible"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, _ := newMemoryTestRepository(t)
			createMemoryWithContent(t, r, "graph-readiness-"+test.name, "readinessword")
			if err := r.RebuildGraphIndex(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := r.store.writer.Exec(test.setup); err != nil {
				t.Fatal(err)
			}
			result, err := r.SearchMemories(context.Background(), SearchParams{Query: "readinessword", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, Limit: 10, MaxChars: 10000})
			if err != nil || contains(result.ExecutedChannels, "graph") || !result.Degraded || !contains(result.DegradationReasons, test.reason) {
				t.Fatalf("readiness result=%+v err=%v", result, err)
			}
		})
	}
}

func TestGraphPolicyOffIsIntentionalNotDegraded(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	result, err := r.SearchMemories(context.Background(), SearchParams{Query: "anything", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, GraphPolicy: retrieval.GraphOff, Limit: 10, MaxChars: 10000})
	if err != nil || result.Degraded || contains(result.ExecutedChannels, "graph") {
		t.Fatalf("graph off result=%+v err=%v", result, err)
	}
}

func TestEmptyEligibilityStillReportsGraphReadiness(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	if _, err := r.store.writer.Exec(`UPDATE graph_index_metadata SET state='stale' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	result, err := r.SearchMemories(context.Background(), SearchParams{Query: "anything", SpaceIDs: []string{"space-1"}, States: []string{}, Channels: []string{"keyword", "graph"}, Limit: 10, MaxChars: 1000})
	if err != nil || contains(result.ExecutedChannels, "graph") || !contains(result.DegradationReasons, "graph_stale") {
		t.Fatalf("empty eligibility execution = %+v, %v", result, err)
	}
}

func TestGraphMentionConfidenceIsPersistedAndConsumed(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	createGraphMemory(t, r, "graph-confidence", "Alice is here")
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95}},
		Mentions: []domain.GraphMention{{EntityLocalID: "e0", Text: "Alice", StartRune: 0, EndRune: 5, Confidence: 0.7}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var confidence float64
	if err := r.store.reader.QueryRow(`SELECT confidence FROM entity_mentions WHERE entity_id='entity-alice'`).Scan(&confidence); err != nil {
		t.Fatal(err)
	}
	if confidence != 0.7 {
		t.Fatalf("persisted mention confidence = %v", confidence)
	}
}

func TestGraphFactPublicationUsesPredicateThreshold(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-acme", "Acme", "organization")
	createGraphMemory(t, r, "graph-threshold", "Alice is a member of Acme")
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{
			{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95},
			{LocalID: "e1", CanonicalName: "Acme", EntityType: "organization", Confidence: 0.95},
		},
		Facts: []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "member_of", ObjectLocalID: "e1", Confidence: 0.7}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var active, pending int
	if err := r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM entity_facts WHERE predicate='member_of' AND state='active'), (SELECT count(*) FROM entity_fact_proposals WHERE predicate='member_of' AND review_state='pending')`).Scan(&active, &pending); err != nil || active != 0 || pending != 1 {
		t.Fatalf("threshold publication active=%d pending=%d err=%v", active, pending, err)
	}
}

func TestGraphFactAutoConfirmsAtPredicateThreshold(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-acme", "Acme", "organization")
	createGraphMemory(t, r, "graph-threshold-ok", "Alice is a member of Acme")
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{
			{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95},
			{LocalID: "e1", CanonicalName: "Acme", EntityType: "organization", Confidence: 0.95},
		},
		Facts: []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "member_of", ObjectLocalID: "e1", Confidence: 0.9}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_facts WHERE predicate='member_of' AND state='active'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("auto-confirmed facts=%d err=%v", active, err)
	}
}

func TestGraphFactManyCardinalityAllowsMultipleObjects(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-acme", "Acme", "organization")
	seedEntity(t, r, "entity-beta", "Beta", "organization")
	createGraphMemory(t, r, "graph-many", "Alice belongs to Acme and Beta")
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{
			{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95},
			{LocalID: "e1", CanonicalName: "Acme", EntityType: "organization", Confidence: 0.95},
			{LocalID: "e2", CanonicalName: "Beta", EntityType: "organization", Confidence: 0.95},
		},
		Facts: []domain.GraphFact{
			{SubjectLocalID: "e0", Predicate: "member_of", ObjectLocalID: "e1", Confidence: 0.9},
			{SubjectLocalID: "e0", Predicate: "member_of", ObjectLocalID: "e2", Confidence: 0.9},
		},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_facts WHERE predicate='member_of' AND state='active'`).Scan(&active); err != nil || active != 2 {
		t.Fatalf("many-cardinality active facts=%d err=%v", active, err)
	}
}

func TestGraphFactLiteralTypeMustMatchPredicate(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-project", "Alpha", "project")
	createGraphMemory(t, r, "graph-literal-type", "Alpha status")
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alpha", EntityType: "project", Confidence: 0.95}},
		Facts:    []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "has_status", ObjectLiteral: "active", ObjectLiteralType: "number", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); !errors.Is(err, ErrInvalidFactProposal) {
		t.Fatalf("literal type mismatch error=%v, want ErrInvalidFactProposal", err)
	}
}

func TestGraphCommitPublishesTypedEventParticipantsPlaceAndTime(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-paris", "Paris", "place")
	createGraphMemory(t, r, "graph-event", "Alice met in Paris")
	claim, input := graphClaimInput(t, r)
	start, end := "2024-03-01T09:00:00Z", "2024-03-01T10:00:00Z"
	startTime, _ := time.Parse(time.RFC3339, start)
	endTime, _ := time.Parse(time.RFC3339, end)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{
			{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95},
			{LocalID: "e1", CanonicalName: "Paris", EntityType: "place", Confidence: 0.95},
		},
		Events: []domain.GraphEvent{{
			LocalID: "x0", EventType: "meeting", ParticipantLocalIDs: []string{"e0"}, PlaceLocalID: "e1",
			Time:       &domain.GraphEventTime{Start: &startTime, End: &endTime, Precision: domain.TimePrecisionExact, Source: "explicit"},
			Confidence: 0.9,
		}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatalf("commit event: %v", err)
	}
	var participants, places, intervals int
	if err := r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM event_participants WHERE event_id='event-1' AND entity_id='entity-alice'), (SELECT count(*) FROM event_temporal_bindings WHERE event_id='event-1' AND binding_kind='occurred_at' AND place_entity_id='entity-paris'), (SELECT count(*) FROM event_temporal_bindings WHERE event_id='event-1' AND binding_kind='occurred_during')`).Scan(&participants, &places, &intervals); err != nil || participants != 1 || places != 1 || intervals != 1 {
		t.Fatalf("typed event rows participants=%d places=%d intervals=%d err=%v", participants, places, intervals, err)
	}
}

func TestGraphFactEvidenceIsDirectRevisionRange(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphExtraction(r)
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-acme", "Acme", "organization")
	createGraphMemory(t, r, "graph-evidence-direct", "Alice belongs to Acme")
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{
			{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95},
			{LocalID: "e1", CanonicalName: "Acme", EntityType: "organization", Confidence: 0.95},
		},
		Facts: []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "member_of", ObjectLocalID: "e1", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var direct int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_fact_evidence WHERE evidence_type='revision_range'`).Scan(&direct); err != nil || direct != 1 {
		t.Fatalf("direct revision evidence=%d err=%v", direct, err)
	}
}

func TestEntityResolutionTypeHintDisambiguates(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-sam-person", "Sam", "person")
	seedEntity(t, r, "entity-sam-org", "Sam", "organization")
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveQueryEntitiesWithHints(context.Background(), tx, "What did Sam decide?", []string{"space-1"}, entityResolutionHints{EntityTypes: []string{"person"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].EntityID != "entity-sam-person" || resolved[0].Ambiguous {
		t.Fatalf("type-hinted resolution = %+v", resolved)
	}
}

func TestEntityResolutionAmbiguityProbabilitiesNormalizedByPrior(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-zeta", "Jordan", "person")
	seedEntity(t, r, "entity-alpha", "Jordan", "person")
	firstMemory := createTestMemory(t, r, "ambiguity-prior-a")
	secondMemory := createTestMemory(t, r, "ambiguity-prior-b")
	for i := 0; i < 3; i++ {
		seedMention(t, r, "mention-zeta-"+strconvItoa2(i), "entity-zeta", firstMemory.MemoryID, firstMemory.CurrentRevision.RevisionID, i*2, i*2+1)
	}
	seedMention(t, r, "mention-alpha-0", "entity-alpha", secondMemory.MemoryID, secondMemory.CurrentRevision.RevisionID, 0, 1)
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveQueryEntities(context.Background(), tx, "Jordan", []string{"space-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 {
		t.Fatalf("ambiguous resolution = %+v", resolved)
	}
	total := 0.0
	for _, entity := range resolved {
		if !entity.Ambiguous {
			t.Fatalf("alternative not marked ambiguous: %+v", entity)
		}
		total += entity.Confidence
	}
	if total < 0.99 || total > 1.01 {
		t.Fatalf("probabilities not normalized: %v", resolved)
	}
	var zeta, alpha domain.ResolvedEntity
	for _, entity := range resolved {
		if entity.EntityID == "entity-zeta" {
			zeta = entity
		}
		if entity.EntityID == "entity-alpha" {
			alpha = entity
		}
	}
	if zeta.Confidence <= alpha.Confidence {
		t.Fatalf("prior-based probability did not favor the higher-degree entity: zeta=%v alpha=%v", zeta.Confidence, alpha.Confidence)
	}
}

func TestEntityResolutionInsufficientConfidenceDisablesExpansion(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphCandidates(r)
	seedEntity(t, r, "entity-zeta", "Jordan", "person")
	seedEntity(t, r, "entity-alpha", "Jordan", "person")
	memory := createTestMemory(t, r, "ambiguous-expansion")
	seedMention(t, r, "mention-ambiguous-expansion", "entity-zeta", memory.MemoryID, memory.CurrentRevision.RevisionID, 0, 1)
	tx, err := r.store.reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveQueryEntities(context.Background(), tx, "Jordan", []string{"space-1"})
	if err != nil {
		t.Fatal(err)
	}
	params := r.planQuery(context.Background(), SearchParams{Query: "Jordan", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Limit: 10, ReferenceTime: formatTime(r.runtime.Now())})
	graph, err := r.graphChannelRecall(context.Background(), tx, params, []string{"space-1"}, graphRecallPolicy{
		Policy: retrieval.GraphDirect, MinEntityConfidence: 0.6, MinFactConfidence: 0.7,
		AllowNewCandidates: true, Primary: map[string]struct{}{},
	}, resolved, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.items) != 0 {
		t.Fatalf("low-confidence ambiguity produced graph candidates: %+v", graph.items)
	}
}

func TestEntityGraphStatisticsProjectionAndGenerationBinding(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	seedEntity(t, r, "entity-zeta", "Zeta", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	memory := createTestMemory(t, r, "stats-memory")
	for i := 0; i < 3; i++ {
		seedMention(t, r, "stats-mention-"+strconvItoa2(i), "entity-zeta", memory.MemoryID, memory.CurrentRevision.RevisionID, i*2, i*2+1)
	}
	seedMention(t, r, "stats-mention-alpha", "entity-alpha", memory.MemoryID, memory.CurrentRevision.RevisionID, 100, 101)
	if _, err := r.store.writer.Exec(`INSERT INTO entity_facts(fact_id, space_id, subject_entity_id, predicate, object_entity_id, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('stats-fact', 'space-1', 'entity-zeta', 'participates_in', 'entity-alpha', ?, ?, 0.9, 'active', 'auto_review', 'hash', 'test', 'v1', '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, memory.MemoryID, memory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	statistic, err := r.EntityGraphStatistic(context.Background(), "entity-zeta")
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := r.store.reader.QueryRow(`SELECT generation FROM graph_index_metadata WHERE singleton=1`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if statistic.MentionCount != 3 || statistic.Degree != 1 || statistic.Generation != generation {
		t.Fatalf("statistic = %+v, generation=%d", statistic, generation)
	}
	if statistic.InverseMentionFrequency <= 0 || statistic.InverseDegree <= 0 {
		t.Fatalf("inverse frequencies not populated: %+v", statistic)
	}
	seedMention(t, r, "stats-mention-extra", "entity-zeta", memory.MemoryID, memory.CurrentRevision.RevisionID, 200, 201)
	if err := r.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, err := r.EntityGraphStatistic(context.Background(), "entity-zeta")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation <= statistic.Generation || updated.MentionCount != 4 {
		t.Fatalf("projection did not advance: before=%+v after=%+v", statistic, updated)
	}
}
