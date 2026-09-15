package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/retrieval"
)

func entityProposalIdentity(key, action, id string, value any) WriteIdentity {
	return memoryIdentity(key, "/v1/entity-proposals/{proposal_id}/"+action, struct {
		ID    string
		Value any
	}{id, value})
}

func createEntityProposalTest(t *testing.T, r *Repository, p CreateEntityProposalParams) domainEntityProposal {
	t.Helper()
	v, replay, err := r.CreateEntityProposal(context.Background(), memoryIdentity("proposal-"+p.CanonicalName+p.Alias+p.ProposalKind, "/v1/entity-proposals", p), p)
	if err != nil || replay {
		t.Fatalf("CreateEntityProposal() = %+v, %t, %v", v, replay, err)
	}
	return domainEntityProposal{v.ProposalID}
}

type domainEntityProposal struct{ ID string }

func confirmEntityTest(t *testing.T, r *Repository, name string) string {
	t.Helper()
	p := CreateEntityProposalParams{EventID: "event-1", SpaceID: "space-1", ProposalKind: "entity", EntityType: "person", CanonicalName: name, Sensitivity: "normal", ProcessorIdentity: "processor/test", ProcessingVersion: "entity-v1"}
	proposal := createEntityProposalTest(t, r, p)
	review := ReviewEntityProposalParams{ReasonCode: "confirmed"}
	v, replay, err := r.ConfirmEntityProposal(context.Background(), entityProposalIdentity("confirm-"+name, "confirm", proposal.ID, review), proposal.ID, review)
	if err != nil || replay || v.ResultEntityID == nil {
		t.Fatalf("ConfirmEntityProposal() = %+v, %t, %v", v, replay, err)
	}
	return *v.ResultEntityID
}

