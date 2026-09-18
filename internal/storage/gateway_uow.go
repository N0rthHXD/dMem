package storage

import (
	"context"
	"database/sql"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Cross-table atomic operation inventory
//
// Splitting capability ports must never split atomic writes. The following
// operations each commit several tables together and are preserved as single
// SQLite write transactions. Each is exposed through exactly one narrow gateway
// capability port method, which delegates to the repository routine named
// below. No adapter opens a transaction across two capability calls.
//
//   - raw event + extraction job + audit + idempotency:
//     Repository.IngestEvent -> extraction.EventStore.IngestEvent
//   - memory revision + evidence + state event + projection + audit +
//     idempotency: Repository.Remember -> memory.AuthorityStore.Remember
//   - memory deletion state + state event + projection + dependent aggregate
//     staleness + audit + idempotency:
//     Repository.ForgetMemory -> memory.AuthorityStore.ForgetMemory
//   - candidate review + memory/evolution authority mutation + consolidation
//     decision + candidate review + audit + idempotency:
//     Repository.ConfirmCandidate/RejectCandidate ->
//     consolidation.Store.ConfirmCandidate/RejectCandidate
//   - configuration activation + capability state + policy state:
//     Repository.CommitActivation (retained until the configuration capability
//     port is introduced; this port is not part of the initial Phase 2 surface)
//   - feedback event + aggregate job + audit + idempotency:
//     Repository.AppendFeedback -> feedback.Store.AppendFeedback
//   - authority write + idempotency result publication: every write routine
//     above writes the idempotency record inside the same transaction that
//     performs the authority mutation.
//
// UnitOfWork is the minimal SQLite transaction boundary for writes that span
// more than one capability area. It deliberately exposes only transaction-scoped
// writers for the capability areas that must commit together; it has no general
// read surface and is not a second repository. Existing single-capability
// operations keep using their repository routine because it already runs one
// transaction internally.
type UnitOfWork struct {
	store *Store
}

// UnitOfWork returns the SQLite unit of work bound to this repository.
func (r *Repository) UnitOfWork() *UnitOfWork {
	return &UnitOfWork{store: r.store}
}

// Do runs fn inside one SQLite write transaction. It commits when fn returns
// nil and rolls back on any error, so a partial cross-capability write can never
// become visible.
func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context, *TxWriters) error) error {
	tx, err := u.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	writers := &TxWriters{
		tx:          tx,
		Idempotency: IdempotencyTxWriter{tx: tx},
		Jobs:        JobTxWriter{tx: tx},
		Audit:       AuditTxWriter{tx: tx},
		Policy:      PolicyTxWriter{tx: tx},
		Authority:   AuthorityTxWriter{tx: tx},
	}
	if err := fn(ctx, writers); err != nil {
		return err
	}
	return tx.Commit()
}

// TxWriters is the transaction-scoped writer bundle exposed by a unit of work.
type TxWriters struct {
	tx          *sql.Tx
	Idempotency IdempotencyTxWriter
	Jobs        JobTxWriter
	Audit       AuditTxWriter
	Policy      PolicyTxWriter
	Authority   AuthorityTxWriter
}

// IdempotencyTxWriter publishes and reads idempotency records inside a unit of
// work so a replay result commits with the authority write it describes.
type IdempotencyTxWriter struct{ tx *sql.Tx }

// Read returns the recorded response for the gateway write identity.
func (w IdempotencyTxWriter) Read(ctx context.Context, identity gateway.WriteIdentity) (int, []byte, bool, error) {
	status, body, found, err := readIdempotency(ctx, w.tx, gatewayWriteIdentity(identity))
	return status, []byte(body), found, err
}

// Write records the response for the gateway write identity.
func (w IdempotencyTxWriter) Write(ctx context.Context, identity gateway.WriteIdentity, operationID string, status int, body []byte, now string) error {
	return writeIdempotency(ctx, w.tx, gatewayWriteIdentity(identity), operationID, status, body, now)
}

// JobSpec is the minimal durable job description a unit of work can enqueue. It
// intentionally omits job reads and transitions, which belong to the job
// worker ports.
type JobSpec struct {
	JobID             string
	OperationID       string
	JobType           string
	TargetType        string
	TargetID          string
	ProcessingVersion string
	Status            string
	Priority          int
	MaxAttempts       int
	NextRunAt         string
	CreatedAt         string
	IgnoreConflict    bool
}

// JobTxWriter enqueues derived-work jobs atomically with their authority write.
type JobTxWriter struct{ tx *sql.Tx }

// Enqueue inserts one durable job.
func (w JobTxWriter) Enqueue(ctx context.Context, job JobSpec) error {
	if job.Status == "" {
		job.Status = "pending"
	}
	if job.NextRunAt == "" {
		job.NextRunAt = job.CreatedAt
	}
	query := `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`
	if job.IgnoreConflict {
		query += ` ON CONFLICT DO NOTHING`
	}
	_, err := w.tx.ExecContext(ctx, query, job.JobID, job.OperationID, job.JobType, job.TargetType, job.TargetID, job.ProcessingVersion, job.Status, job.Priority, job.MaxAttempts, job.NextRunAt, job.CreatedAt)
	return err
}

// AuditEvent is the minimal audit record a unit of work can append.
type AuditEvent struct {
	AuditID     string
	OperationID string
	RequestID   string
	CallerID    string
	Action      string
	TargetType  string
	TargetID    string
	Outcome     string
	Reason      string
	CreatedAt   string
}

// AuditTxWriter appends audit history inside a unit of work.
type AuditTxWriter struct{ tx *sql.Tx }

// Record inserts one audit event.
func (w AuditTxWriter) Record(ctx context.Context, event AuditEvent) error {
	_, err := w.tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, event.AuditID, event.OperationID, event.RequestID, event.CallerID, event.Action, event.TargetType, event.TargetID, event.Outcome, nullableString(event.Reason), event.CreatedAt)
	return err
}

// PolicyTxWriter persists resolved policy state inside a unit of work.
type PolicyTxWriter struct{ tx *sql.Tx }

// PersistEffective records one effective policy decision.
func (w PolicyTxWriter) PersistEffective(ctx context.Context, policy EffectiveSpacePolicy, now string) error {
	_, err := w.tx.ExecContext(ctx, `INSERT INTO effective_space_policies(capability, space_id, policy_generation, decision, source_assignment_id, source_space_id, inheritance_depth, reason_code, evaluated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(capability, space_id, policy_generation) DO NOTHING`, policy.Capability, policy.SpaceID, policy.PolicyGeneration, policy.Decision, nullableString(policy.SourceAssignmentID), nullableString(policy.SourceSpaceID), policy.InheritanceDepth, policy.ReasonCode, now)
	return err
}

// AuthorityTxWriter performs the minimal authority insertion a composite
// operation needs when it is not already covered by a single-capability
// repository routine.
type AuthorityTxWriter struct{ tx *sql.Tx }

// InsertSpace inserts one scope authority row.
func (w AuthorityTxWriter) InsertSpace(ctx context.Context, space domain.Space) error {
	_, err := w.tx.ExecContext(ctx, `INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES (?, ?, ?, ?, ?, ?)`, space.SpaceID, space.ParentSpaceID, space.SpaceType, space.ExternalID, space.DisplayName, space.CreatedAt)
	return err
}
