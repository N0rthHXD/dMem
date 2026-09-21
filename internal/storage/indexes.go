package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"syscall"

	"github.com/N0rthHXD/dMem/internal/domain"
)

const ftsIndexName = "memory_fts"

// Shared semantic corpus predicates. Every semantic query binds vectors and
// projections to one compatibility identity, one policy generation, and one
// dimension, so the predicates below are the single definition of "this
// projection belongs to the active vector space and has a usable vector".
//
// They are SQL fragments with fixed bind order:
//
//	eligibleSearchableCountSQL     -> policyGeneration
//	eligibleEmbeddingMismatchSQL   -> policyGeneration, compatibilityIdentity
const (
	eligiblePredicateSQL = `EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.policy_generation=? AND e.eligible=1)`
	// eligibleSearchableCountSQL counts the searchable projections that must be
	// covered by the published corpus generation.
	eligibleSearchableCountSQL = `SELECT count(*) FROM memory_projections p WHERE p.searchable=1 AND ` + eligiblePredicateSQL
	// eligibleEmbeddingMismatchSQL selects the current revision of every
	// searchable eligible projection that has no vector in the requested vector
	// space. The vector is matched on content hash as well as identity, so
	// revisions awaiting re-embedding after a provider, model, format, or
	// dimension change are selected even while their previous vector is still
	// stored under its old identity.
	eligibleEmbeddingMismatchSQL = `SELECT p.revision_id FROM memory_projections p
		WHERE p.searchable=1 AND ` + eligiblePredicateSQL + `
		AND NOT EXISTS (SELECT 1 FROM memory_embeddings em WHERE em.memory_id=p.memory_id AND em.revision_id=p.revision_id AND em.content_hash=p.content_hash AND em.compatibility_identity=?)`
)

func (r *Repository) enqueueSemanticRebuild(ctx context.Context, tx *sql.Tx, now string) (bool, error) {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE job_type='rebuild_semantic' AND target_type='index' AND target_id=? AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, semanticIndexName, r.embeddingIdentity()).Scan(&active); err != nil {
		return false, err
	}
	if active > 0 {
		return false, nil
	}
	ids, err := r.newIDs(2)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_semantic','index',?,?,'waiting',-100,?,?,?) ON CONFLICT DO NOTHING`, ids[0], ids[1], semanticIndexName, r.embeddingIdentity(), r.maxAttempts, now, now)
	if err != nil {
		return false, err
	}
	return oneRow(result), nil
}

