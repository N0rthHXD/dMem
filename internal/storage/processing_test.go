package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

func claimProcessingJob(t *testing.T, repository *Repository, maxAttempts int) (ClaimedJob, domain.ProcessingInput) {
	t.Helper()
	now := formatTime(repository.runtime.Now())
	if _, err := repository.store.writer.Exec(`INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,created_at) VALUES ('process-job','process-op','extract_memory','raw_event','event-1','test-v1','pending',?,?,?)`, maxAttempts, now, now); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimJob(context.Background(), ClaimJobParams{WorkerID: "worker-1", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	input, err := repository.PrepareProcessing(context.Background(), claim, ProcessingPreparationParams{ProcessorIdentity: "processor/test", AllowedMemoryTypes: []string{"fact"}, MaxCandidates: 2, MaxContentChars: 100, MaxContextEvents: 2, MaxContextMemories: 2})
	if err != nil {
		t.Fatal(err)
	}
	return claim, input
}

func processingCandidate(content string) domain.ProcessingCandidate {
	start, end := int64(0), int64(6)
	return domain.ProcessingCandidate{Content: content, MemoryType: "fact", SpaceID: "space-1", Confidence: 0.8, Sensitivity: "normal", SuggestionKind: "new", Evidence: []domain.ProcessingEvidence{{EventID: "event-1", EvidenceType: "direct_statement", StartOffset: &start, EndOffset: &end}}}
}

func TestProcessingCommitAtomicallyStoresCandidatesEvidenceAuditAttemptAndStatus(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	claim, input := claimProcessingJob(t, repository, 3)
	result := domain.ProcessingResult{Code: "candidates_extracted", Candidates: []domain.ProcessingCandidate{processingCandidate("remember this")}, ProviderIdentity: "provider/test", ModelIdentity: "model-test", ProviderRequestID: "request-1", PolicyVersion: "policy-1", Usage: domain.ProcessingUsage{InputTokens: 2, OutputTokens: 3}}
	if err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var jobStatus, eventStatus, resultCode, attemptOutcome string
	var candidates, evidence, audits int
	err := repository.store.reader.QueryRow(`SELECT (SELECT status FROM jobs WHERE job_id='process-job'),(SELECT processing_status FROM raw_events WHERE event_id='event-1'),(SELECT result_code FROM jobs WHERE job_id='process-job'),(SELECT outcome FROM job_attempts WHERE job_id='process-job'),(SELECT count(*) FROM candidates),(SELECT count(*) FROM candidate_evidence),(SELECT count(*) FROM audit_events WHERE action='process_event')`).Scan(&jobStatus, &eventStatus, &resultCode, &attemptOutcome, &candidates, &evidence, &audits)
	if err != nil || jobStatus != "succeeded" || eventStatus != "processed" || resultCode != "candidates_extracted" || attemptOutcome != "succeeded" || candidates != 1 || evidence != 1 || audits != 1 {
		t.Fatalf("state = %s/%s/%s/%s rows=%d/%d/%d err=%v", jobStatus, eventStatus, resultCode, attemptOutcome, candidates, evidence, audits, err)
	}
}

func TestProcessingCommitStoresEvolutionProposal(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	params := CreateMemoryParams{SpaceID: "space-1", MemoryType: "fact", Content: "existing memory", Sensitivity: "normal", Evidence: directEvidence("event-1"), Reason: "context"}
	memory, _, err := repository.CreateMemory(context.Background(), memoryIdentity("evolution-context", "/v1/memories", params), params)
	if err != nil {
		t.Fatal(err)
	}
	claim, input := claimProcessingJob(t, repository, 3)
	candidate := processingCandidate("supplemented memory")
	candidate.SuggestionKind = "supplement"
	candidate.RelatedMemoryIDs = []string{memory.MemoryID}
	result := domain.ProcessingResult{Code: domain.ProcessingCandidatesExtracted, Candidates: []domain.ProcessingCandidate{candidate}}
	if err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var action, proposedContent, relationSpecs, mentionSpecs, targetMemoryID, targetRevisionID, jobStatus string
	err = repository.store.reader.QueryRow(`SELECT p.action,p.proposed_content,p.relation_specs_json,p.mention_specs_json,t.memory_id,t.expected_revision_id,(SELECT status FROM jobs WHERE job_id='process-job') FROM evolution_proposals p JOIN evolution_proposal_targets t ON t.proposal_id=p.proposal_id`).Scan(&action, &proposedContent, &relationSpecs, &mentionSpecs, &targetMemoryID, &targetRevisionID, &jobStatus)
	if err != nil || action != "supplement" || proposedContent != candidate.Content || relationSpecs != "[]" || mentionSpecs != "[]" || targetMemoryID != memory.MemoryID || targetRevisionID != memory.CurrentRevision.RevisionID || jobStatus != "succeeded" {
		t.Fatalf("proposal = %s/%s specs=%s/%s target=%s/%s status=%s err=%v", action, proposedContent, relationSpecs, mentionSpecs, targetMemoryID, targetRevisionID, jobStatus, err)
	}
}

func TestProcessingCommitPersistsSafeOmissionSummary(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	claim, input := claimProcessingJob(t, repository, 3)
	result := domain.ProcessingResult{Code: domain.ProcessingNoMemory, OmittedContextCount: 2, OmissionSummary: map[string]int{"context_limit": 1, "context_memory_policy_denied": 1}}
	if err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: result}); err != nil {
		t.Fatal(err)
	}
	var count int
	var summary string
	if err := repository.store.reader.QueryRow(`SELECT omitted_context_count, omission_summary FROM job_attempts WHERE job_id=?`, claim.Job.JobID).Scan(&count, &summary); err != nil {
		t.Fatal(err)
	}
	if count != 2 || summary != `{"context_limit":1,"context_memory_policy_denied":1}` {
		t.Fatalf("omissions = %d %s", count, summary)
	}
}

