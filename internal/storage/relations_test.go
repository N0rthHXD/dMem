package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func relationParams(source, target domain.Memory) CreateRelationProposalParams {
	return CreateRelationProposalParams{
		EventID: "event-1", SpaceID: "space-1", RelationType: "supports",
		SourceMemoryID: source.MemoryID, SourceRevisionID: source.CurrentRevision.RevisionID,
		TargetMemoryID: target.MemoryID, TargetRevisionID: target.CurrentRevision.RevisionID,
		Evidence:          []domain.RelationEvidence{{EvidenceType: "human_confirmation"}},
		ProcessorIdentity: "processor/test", ProcessingVersion: "relation-v1",
	}
}

func createRelationProposalTest(t *testing.T, r *Repository, key string, p CreateRelationProposalParams) domain.RelationProposal {
	t.Helper()
	v, _, err := r.CreateRelationProposal(context.Background(), memoryIdentity(key, "/v1/relation-proposals", p), p)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func confirmRelationTest(t *testing.T, r *Repository, key string, p domain.RelationProposal) domain.RelationProposal {
	t.Helper()
	review := ReviewRelationProposalParams{ReasonCode: "confirmed"}
	v, _, err := r.ConfirmRelationProposal(context.Background(), memoryIdentity(key, "/v1/relation-proposals/{proposal_id}/confirm", review), p.ProposalID, review)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestP3REL001ConfirmRelationProposalAtomic(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	p := createRelationProposalTest(t, r, "relation-proposal", relationParams(createTestMemory(t, r, "relation-source"), createTestMemory(t, r, "relation-target")))
	confirmed := confirmRelationTest(t, r, "relation-confirm", p)
	if confirmed.ResultRelationID == nil {
		t.Fatalf("confirmed=%+v", confirmed)
	}
	var assertions, evidence, states, reviews, audits, jobs int
	err := r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_relations),(SELECT count(*) FROM relation_evidence),(SELECT count(*) FROM relation_state_events),(SELECT count(*) FROM relation_proposal_reviews),(SELECT count(*) FROM audit_events WHERE action='confirm_relation_proposal'),(SELECT count(*) FROM jobs WHERE job_type='rebuild_relation_index')`).Scan(&assertions, &evidence, &states, &reviews, &audits, &jobs)
	if err != nil || assertions != 1 || evidence != 1 || states != 1 || reviews != 1 || audits != 1 || jobs != 1 {
		t.Fatalf("P3-REL-001 rows=%d/%d/%d/%d/%d/%d err=%v", assertions, evidence, states, reviews, audits, jobs, err)
	}
}

func TestP3REL002DuplicateRelationConfirmConcurrentIdempotency(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	params := relationParams(createTestMemory(t, r, "duplicate-source"), createTestMemory(t, r, "duplicate-target"))
	p := createRelationProposalTest(t, r, "duplicate-proposal", params)
	review := ReviewRelationProposalParams{ReasonCode: "confirmed"}
	identity := memoryIdentity("duplicate-confirm", "/v1/relation-proposals/{proposal_id}/confirm", review)
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan domain.RelationProposal, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			defer wg.Done()
			v, _, err := r.ConfirmRelationProposal(context.Background(), identity, p.ProposalID, review)
			results <- v
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var relation string
	for v := range results {
		if v.ResultRelationID == nil {
			t.Fatalf("result=%+v", v)
		}
		if relation == "" {
			relation = *v.ResultRelationID
		} else if relation != *v.ResultRelationID {
			t.Fatalf("relation IDs differ")
		}
	}
	var assertions, reviews, jobs int
	r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_relations),(SELECT count(*) FROM relation_proposal_reviews),(SELECT count(*) FROM jobs WHERE job_type='rebuild_relation_index')`).Scan(&assertions, &reviews, &jobs)
	if assertions != 1 || reviews != 1 || jobs != 1 {
		t.Fatalf("P3-REL-002 rows=%d/%d/%d", assertions, reviews, jobs)
	}
}