// semanticIndexDirty reports whether the published semantic index still
// describes exactly the vector space the runtime is configured for. The
// compatibility identity already binds provider/model, document format, query
// format, and requested dimension, so any of those changes makes the published
// index dirty. It uses the same corpus predicate as the rebuild path, so a
// publishing rebuild can never observe a dirty index for the reason it just
// fixed.
func (r *Repository) semanticIndexDirty(ctx context.Context, tx *sql.Tx) (bool, error) {
	var dirty int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM semantic_index_metadata
		WHERE singleton=1 AND (
			state<>'ready' OR coalesce(compatibility_identity,'')<>?
			OR source_revision_count<>(`+eligibleSearchableCountSQL+`)
		)
	) OR EXISTS (
		`+eligibleEmbeddingMismatchSQL+`
	)`, r.emb().CompatibilityIdentity, r.emb().PolicyGeneration, r.emb().CompatibilityIdentity, r.emb().CompatibilityIdentity).Scan(&dirty)
	return dirty != 0, err
}

func (r *Repository) CreateSemanticRebuildJob(ctx context.Context, identity WriteIdentity) (domain.JobReference, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.JobReference{}, false, err
	} else if found {
		var result domain.JobReference
		if status != 202 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.JobReference{}, false, err
	}
	var active string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs WHERE job_type='rebuild_semantic' AND target_type='index' AND target_id=? AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, semanticIndexName, r.embeddingIdentity()).Scan(&active)
	if err == nil {
		return domain.JobReference{JobID: active}, false, ErrJobAlreadyActive
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.JobReference{}, false, err
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	now := formatTime(r.runtime.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, priority, max_attempts, next_run_at, created_at) VALUES (?, ?, 'rebuild_semantic', 'index', ?, ?, 'waiting', -100, ?, ?, ?)`, ids[0], ids[1], semanticIndexName, r.embeddingIdentity(), r.maxAttempts, now, now); err != nil {
		return domain.JobReference{}, false, err
	}
	result := domain.JobReference{OperationID: ids[1], JobID: ids[0]}
	body, _ := json.Marshal(result)
	if err = writeIdempotency(ctx, tx, identity, ids[1], 202, body, now); err != nil {
		return domain.JobReference{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?, ?, ?, ?, 'rebuild_semantic', 'index', ?, 'accepted', ?)`, ids[2], ids[1], identity.RequestID, identity.CallerID, semanticIndexName, now); err != nil {
		return domain.JobReference{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.JobReference{}, false, err
	}
	return result, false, nil
}

// EnsureSemanticRecoveryJobs re-establishes the invariant that the published
// semantic index describes the runtime's current vector space and that every
// eligible projection has a vector in it. It runs at startup, after every
// configuration reload, and from the maintenance loop, so it is also the path
// that turns a provider, model, document/query format, or dimension change into
// replacement work: jobs bound to another vector space are cancelled and their
// revisions are scheduled again under the new identity.
func (r *Repository) EnsureSemanticRecoveryJobs(ctx context.Context) error {
	if r.emb().Provider == nil {
		return nil
	}
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(r.runtime.Now())
	identity := r.embeddingIdentity()
	if err := r.ensureCurrentEmbeddingEligibility(ctx, tx, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status='cancelled', finished_at=?, last_error_code='provider_changed', last_error_message='semantic vector space changed' WHERE job_type IN ('embed_memory','rebuild_semantic') AND processing_version<>? AND status IN ('pending','waiting','retrying')`, now, identity); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, eligibleEmbeddingMismatchSQL+` ORDER BY p.memory_id`, r.emb().PolicyGeneration, r.emb().CompatibilityIdentity)
	if err != nil {
		return err
	}
	var revisions []string
	for rows.Next() {
		var revision string
		if err := rows.Scan(&revision); err != nil {
			rows.Close()
			return err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, revision := range revisions {
		ids, err := r.newIDs(2)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,priority,max_attempts,next_run_at,created_at) VALUES (?,?,'embed_memory','memory_revision',?,?,'pending',100,?,?,?) ON CONFLICT DO NOTHING`, ids[0], ids[1], revision, identity, r.maxAttempts, now, now); err != nil {
			return err
		}
	}
	if _, err := r.repairDivergentEmbeddings(ctx, tx, now); err != nil {
		return err
	}
	needsRebuild := len(revisions) > 0
	if !needsRebuild {
		needsRebuild, err = r.semanticIndexDirty(ctx, tx)
		if err != nil {
			return err
		}
	}
	if needsRebuild {
		if _, err := r.enqueueSemanticRebuild(ctx, tx, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// repairDivergentEmbeddings resets projections that claim a current vector but
// hold none in the active vector space. A vector can only diverge from its
// projection when the configured vector space changed while the projection
// still recorded the previous one, so this makes the divergence visible to
// semanticIndexDirty and forces a rebuild instead of publishing a generation
// that silently misses those revisions.
func (r *Repository) repairDivergentEmbeddings(ctx context.Context, tx *sql.Tx, now string) (int64, error) {
	result, err := tx.ExecContext(ctx, `UPDATE memory_projections SET embedding_state='stale', embedding_provider=NULL, embedding_model=NULL, embedding_dimension=NULL, embedding_content_hash=NULL, updated_at=? WHERE searchable=1 AND embedding_state='current' AND EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=memory_projections.memory_id AND e.revision_id=memory_projections.revision_id AND e.policy_generation=? AND e.eligible=1) AND NOT EXISTS (SELECT 1 FROM memory_embeddings em WHERE em.memory_id=memory_projections.memory_id AND em.revision_id=memory_projections.revision_id AND em.content_hash=memory_projections.content_hash AND em.compatibility_identity=?)`, now, r.emb().PolicyGeneration, r.emb().CompatibilityIdentity)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) CreateFTSRebuildJob(ctx context.Context, identity WriteIdentity) (domain.JobReference, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.JobReference{}, false, err
	} else if found {
		var result domain.JobReference
		if status != 202 || json.Unmarshal([]byte(body), &result) != nil {
			return domain.JobReference{}, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.JobReference{}, false, err
	}
	var activeID string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM jobs WHERE job_type='rebuild_fts' AND target_type='index' AND target_id=? AND processing_version=? AND status IN ('pending','waiting','running','retrying')`, ftsIndexName, r.processingVersion()).Scan(&activeID)
	if err == nil {
		return domain.JobReference{JobID: activeID}, false, ErrJobAlreadyActive
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.JobReference{}, false, err
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	jobID, operationID, auditID := ids[0], ids[1], ids[2]
	now := formatTime(r.runtime.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES (?, ?, 'rebuild_fts', 'index', ?, ?, 'pending', ?, ?, ?)`, jobID, operationID, ftsIndexName, r.processingVersion(), r.maxAttempts, now, now); err != nil {
		return domain.JobReference{}, false, err
	}
	result := domain.JobReference{OperationID: operationID, JobID: jobID}
	body, _ := json.Marshal(result)
	if err := writeIdempotency(ctx, tx, identity, operationID, 202, body, now); err != nil {
		return domain.JobReference{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?, ?, ?, ?, 'rebuild_fts', 'index', ?, 'accepted', ?)`, auditID, operationID, identity.RequestID, identity.CallerID, ftsIndexName, now); err != nil {
		return domain.JobReference{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.JobReference{}, false, err
	}
	return result, false, nil
}

func (r *Repository) RebuildFTS(ctx context.Context) error {
	return r.rebuildFTS(ctx, nil)
}

func (r *Repository) RebuildFTSClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildFTS(ctx, &claim)
}