func TestConfirmEntityProposalAtomic(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	start, end := 0, 6
	p := CreateEntityProposalParams{EventID: "event-1", SpaceID: "space-1", ProposalKind: "entity", EntityType: "person", CanonicalName: "Alice", Alias: "Ally", NormalizationVersion: "v1", MentionEventID: stringPointer("event-1"), StartOffset: &start, EndOffset: &end, MentionType: "explicit", Sensitivity: "normal", ProcessorIdentity: "processor/test", ProcessingVersion: "entity-v1"}
	proposal := createEntityProposalTest(t, r, p)
	review := ReviewEntityProposalParams{ReasonCode: "confirmed"}
	identity := entityProposalIdentity("confirm-Alice", "confirm", proposal.ID, review)
	result, replay, err := r.ConfirmEntityProposal(context.Background(), identity, proposal.ID, review)
	if err != nil || replay || result.ResultEntityID == nil || result.ResultCanonicalAliasID == nil || result.ResultAliasID == nil || result.ResultMentionID == nil || len(result.Reviews) != 1 || result.Reviews[0].ResultCanonicalAliasID == nil || *result.Reviews[0].ResultCanonicalAliasID != *result.ResultCanonicalAliasID || result.Reviews[0].ResultAliasID == nil || *result.Reviews[0].ResultAliasID != *result.ResultAliasID || result.Reviews[0].ResultMentionID == nil || *result.Reviews[0].ResultMentionID != *result.ResultMentionID {
		t.Fatalf("ConfirmEntityProposal() = %+v, %t, %v", result, replay, err)
	}
	replayed, replay, err := r.ConfirmEntityProposal(context.Background(), identity, proposal.ID, review)
	if err != nil || !replay || replayed.ResultMentionID == nil || *replayed.ResultMentionID != *result.ResultMentionID {
		t.Fatalf("ConfirmEntityProposal() replay = %+v, %t, %v", replayed, replay, err)
	}
	var entities, aliases, mentions, reviews, audits, jobs, idempotency int
	err = r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM entities WHERE entity_id=?),(SELECT count(*) FROM entity_aliases WHERE entity_id=?),(SELECT count(*) FROM entity_mentions WHERE entity_id=?),(SELECT count(*) FROM entity_proposal_reviews),(SELECT count(*) FROM audit_events WHERE action='confirm_entity_proposal'),(SELECT count(*) FROM jobs WHERE job_type='rebuild_entity_index'),(SELECT count(*) FROM idempotency_records WHERE normalized_route LIKE '/v1/entity-proposals/%')`, *result.ResultEntityID, *result.ResultEntityID, *result.ResultEntityID).Scan(&entities, &aliases, &mentions, &reviews, &audits, &jobs, &idempotency)
	if err != nil || entities != 1 || aliases != 2 || mentions != 1 || reviews != 1 || audits != 1 || jobs != 1 || idempotency != 1 {
		t.Fatalf("confirmed entity rows=%d/%d/%d/%d/%d/%d/%d, error=%v", entities, aliases, mentions, reviews, audits, jobs, idempotency, err)
	}
}

func TestEntityMentionRevisionAndRuneRangeRejectedAtomically(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	entityID := confirmEntityTest(t, r, "范围")
	memory := createTestMemory(t, r, "entity-range-memory")
	badRevision := "missing"
	start, end := 0, 99
	for _, revision := range []*string{&badRevision, &memory.CurrentRevision.RevisionID} {
		p := CreateEntityProposalParams{EventID: "event-1", SpaceID: "space-1", ProposalKind: "mention", EntityID: &entityID, MentionMemoryID: &memory.MemoryID, MentionRevisionID: revision, StartOffset: &start, EndOffset: &end, MentionType: "explicit", Sensitivity: "normal", ProcessorIdentity: "processor/test", ProcessingVersion: "entity-v1"}
		if _, _, err := r.CreateEntityProposal(context.Background(), memoryIdentity("bad-mention-"+*revision, "/v1/entity-proposals", p), p); !errors.Is(err, ErrInvalidEntityProposal) {
			t.Fatalf("invalid entity mention accepted or returned unexpected error: %v", err)
		}
	}
	var mentions int
	r.store.reader.QueryRow(`SELECT count(*) FROM entity_mentions`).Scan(&mentions)
	if mentions != 0 {
		t.Fatalf("invalid entity mentions persisted=%d, want 0", mentions)
	}
}

func TestEntityAliasCollisionCreatesMergeProposalOnly(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	first := confirmEntityTest(t, r, "Alpha")
	second := confirmEntityTest(t, r, "Beta")
	alias := " ALPHA "
	p := CreateEntityProposalParams{EventID: "event-2", SpaceID: "space-1", ProposalKind: "alias", EntityID: &second, Alias: alias, NormalizationVersion: "v1", Sensitivity: "normal", ProcessorIdentity: "processor/test", ProcessingVersion: "entity-v1"}
	proposal := createEntityProposalTest(t, r, p)
	review := ReviewEntityProposalParams{ReasonCode: "alias"}
	if _, _, err := r.ConfirmEntityProposal(context.Background(), entityProposalIdentity("alias-collision", "confirm", proposal.ID, review), proposal.ID, review); err != nil {
		t.Fatal(err)
	}
	var merges int
	r.store.reader.QueryRow(`SELECT count(*) FROM entity_merge_proposals WHERE source_entity_id=? AND target_entity_id=? AND state='pending'`, second, first).Scan(&merges)
	if merges != 1 {
		t.Fatalf("alias collision merge proposals=%d, want 1", merges)
	}
	var entityCount int
	r.store.reader.QueryRow(`SELECT count(*) FROM entities`).Scan(&entityCount)
	if entityCount != 2 {
		t.Fatalf("identity merged unexpectedly: %d", entityCount)
	}
}

func TestEntitySiblingSpaceRejectedAndNotListed(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	entityID := confirmEntityTest(t, r, "Scoped")
	now := "2026-08-21T11:00:00Z"
	_, err := r.store.writer.Exec(`INSERT INTO memory_spaces(space_id,parent_space_id,space_type,external_id,display_name,created_at) VALUES ('sibling','space-1','project','sibling','Sibling',?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	start, end := 0, 1
	p := CreateEntityProposalParams{EventID: "event-1", SpaceID: "sibling", ProposalKind: "mention", EntityID: &entityID, MentionEventID: stringPointer("event-1"), StartOffset: &start, EndOffset: &end, MentionType: "explicit", Sensitivity: "normal", ProcessorIdentity: "p", ProcessingVersion: "v"}
	if _, _, err = r.CreateEntityProposal(context.Background(), memoryIdentity("sibling-entity", "/v1/entity-proposals", p), p); !errors.Is(err, ErrInvalidEntityProposal) {
		t.Fatalf("sibling-space entity proposal error=%v, want scope violation", err)
	}
	page, err := r.ListEntities(context.Background(), EntityFilter{SpaceID: "sibling", Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("sibling-space entity list=%+v, error=%v", page, err)
	}
}

func TestEntityStaleRevisionMentionNotRecalled(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	enableGraphCandidates(r)
	entityID := confirmEntityTest(t, r, "RecallName")
	memory := createTestMemory(t, r, "entity-recall-memory")
	start, end := 0, 1
	p := CreateEntityProposalParams{EventID: "event-1", SpaceID: "space-1", ProposalKind: "mention", EntityID: &entityID, MentionMemoryID: &memory.MemoryID, MentionRevisionID: &memory.CurrentRevision.RevisionID, StartOffset: &start, EndOffset: &end, MentionType: "explicit", Sensitivity: "normal", ProcessorIdentity: "p", ProcessingVersion: "v"}
	proposal := createEntityProposalTest(t, r, p)
	review := ReviewEntityProposalParams{ReasonCode: "mention"}
	if _, _, err := r.ConfirmEntityProposal(context.Background(), entityProposalIdentity("mention-recall", "confirm", proposal.ID, review), proposal.ID, review); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildEntityIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := r.SearchMemories(context.Background(), SearchParams{Query: "RecallName", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, GraphPolicy: retrieval.GraphDirect, Limit: 10, MaxChars: 10000})
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("current recall=%+v err=%v", result, err)
	}
	newRevision := "new-revision"
	now := "2026-08-21T13:00:00Z"
	if _, err = r.store.writer.Exec(`INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,kind,display_content,canonical_content,canonical_hash,sensitivity,specificity,confidence,created_by,processing_version,created_at) VALUES (?,?,?,'fact','replacement','replacement','replacement-hash','normal',0,0,'test','v',?); UPDATE memories SET current_revision_id=?,updated_at=? WHERE memory_id=?`, newRevision, memory.MemoryID, memory.CurrentRevision.RevisionID, now, newRevision, now, memory.MemoryID); err != nil {
		t.Fatal(err)
	}
	result, err = r.SearchMemories(context.Background(), SearchParams{Query: "RecallName", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Channels: []string{"keyword", "graph"}, GraphPolicy: retrieval.GraphDirect, Limit: 10, MaxChars: 10000})
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("stale entity mention recall=%+v, error=%v", result, err)
	}
}

