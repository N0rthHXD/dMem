package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func enableGraphExtraction(r *Repository) {
	r.SetGraphExtractionEnabled(true)
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
	candidate := CreateCandidateParams{EventID: "event-1", SpaceID: "space-1", MemoryType: "fact", Content: "Alice uses PostgreSQL", Sensitivity: "normal", SuggestionKind: "new", ProcessorIdentity: "p", ProcessingVersion: "v", Evidence: directEvidence("event-1")}
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
	params := CreateMemoryParams{SpaceID: "space-1", MemoryType: "fact", Content: content, Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "graph extraction"}
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

	// First revision: Alice uses PostgreSQL.
	claim, input := graphClaimInput(t, r)
	result := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95}},
		Facts:    []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "uses_database", ObjectLiteral: "PostgreSQL", ObjectLiteralType: "string", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim, input, GraphExtractionCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	// Second revision: Alice uses MySQL -> conflicting fact stays pending review.
	revised, _, err := r.ReviseMemory(context.Background(), memoryIdentity("fact-evolve-revise", "/v1/memories/{memory_id}/revisions", ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "Alice uses MySQL", Evidence: directEvidence("event-2"), Reason: "update"}), memory.MemoryID, ReviseMemoryParams{ExpectedRevisionID: memory.CurrentRevision.RevisionID, Content: "Alice uses MySQL", Evidence: directEvidence("event-2"), Reason: "update"})
	if err != nil {
		t.Fatal(err)
	}
	_ = revised
	claim2, input2 := graphClaimInput(t, r)
	result2 := domain.GraphExtractionResult{Code: domain.GraphExtractionExtracted,
		Entities: []domain.GraphEntity{{LocalID: "e0", CanonicalName: "Alice", EntityType: "person", Confidence: 0.95}},
		Facts:    []domain.GraphFact{{SubjectLocalID: "e0", Predicate: "uses_database", ObjectLiteral: "MySQL", ObjectLiteralType: "string", Confidence: 0.95}},
	}
	if err := r.CommitGraphExtraction(context.Background(), claim2, input2, GraphExtractionCommit{Result: result2}); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_fact_proposals WHERE predicate='uses_database' AND review_state='pending'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("conflicting fact pending proposals=%d err=%v", pending, err)
	}
	// Confirming the newer fact supersedes the older PostgreSQL fact.
	page, err := r.ListFactProposals(context.Background(), FactProposalFilter{SpaceID: "space-1", ReviewState: "pending", Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("pending fact proposals=%+v err=%v", page, err)
	}
	if _, _, err := r.ConfirmFactProposal(context.Background(), memoryIdentity("fact-evolve-confirm", "/v1/fact-proposals/{proposal_id}/confirm", ReviewFactProposalParams{ReasonCode: "updated"}), page.Items[0].ProposalID, ReviewFactProposalParams{ReasonCode: "updated"}); err != nil {
		t.Fatal(err)
	}
	var active, superseded int
	if err := r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM entity_facts WHERE predicate='uses_database' AND state='active' AND object_literal='MySQL'), (SELECT count(*) FROM entity_facts WHERE predicate='uses_database' AND state='superseded' AND object_literal='PostgreSQL')`).Scan(&active, &superseded); err != nil || active != 1 || superseded != 1 {
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
	seedEntity(t, r, "entity-alice", "Alice", "person")
	seedEntity(t, r, "entity-alpha", "Alpha", "project")
	mentionMemory := createTestMemory(t, r, "graph-mention-mem")
	seedMention(t, r, "mention-alice", "entity-alice", mentionMemory.MemoryID, mentionMemory.CurrentRevision.RevisionID, 0, 5)

	// fact memory: source revision supporting an Alice->Alpha participates_in fact
	factMemory := createTestMemory(t, r, "graph-fact-mem")
	if _, err := r.store.writer.Exec(`INSERT INTO entity_facts(fact_id, space_id, subject_entity_id, predicate, object_entity_id, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('fact-alice-alpha', 'space-1', 'entity-alice', 'participates_in', 'entity-alpha', ?, ?, 0.9, 'active', 'auto_review', 'hash', 'test', 'v1', '2026-08-21T11:00:00Z', '2026-08-21T11:00:00Z')`, factMemory.MemoryID, factMemory.CurrentRevision.RevisionID); err != nil {
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

func TestGraphIndexGenerationChangeInvalidatesCursor(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	createGraphMemory(t, r, "graph-cursor-a", "graphcursor one")
	createGraphMemory(t, r, "graph-cursor-b", "graphcursor two")
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
	lit, typ := "PostgreSQL", "string"
	p := CreateFactProposalParams{EventID: "event-1", SpaceID: "space-1", SubjectEntityID: "entity-alice", Predicate: "uses_database", ObjectLiteral: &lit, ObjectLiteralType: &typ, SourceMemoryID: memory.MemoryID, SourceRevisionID: memory.CurrentRevision.RevisionID, Confidence: &conf, Evidence: []domain.EntityFactEvidence{{EvidenceType: "human_confirmation"}}, ProcessorIdentity: "p", ProcessingVersion: "v"}
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
	if err := r.store.reader.QueryRow(`SELECT count(*) FROM entity_facts WHERE subject_entity_id='entity-alice' AND predicate='uses_database' AND state='active'`).Scan(&facts); err != nil || facts != 1 {
		t.Fatalf("facts after concurrent confirm=%d err=%v", facts, err)
	}
}