func (r *Repository) rebuildFTS(ctx context.Context, claim *ClaimedJob) error {
	pressure, err := r.DiskPressure()
	if err != nil {
		return err
	}
	if pressure != "normal" {
		return errors.New("insufficient disk space for FTS rebuild")
	}
	now := formatTime(r.runtime.Now())
	initial, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer initial.Rollback()
	if claim != nil {
		if err := r.requireJobClaim(ctx, initial, *claim); err != nil {
			return err
		}
	}
	if _, err := initial.ExecContext(ctx, `UPDATE index_metadata SET state='rebuilding', updated_at=? WHERE index_name=?`, now, ftsIndexName); err != nil {
		return err
	}
	if err := initial.Commit(); err != nil {
		return err
	}
	type projection struct{ memoryID, revisionID, content, hints string }
	readTx, err := r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		r.markFTSDegraded()
		return err
	}
	rows, err := readTx.QueryContext(ctx, `SELECT p.memory_id, p.revision_id FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state WHERE p.searchable=1 ORDER BY p.memory_id`)
	var projections []projection
	if err == nil {
		for rows.Next() {
			var item projection
			if err = rows.Scan(&item.memoryID, &item.revisionID); err != nil {
				break
			}
			revision, revisionErr := getRevision(ctx, readTx, item.memoryID, item.revisionID)
			if revisionErr != nil {
				err = revisionErr
				break
			}
			item.content = canonicalStructuredDocument(revision)
			item.hints, err = RevisionSearchHintDocument(ctx, readTx, item.revisionID)
			if err != nil {
				break
			}
			projections = append(projections, item)
		}
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = readTx.Commit()
	} else {
		_ = readTx.Rollback()
	}
	if err != nil {
		r.markFTSDegraded()
		return err
	}

	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err == nil && claim != nil {
		err = r.requireJobClaim(ctx, tx, *claim)
	}
	if err == nil {
		var currentCount int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state WHERE p.searchable=1`).Scan(&currentCount)
		if err == nil && currentCount != len(projections) {
			err = errors.New("FTS rebuild source changed")
		}
	}
	if err == nil {
		for _, item := range projections {
			var exists int
			err = tx.QueryRowContext(ctx, `SELECT 1 FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id AND m.current_state=p.current_state WHERE p.searchable=1 AND p.memory_id=? AND p.revision_id=?`, item.memoryID, item.revisionID).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				err = errors.New("FTS rebuild source changed")
			}
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM memory_fts`)
	}
	if err == nil {
		statement, prepareErr := tx.PrepareContext(ctx, `INSERT INTO memory_fts(memory_id, revision_id, content, hints) VALUES (?, ?, ?, ?)`)
		if prepareErr != nil {
			err = prepareErr
		} else {
			for _, item := range projections {
				if _, err = statement.ExecContext(ctx, item.memoryID, item.revisionID, item.content, item.hints); err != nil {
					break
				}
			}
			if closeErr := statement.Close(); err == nil {
				err = closeErr
			}
		}
	}
	var indexed int
	if err == nil {
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_fts`).Scan(&indexed)
	}
	if err == nil && len(projections) != indexed {
		err = errors.New("FTS rebuild validation failed")
	}
	if err == nil && claim != nil {
		err = r.requireJobClaim(ctx, tx, *claim)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE index_metadata SET index_version=index_version+1, state='ready', source_revision_count=?, updated_at=? WHERE index_name=?`, len(projections), formatTime(r.runtime.Now()), ftsIndexName)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		r.markFTSDegraded()
	}
	return err
}

func (r *Repository) markFTSDegraded() {
	_, _ = r.store.writer.ExecContext(context.Background(), `UPDATE index_metadata SET state='degraded', updated_at=? WHERE index_name=?`, formatTime(r.runtime.Now()), ftsIndexName)
}

func (r *Repository) DiskPressure() (string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(r.store.path, &stat); err != nil {
		return "", err
	}
	free := stat.Bavail * uint64(stat.Bsize)
	if r.diskHardFree > 0 && free < r.diskHardFree {
		return "hard", nil
	}
	if r.diskSoftFree > 0 && free < r.diskSoftFree {
		return "soft", nil
	}
	return "normal", nil
}

func (r *Repository) diskHardLimit() (bool, error) {
	if r.diskHardFree == 0 {
		return false, nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(r.store.path, &stat); err != nil {
		return false, err
	}
	return stat.Bavail*uint64(stat.Bsize) < r.diskHardFree, nil
}

func (r *Repository) requireDiskWritable() error {
	hard, err := r.diskHardLimit()
	if err != nil {
		return err
	}
	if hard {
		return ErrResourceExhausted
	}
	return nil
}
