package maintenance

import (
	"context"
	"testing"
	"time"
)

// TestRecoveryDrillsFenceInterruptedWork proves restore fences an interrupted
// worker for every generation-fenced stage: consolidation, quality and
// duplicate projection rebuilds, graph extraction, semantic rebuild, and
// feedback aggregation. A fenced job is retried under the current generation
// instead of publishing a stale result.
func TestRecoveryDrillsFenceInterruptedWork(t *testing.T) {
	cases := []struct {
		name, jobType, targetType, targetID, version, expectStatus string
	}{
		{"consolidation", "extract_memory", "raw_event", "recovery-event", "recovery-v1", "retrying"},
		{"quality_rebuild", "rebuild_quality", "index", "memory_quality", "recovery-v1", "retrying"},
		{"duplicate_rebuild", "rebuild_duplicates", "index", "duplicate_clusters", "recovery-v1", "retrying"},
		{"graph_extraction", "extract_graph", "memory_revision", "recovery-revision-a", "recovery-v1", "retrying"},
		{"semantic_rebuild", "rebuild_semantic", "index", "memory_semantic", "provider/model", "cancelled"},
		{"feedback_aggregation", "aggregate_feedback", "feedback_revision", "recovery-revision-a", "v2", "retrying"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db, _ := recoveryTestDatabase(t)
			jobID := "drill-" + testCase.name
			attemptID := "drill-attempt-" + testCase.name
			if _, err := db.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,attempt_count,max_attempts,next_run_at,lease_owner,lease_until,lease_generation,created_at) VALUES (?,?,?,?,?,?, 'running',1,3,'2026-08-25T10:00:00Z','worker','2099-01-01T00:00:00Z',4,'2026-08-25T10:00:00Z')`,
				jobID, "drill-op-"+testCase.name, testCase.jobType, testCase.targetType, testCase.targetID, testCase.version); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO job_attempts(attempt_id,job_id,lease_generation,worker_id,started_at) VALUES (?,?,4,'worker','2026-08-25T10:00:00Z')`, attemptID, jobID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := validateAndPrepare(context.Background(), db, time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			var status, outcome string
			var generation int
			if err := db.QueryRow(`SELECT status,lease_generation FROM jobs WHERE job_id=?`, jobID).Scan(&status, &generation); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT outcome FROM job_attempts WHERE attempt_id=?`, attemptID).Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			if status != testCase.expectStatus || generation != 5 || outcome != "lease_lost" {
				t.Fatalf("interrupted %s not fenced: status=%s (want %s) generation=%d outcome=%s", testCase.name, status, testCase.expectStatus, generation, outcome)
			}
		})
	}
}

// TestRecoverySchedulesProjectionAndGraphRebuilds proves restore resets the
// rebuildable projection and graph metadata and enqueues generation-fenced
// rebuild jobs so interrupted derived state can never be read as current.
func TestRecoverySchedulesProjectionAndGraphRebuilds(t *testing.T) {
	db, _ := recoveryTestDatabase(t)
	if _, err := db.Exec(`UPDATE projection_metadata SET quality_state='rebuilding', duplicate_state='rebuilding' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateAndPrepare(context.Background(), db, time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var qualityState, duplicateState, graphState string
	if err := db.QueryRow(`SELECT quality_state, duplicate_state FROM projection_metadata WHERE singleton=1`).Scan(&qualityState, &duplicateState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM graph_index_metadata WHERE singleton=1`).Scan(&graphState); err != nil {
		t.Fatal(err)
	}
	if qualityState != "rebuilding" || duplicateState != "rebuilding" || graphState != "rebuilding" {
		t.Fatalf("restore did not mark derived state rebuilding: quality=%s duplicate=%s graph=%s", qualityState, duplicateState, graphState)
	}
	for _, jobType := range []string{"rebuild_quality", "rebuild_duplicates", "rebuild_graph_index"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE job_type=? AND status='pending'`, jobType).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count < 1 {
			t.Fatalf("restore did not enqueue %s", jobType)
		}
	}
}