func TestPrepareProcessingIncludesBoundedCurrentMemoryContract(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	validFrom, validUntil := "2026-08-20T00:00:00Z", "2026-09-20T00:00:00Z"
	params := CreateMemoryParams{SpaceID: "space-1", MemoryType: "fact", Content: "context contract", ValidFrom: &validFrom, ValidUntil: &validUntil, Sensitivity: "sensitive", Evidence: directEvidence("event-1"), Reason: "context"}
	memory, _, err := repository.CreateMemory(context.Background(), memoryIdentity("context-memory", "/v1/memories", params), params)
	if err != nil {
		t.Fatal(err)
	}
	_, input := claimProcessingJob(t, repository, 3)
	if input.Policy.MaxContentChars != 100 || len(input.ContextMemories) != 1 {
		t.Fatalf("processing input = %+v", input)
	}
	contextMemory := input.ContextMemories[0]
	if contextMemory.MemoryID != memory.MemoryID || contextMemory.RevisionID != memory.CurrentRevision.RevisionID || contextMemory.Content != params.Content || contextMemory.CurrentState != "active" || contextMemory.Sensitivity != "sensitive" || contextMemory.ValidFrom == nil || contextMemory.ValidUntil == nil {
		t.Fatalf("context memory = %+v", contextMemory)
	}
}

func TestProcessingCommitLeaseLossAndTransactionFailureLeaveNoPartialResult(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Repository, *ClaimedJob, *domain.ProcessingResult)
	}{
		{name: "lease loss", mutate: func(_ *Repository, claim *ClaimedJob, _ *domain.ProcessingResult) { claim.LeaseGeneration++ }},
		{name: "transaction failure", mutate: func(_ *Repository, _ *ClaimedJob, result *domain.ProcessingResult) {
			bad := processingCandidate("bad")
			bad.RelatedMemoryIDs = []string{"missing"}
			result.Candidates = append(result.Candidates, bad)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, _ := newMemoryTestRepository(t)
			claim, input := claimProcessingJob(t, repository, 3)
			result := domain.ProcessingResult{Code: "candidates_extracted", Candidates: []domain.ProcessingCandidate{processingCandidate("first")}}
			test.mutate(repository, &claim, &result)
			err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: result})
			if err == nil {
				t.Fatal("commit succeeded")
			}
			var candidates, evidence int
			var jobStatus, eventStatus string
			if queryErr := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM candidates),(SELECT count(*) FROM candidate_evidence),(SELECT status FROM jobs WHERE job_id='process-job'),(SELECT processing_status FROM raw_events WHERE event_id='event-1')`).Scan(&candidates, &evidence, &jobStatus, &eventStatus); queryErr != nil {
				t.Fatal(queryErr)
			}
			if candidates != 0 || evidence != 0 || jobStatus != "running" || eventStatus != "processing" {
				t.Fatalf("partial state = %d/%d %s/%s", candidates, evidence, jobStatus, eventStatus)
			}
		})
	}
}

func TestProcessingCommitRejectsStaleConfigurationGeneration(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	repository.SetProcessingGeneration(1, 0, "processing-v1")
	claim, input := claimProcessingJob(t, repository, 3)
	repository.SetProcessingGeneration(2, 0, "processing-v2")

	err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: domain.ProcessingResult{Code: domain.ProcessingNoMemory}})
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("CommitProcessing() error = %v, want ErrStaleGeneration", err)
	}
	var status string
	if err := repository.store.reader.QueryRow(`SELECT status FROM jobs WHERE job_id='process-job'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("stale generation changed job state to %q", status)
	}
}