func TestP3REL003EndpointRevisionSpaceEvidenceRejectedAtomically(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	source, target := createTestMemory(t, r, "invalid-source"), createTestMemory(t, r, "invalid-target")
	params := relationParams(source, target)
	params.TargetRevisionID = "missing"
	if _, _, err := r.CreateRelationProposal(context.Background(), memoryIdentity("bad-revision", "/v1/relation-proposals", params), params); !errors.Is(err, ErrInvalidRelationProposal) {
		t.Fatalf("revision error=%v", err)
	}
	params = relationParams(source, target)
	start, end := 0, 999
	params.Evidence = []domain.RelationEvidence{{EvidenceType: "revision_range", MemoryID: &source.MemoryID, RevisionID: &source.CurrentRevision.RevisionID, StartOffset: &start, EndOffset: &end}}
	if _, _, err := r.CreateRelationProposal(context.Background(), memoryIdentity("bad-evidence", "/v1/relation-proposals", params), params); !errors.Is(err, ErrInvalidRelationProposal) {
		t.Fatalf("evidence error=%v", err)
	}
	var assertions, reviews int
	r.store.reader.QueryRow(`SELECT (SELECT count(*) FROM memory_relations),(SELECT count(*) FROM relation_proposal_reviews)`).Scan(&assertions, &reviews)
	if assertions != 0 || reviews != 0 {
		t.Fatalf("P3-REL-003 rows=%d/%d", assertions, reviews)
	}
}

func TestP3REL004OneHopTypeDirectionScopeAndCurrentRevision(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	source, target := createTestMemory(t, r, "hop-source"), createTestMemory(t, r, "hop-target")
	confirmed := confirmRelationTest(t, r, "hop-confirm", createRelationProposalTest(t, r, "hop-proposal", relationParams(source, target)))
	page, err := r.ListRelations(context.Background(), RelationFilter{SpaceID: "space-1", MemoryID: source.MemoryID, Direction: "outgoing", RelationType: "supports", State: "active", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].RelationID != *confirmed.ResultRelationID {
		t.Fatalf("outgoing=%+v err=%v", page, err)
	}
	page, err = r.ListRelations(context.Background(), RelationFilter{SpaceID: "space-1", MemoryID: source.MemoryID, Direction: "incoming", Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("incoming=%+v err=%v", page, err)
	}
	newRevision := "relation-new-revision"
	now := "2026-08-25T15:00:00Z"
	_, err = r.store.writer.Exec(`INSERT INTO memory_revisions(revision_id,memory_id,parent_revision_id,content,content_hash,created_by,processing_version,created_at) VALUES (?,?,?,'new','new-hash','test','v',?); UPDATE memories SET current_revision_id=?,updated_at=? WHERE memory_id=?`, newRevision, target.MemoryID, target.CurrentRevision.RevisionID, now, newRevision, now, target.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	page, err = r.ListRelations(context.Background(), RelationFilter{SpaceID: "space-1", MemoryID: source.MemoryID, Direction: "outgoing", Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("P3-REL-004 stale=%+v err=%v", page, err)
	}
}

func TestP3REL005RelationIndexDegradedAuthorityAndRebuild(t *testing.T) {
	r, _ := newMemoryTestRepository(t)
	source, target := createTestMemory(t, r, "indexsource"), createTestMemory(t, r, "indextarget")
	confirmed := confirmRelationTest(t, r, "index-confirm", createRelationProposalTest(t, r, "index-proposal", relationParams(source, target)))
	if _, err := r.store.writer.Exec(`UPDATE jobs SET status='succeeded',finished_at='2026-08-25T15:00:00Z' WHERE job_type='rebuild_relation_index'; UPDATE relation_index_metadata SET state='degraded',source_relation_count=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if relation, err := r.GetRelation(context.Background(), *confirmed.ResultRelationID); err != nil || relation.RelationID != *confirmed.ResultRelationID {
		t.Fatalf("authority=%+v err=%v", relation, err)
	}
	search, err := r.SearchMemories(context.Background(), SearchParams{Query: "initial", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Mode: "keyword", Limit: 10, MaxChars: 10000})
	if err != nil || !search.Degraded || !contains(search.DegradationReasons, "relation_unavailable") {
		t.Fatalf("degraded search=%+v err=%v", search, err)
	}
	ref, _, err := r.CreateRelationIndexRebuildJob(context.Background(), memoryIdentity("relation-rebuild", "/v1/indexes/relations/rebuild", struct{}{}))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := r.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "worker", LeaseDuration: time.Hour})
	if err != nil || claim.Job.JobID != ref.JobID {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if err = r.RebuildRelationIndexClaimed(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	state, _ := r.RelationIndexHealth(context.Background())
	if state != "ready" {
		t.Fatalf("P3-REL-005 state=%s", state)
	}
	search, err = r.SearchMemories(context.Background(), SearchParams{Query: "initial", SpaceIDs: []string{"space-1"}, States: []string{"active"}, Mode: "keyword", Limit: 10, MaxChars: 10000})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range search.Results {
		if result.Memory.MemoryID == target.MemoryID && contains(result.MatchedChannels, "relation") {
			found = true
		}
	}
	if !found {
		t.Fatalf("P3-REL-005 independent relation recall=%+v", search)
	}
}