func TestEntityIndexRebuildFromAuthority(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	confirmEntityTest(t, r, "Indexed")
	if _, err := r.store.writer.Exec(`UPDATE jobs SET status='succeeded',finished_at='2026-08-21T12:30:00Z' WHERE job_type='rebuild_entity_index' AND status='pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.writer.Exec(`UPDATE entity_index_metadata SET state='degraded',source_entity_count=0,source_mention_count=99 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	tx, err := r.store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := r.newIDs(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enqueueEntityIndexJob(context.Background(), tx, ids[0], ids[1], r.processingVersion, r.maxAttempts, formatTime(r.runtime.Now())); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := r.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Hour})
	if err != nil || claim.Job.JobID != ids[0] {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if err = r.RebuildEntityIndexClaimed(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if err = r.CompleteJob(context.Background(), claim, JobResult{Code: "index_rebuilt"}); err != nil {
		t.Fatal(err)
	}
	var state string
	var entities, mentions int
	if err = r.store.reader.QueryRow(`SELECT state,source_entity_count,source_mention_count FROM entity_index_metadata WHERE singleton=1`).Scan(&state, &entities, &mentions); err != nil || state != "ready" || entities != 1 || mentions != 0 {
		t.Fatalf("rebuilt entity index metadata=%s/%d/%d, error=%v", state, entities, mentions, err)
	}
}

func TestEntityNormalizationUnicodePunctuationWhitespace(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  Ａｌｉｃｅ　Ｓｍｉｔｈ ", "alice smith"},
		{"O'Brien, Alice!", "obrien alice"},
		{"爱丽丝。", "爱丽丝"},
		{"Alice\t\nSmith", "alice smith"},
		{"ALPHA", "alpha"},
		{"  --  ", ""},
		{"Müller", "muller"},
	}
	for _, test := range tests {
		if got := normalizeEntityName(test.in); got != test.want {
			t.Fatalf("normalizeEntityName(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestEntityNormalizationVersionIsStable(t *testing.T) {
	if entityNormalizationVersion == "" || entityNormalizationPolicy == "" {
		t.Fatal("normalization identity must be defined")
	}
	if normalizeEntityName("Alice") != normalizeEntityName("alice") {
		t.Fatal("normalization is not case-folded")
	}
}

func TestAliasPublicationPolicyRejectsWeakAliases(t *testing.T) {
	cases := []struct {
		alias, entityType string
		confidence        float64
		want              bool
	}{
		{"Alice", "person", 0.95, true},
		{"Al", "person", 0.99, false},
		{"he", "person", 0.99, false},
		{"they", "person", 0.99, false},
		{"it", "concept", 0.99, false},
		{"company", "organization", 0.99, false},
		{"thing", "concept", 0.99, false},
		{"Alice", "person", 0.5, false},
		{"爱丽丝", "person", 0.95, true},
		{"", "person", 0.99, false},
	}
	for _, test := range cases {
		if got := aliasPublishable(test.alias, test.entityType, test.confidence); got != test.want {
			t.Fatalf("aliasPublishable(%q, %q, %v) = %v, want %v", test.alias, test.entityType, test.confidence, got, test.want)
		}
	}
}

func TestEntityTypeCompatibilityRejectsUnknownTypes(t *testing.T) {
	if validEntityType("rocket") {
		t.Fatal("unknown entity type accepted")
	}
	for _, typ := range []string{"person", "organization", "place", "project", "product", "event", "concept", "other"} {
		if !validEntityType(typ) {
			t.Fatalf("known entity type %q rejected", typ)
		}
	}
}