func TestProcessingCommitAndFailRejectExpiredLeaseBeforeRecovery(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Repository, ClaimedJob, domain.ProcessingInput) error
	}{
		{name: "commit", run: func(repository *Repository, claim ClaimedJob, input domain.ProcessingInput) error {
			return repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: domain.ProcessingResult{Code: domain.ProcessingNoMemory}})
		}},
		{name: "fail", run: func(repository *Repository, claim ClaimedJob, input domain.ProcessingInput) error {
			return repository.FailProcessing(context.Background(), claim, input, JobFailure{Code: "failed", Message: "safe failure", Transient: false})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, clock := newMemoryTestRepository(t)
			claim, input := claimProcessingJob(t, repository, 3)
			clock.now = clock.now.Add(time.Minute)

			if err := test.run(repository, claim, input); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("expired %s error = %v", test.name, err)
			}
			var candidates, audits int
			var jobStatus, eventStatus string
			if err := repository.store.reader.QueryRow(`SELECT (SELECT count(*) FROM candidates),(SELECT count(*) FROM audit_events WHERE action='process_event'),(SELECT status FROM jobs WHERE job_id='process-job'),(SELECT processing_status FROM raw_events WHERE event_id='event-1')`).Scan(&candidates, &audits, &jobStatus, &eventStatus); err != nil {
				t.Fatal(err)
			}
			if candidates != 0 || audits != 0 || jobStatus != "running" || eventStatus != "processing" {
				t.Fatalf("expired mutation = candidates=%d audits=%d status=%s/%s", candidates, audits, jobStatus, eventStatus)
			}
			if recovered, err := repository.RecoverExpiredLeases(context.Background()); err != nil || recovered != 1 {
				t.Fatalf("RecoverExpiredLeases() = %d, %v", recovered, err)
			}
		})
	}
}

func TestProcessingOutcomeStatusesAndTransientExhaustion(t *testing.T) {
	for _, test := range []struct{ code, eventStatus string }{{"no_processor", "stored"}, {"no_memory", "processed"}} {
		t.Run(test.code, func(t *testing.T) {
			repository, _ := newMemoryTestRepository(t)
			claim, input := claimProcessingJob(t, repository, 3)
			if err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: domain.ProcessingResult{Code: domain.ProcessingOutcome(test.code), ReasonCode: "reason"}}); err != nil {
				t.Fatal(err)
			}
			var jobStatus, eventStatus string
			if err := repository.store.reader.QueryRow(`SELECT (SELECT status FROM jobs WHERE job_id='process-job'),(SELECT processing_status FROM raw_events WHERE event_id='event-1')`).Scan(&jobStatus, &eventStatus); err != nil || jobStatus != "succeeded" || eventStatus != test.eventStatus {
				t.Fatalf("status = %s/%s err=%v", jobStatus, eventStatus, err)
			}
		})
	}
	repository, _ := newMemoryTestRepository(t)
	claim, input := claimProcessingJob(t, repository, 1)
	if err := repository.FailProcessing(context.Background(), claim, input, JobFailure{Code: "transient_failure", Message: "busy", Transient: true, Backoff: time.Second}); err != nil {
		t.Fatal(err)
	}
	var jobStatus, eventStatus string
	if err := repository.store.reader.QueryRow(`SELECT (SELECT status FROM jobs WHERE job_id='process-job'),(SELECT processing_status FROM raw_events WHERE event_id='event-1')`).Scan(&jobStatus, &eventStatus); err != nil || jobStatus != "failed" || eventStatus != "failed" {
		t.Fatalf("terminal transient = %s/%s err=%v", jobStatus, eventStatus, err)
	}
	if err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: domain.ProcessingResult{Code: "no_memory"}}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("late commit error = %v", err)
	}
}

func TestProcessingReplayUsesExistingResultForSameVersionAndCreatesForNewVersion(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	claim, input := claimProcessingJob(t, repository, 3)
	if err := repository.CommitProcessing(context.Background(), claim, input, ProcessingCommit{Result: domain.ProcessingResult{Code: domain.ProcessingNoMemory}}); err != nil {
		t.Fatal(err)
	}
	identity := WriteIdentity{CallerID: "caller", IdempotencyKey: "same-version", RequestID: "request", Method: "POST", Route: "/v1/events/{event_id}/replay", RequestHash: HashRequest("event-1")}
	replay, replayed, err := repository.ReplayEvent(context.Background(), identity, "event-1")
	if err != nil || !replayed || replay.JobID != claim.Job.JobID {
		t.Fatalf("same-version replay = %+v replayed=%v err=%v", replay, replayed, err)
	}
	var jobs int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE target_id='event-1'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("same-version jobs = %d err=%v", jobs, err)
	}

	repository = NewRepository(repository.store, repository.runtime, repository.queueSoftLimit, repository.queueHardLimit, repository.maxAttempts, "test-v2")
	identity.IdempotencyKey = "new-version"
	identity.RequestHash = HashRequest("event-1-v2")
	created, replayed, err := repository.ReplayEvent(context.Background(), identity, "event-1")
	if err != nil || replayed || created.JobID == claim.Job.JobID {
		t.Fatalf("new-version replay = %+v replayed=%v err=%v", created, replayed, err)
	}
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM jobs WHERE target_id='event-1'`).Scan(&jobs); err != nil || jobs != 2 {
		t.Fatalf("new-version jobs = %d err=%v", jobs, err)
	}
}
