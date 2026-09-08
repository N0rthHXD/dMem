package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/N0rthHXD/dMem/internal/provider"
	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

const semanticIndexName = "memory_semantic"

type EmbeddingIdentity struct {
	MemoryID, RevisionID, ContentHash, Provider, Model string
	Dimension                                          int
}

type SemanticStatus struct {
	State, Provider, Model, CompatibilityIdentity                   string
	ActiveVersion, Dimension, SourceRevisionCount, PolicyGeneration int
}

func (r *Repository) SemanticHealth(ctx context.Context) (string, int) {
	if r.emb().Provider == nil {
		var required int
		if err := r.store.reader.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections WHERE searchable=1 AND embedding_state='stale'`).Scan(&required); err == nil && required > 0 {
			return "rebuild_required", 0
		}
		return "not_configured", 0
	}
	status, err := r.SemanticStatus(ctx)
	if err != nil {
		return "unavailable", 0
	}
	if status.ActiveVersion < 1 {
		if status.State == "building" {
			return "rebuilding", 0
		}
		return "unavailable", 0
	}
	if !r.semanticQueryable(status, 0) {
		return "degraded", status.ActiveVersion
	}
	switch status.State {
	case "ready":
		return "ready", status.ActiveVersion
	case "building":
		return "rebuilding", status.ActiveVersion
	default:
		return "degraded", status.ActiveVersion
	}
}

func (r *Repository) semanticQueryable(status SemanticStatus, dimension int) bool {
	runtime := r.emb()
	if runtime.Provider == nil || status.ActiveVersion < 1 {
		return false
	}
	if status.State != "ready" && status.State != "stale" && status.State != "building" {
		return false
	}
	return status.Provider == runtime.ProviderIdentity &&
		status.Model == runtime.ModelIdentity &&
		status.CompatibilityIdentity == runtime.CompatibilityIdentity &&
		status.PolicyGeneration == runtime.PolicyGeneration &&
		(dimension == 0 || status.Dimension == dimension)
}

func (r *Repository) SemanticStatus(ctx context.Context) (SemanticStatus, error) {
	var result SemanticStatus
	var active, dimension sql.NullInt64
	var providerName, model, compatibilityIdentity sql.NullString
	err := r.store.reader.QueryRowContext(ctx, `SELECT state, active_version, provider_identity, model_identity, compatibility_identity, dimension, source_revision_count, coalesce(policy_generation,0) FROM semantic_index_metadata WHERE singleton=1`).Scan(&result.State, &active, &providerName, &model, &compatibilityIdentity, &dimension, &result.SourceRevisionCount, &result.PolicyGeneration)
	if err != nil {
		return result, err
	}
	result.ActiveVersion, result.Dimension = int(active.Int64), int(dimension.Int64)
	result.Provider, result.Model, result.CompatibilityIdentity = providerName.String, model.String, compatibilityIdentity.String
	return result, nil
}

func (r *Repository) CurrentEmbeddingIdentity(ctx context.Context, memoryID string) (EmbeddingIdentity, error) {
	var item EmbeddingIdentity
	err := r.store.reader.QueryRowContext(ctx, `SELECT memory_id, revision_id, content_hash, embedding_provider, embedding_model, embedding_dimension FROM memory_projections WHERE memory_id=? AND searchable=1 AND embedding_state='current'`, memoryID).Scan(&item.MemoryID, &item.RevisionID, &item.ContentHash, &item.Provider, &item.Model, &item.Dimension)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func validEmbedding(values []float32) ([]float32, error) {
	if len(values) == 0 {
		return nil, errors.New("empty embedding")
	}
	nonzero := false
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, errors.New("non-finite embedding")
		}
		nonzero = nonzero || value != 0
	}
	if !nonzero {
		return nil, errors.New("zero embedding")
	}
	return values, nil
}

func (r *Repository) EmbedMemory(ctx context.Context, revisionID string) error {
	return r.embedMemory(ctx, nil, revisionID)
}

func (r *Repository) EmbedMemoryClaimed(ctx context.Context, claim ClaimedJob, revisionID string) error {
	return r.embedMemory(ctx, &claim, revisionID)
}

func (r *Repository) embedMemory(ctx context.Context, claim *ClaimedJob, revisionID string) error {
	runtime := r.emb()
	if runtime.Provider == nil {
		return errors.New("embedding provider is unavailable")
	}
	var memoryID, spaceID, content, hash, sensitivity string
	err := r.store.reader.QueryRowContext(ctx, `SELECT p.memory_id, p.space_id, p.display_content, p.content_hash, m.sensitivity FROM memory_projections p JOIN memories m ON m.memory_id=p.memory_id AND m.current_revision_id=p.revision_id WHERE p.revision_id=? AND p.searchable=1`, revisionID).Scan(&memoryID, &spaceID, &content, &hash, &sensitivity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.RequireEffectivePolicy(ctx, "embedding", spaceID); err != nil {
		return err
	}
	var eligible int
	if err := r.store.reader.QueryRowContext(ctx, `SELECT eligible FROM embedding_eligibility WHERE memory_id=? AND revision_id=? AND policy_generation=?`, memoryID, revisionID, runtime.PolicyGeneration).Scan(&eligible); errors.Is(err, sql.ErrNoRows) || eligible == 0 {
		return ErrEmbeddingPolicy
	} else if err != nil {
		return err
	}
	rows, err := r.store.reader.QueryContext(ctx, `SELECT DISTINCT e.source_type FROM memory_evidence me JOIN raw_events e ON e.event_id=me.event_id WHERE me.revision_id=? ORDER BY e.source_type`, revisionID)
	if err != nil {
		return err
	}
	var sourceTypes []string
	for rows.Next() {
		var sourceType string
		if err := rows.Scan(&sourceType); err != nil {
			rows.Close()
			return err
		}
		sourceTypes = append(sourceTypes, sourceType)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(sourceTypes) == 0 {
		sourceTypes = []string{"manual"}
	}
	result, err := runtime.Provider.Embed(ctx, provider.NewEmbeddingRequest(spaceID, "text/plain", sensitivity, sourceTypes, []string{content}))
	if err != nil {
		var providerErr *provider.Error
		if errors.As(err, &providerErr) && providerErr.Class() == provider.ErrorPolicy {
			return fmt.Errorf("%w: %s", ErrEmbeddingPolicy, providerErr.Code())
		}
		return err
	}
	if len(result.Vectors()) != 1 {
		return errors.New("embedding count mismatch")
	}
	values, err := validEmbedding(result.Vectors()[0])
	if err != nil {
		return err
	}
	blob, err := vec.SerializeFloat32(values)
	if err != nil {
		return err
	}
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if current := r.emb(); current.Generation != runtime.Generation || current.CompatibilityIdentity != runtime.CompatibilityIdentity {
		return ErrStaleGeneration
	}
	if claim != nil {
		if claim.Job.ConfigurationGeneration != runtime.Generation || claim.Job.PolicyGeneration != runtime.PolicyGeneration {
			return ErrStaleGeneration
		}
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	var currentRevision, currentHash string
	var searchable bool
	if err := tx.QueryRowContext(ctx, `SELECT revision_id, content_hash, searchable FROM memory_projections WHERE memory_id=?`, memoryID).Scan(&currentRevision, &currentHash, &searchable); err != nil {
		return err
	}
	if currentRevision != revisionID || currentHash != hash || !searchable {
		return nil
	}
	writeResult, err := tx.ExecContext(ctx, `INSERT INTO memory_embeddings(memory_id, revision_id, content_hash, compatibility_identity, configuration_identity, provider_identity, model_identity, dimension, vector_blob, processed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(memory_id, revision_id, content_hash, compatibility_identity) DO UPDATE SET dimension=excluded.dimension, vector_blob=excluded.vector_blob WHERE memory_embeddings.dimension<>excluded.dimension OR memory_embeddings.vector_blob<>excluded.vector_blob`, memoryID, revisionID, hash, runtime.CompatibilityIdentity, runtime.ConfigurationIdentity, runtime.ProviderIdentity, runtime.ModelIdentity, len(values), blob, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE memory_projections SET embedding_state='current', embedding_provider=?, embedding_model=?, embedding_dimension=?, embedding_content_hash=? WHERE memory_id=? AND revision_id=? AND content_hash=?`, runtime.ProviderIdentity, runtime.ModelIdentity, len(values), hash, memoryID, revisionID, hash)
	if err != nil {
		return err
	}
	if changed, err := writeResult.RowsAffected(); err != nil {
		return err
	} else if changed > 0 {
		if err := advanceSemanticCorpusGeneration(ctx, tx, now, true); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_embeddings SET configuration_identity=?, provider_identity=?, model_identity=?, processed_at=? WHERE memory_id=? AND revision_id=? AND content_hash=? AND compatibility_identity=?`, runtime.ConfigurationIdentity, runtime.ProviderIdentity, runtime.ModelIdentity, now, memoryID, revisionID, hash, runtime.CompatibilityIdentity); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RebuildSemantic(ctx context.Context) error {
	return r.rebuildSemantic(ctx, nil)
}

func (r *Repository) RebuildSemanticClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildSemantic(ctx, &claim)
}

func (r *Repository) rebuildSemantic(ctx context.Context, claim *ClaimedJob) error {
	runtime := r.emb()
	if runtime.Provider == nil {
		return ErrIndexDegraded
	}
	status, err := r.SemanticStatus(ctx)
	if err != nil {
		return err
	}
	version := status.ActiveVersion + 1
	if version < 1 {
		version = 1
	}
	now := formatTime(r.runtime.Now())
	initial, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer initial.Rollback()
	if current := r.emb(); current.Generation != runtime.Generation || current.CompatibilityIdentity != runtime.CompatibilityIdentity {
		return ErrStaleGeneration
	}
	if claim != nil {
		if claim.Job.ConfigurationGeneration != runtime.Generation || claim.Job.PolicyGeneration != runtime.PolicyGeneration {
			return ErrStaleGeneration
		}
		if err := r.requireJobClaim(ctx, initial, *claim); err != nil {
			return err
		}
	}
	if _, err := initial.ExecContext(ctx, `UPDATE semantic_index_metadata SET state='building', building_version=?, updated_at=? WHERE singleton=1`, version, now); err != nil {
		return err
	}
	if err := initial.Commit(); err != nil {
		return err
	}

	if claim == nil {
		if err := r.embedEligibleCorpus(ctx, runtime, status); err != nil {
			r.markSemanticDegraded()
			return err
		}
	}

	if claim != nil {
		if err := r.requireJobClaim(ctx, r.store.reader, *claim); err != nil {
			return err
		}
	}
	if current := r.emb(); current.Generation != runtime.Generation || current.CompatibilityIdentity != runtime.CompatibilityIdentity {
		return ErrStaleGeneration
	}

	dimension, err := r.semanticCorpusDimension(ctx, runtime, status.Dimension)
	if err != nil {
		r.markSemanticDegraded()
		return err
	}

	if r.semanticBuildHook != nil {
		r.semanticBuildHook()
	}

	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		r.markSemanticDegraded()
		return err
	}
	defer tx.Rollback()
	if current := r.emb(); current.Generation != runtime.Generation || current.CompatibilityIdentity != runtime.CompatibilityIdentity {
		return ErrStaleGeneration
	}
	if claim != nil {
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}

	sourceCount, embeddedCount, totalCount, err := r.semanticCorpusCounts(ctx, tx, runtime, dimension)
	if err != nil {
		_ = tx.Rollback()
		r.markSemanticDegraded()
		return err
	}
	if sourceCount != embeddedCount {
		_ = tx.Rollback()
		r.markSemanticDegraded()
		return errors.New("semantic rebuild source changed")
	}
	excludedCount := totalCount - sourceCount
	if excludedCount < 0 {
		excludedCount = 0
	}

	result, err := tx.ExecContext(ctx, `UPDATE semantic_index_metadata SET active_version=coalesce(active_version,0)+1, building_version=NULL, state='ready', provider_identity=?, model_identity=?, dimension=?, compatibility_identity=?, policy_generation=?, source_revision_count=?, eligible_revision_count=?, embedded_revision_count=?, excluded_revision_count=?, activated_at=?, updated_at=? WHERE singleton=1 AND building_version=?`, runtime.ProviderIdentity, runtime.ModelIdentity, dimension, runtime.CompatibilityIdentity, runtime.PolicyGeneration, sourceCount, sourceCount, embeddedCount, excludedCount, formatTime(r.runtime.Now()), formatTime(r.runtime.Now()), version)
	if err != nil || !oneRow(result) {
		_ = tx.Rollback()
		r.markSemanticDegraded()
		return fmt.Errorf("activate semantic metadata: %w", err)
	}

	if r.semanticActivateHook != nil {
		r.semanticActivateHook()
	}

	if claim != nil {
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		r.markSemanticDegraded()
		return err
	}
	return nil
}

func (r *Repository) embedEligibleCorpus(ctx context.Context, runtime *embeddingRuntime, status SemanticStatus) error {
	policyChanged := status.ActiveVersion > 0 && runtime.PolicyGeneration != status.PolicyGeneration
	var query string
	args := []any{runtime.PolicyGeneration}
	if policyChanged {
		query = `SELECT p.revision_id FROM memory_projections p WHERE p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.policy_generation=? AND e.eligible=1) AND NOT EXISTS (SELECT 1 FROM memory_embeddings m WHERE m.memory_id=p.memory_id AND m.revision_id=p.revision_id AND m.content_hash=p.content_hash AND m.compatibility_identity=?) ORDER BY p.memory_id`
		args = append(args, runtime.CompatibilityIdentity)
	} else {
		query = `SELECT p.revision_id FROM memory_projections p WHERE p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.policy_generation=? AND e.eligible=1) ORDER BY p.memory_id`
	}
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
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
		if err := r.embedMemory(ctx, nil, revision); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) semanticCorpusDimension(ctx context.Context, runtime *embeddingRuntime, fallback int) (int, error) {
	var dimension sql.NullInt64
	err := r.store.reader.QueryRowContext(ctx, `SELECT max(dimension) FROM memory_embeddings WHERE compatibility_identity=?`, runtime.CompatibilityIdentity).Scan(&dimension)
	if err != nil {
		return 0, err
	}
	if dimension.Valid && dimension.Int64 > 0 {
		return int(dimension.Int64), nil
	}
	if fallback >= 1 {
		return fallback, nil
	}
	return 1, nil
}

func (r *Repository) semanticCorpusCounts(ctx context.Context, tx *sql.Tx, runtime *embeddingRuntime, dimension int) (source, embedded, total int, err error) {
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections p WHERE p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.policy_generation=? AND e.eligible=1)`, runtime.PolicyGeneration).Scan(&source); err != nil {
		return 0, 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections p WHERE p.searchable=1 AND p.embedding_state='current' AND EXISTS (SELECT 1 FROM memory_embeddings e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.content_hash=p.content_hash AND e.compatibility_identity=? AND e.dimension=?) AND EXISTS (SELECT 1 FROM embedding_eligibility el WHERE el.memory_id=p.memory_id AND el.revision_id=p.revision_id AND el.policy_generation=? AND el.eligible=1)`, runtime.CompatibilityIdentity, dimension, runtime.PolicyGeneration).Scan(&embedded); err != nil {
		return 0, 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections WHERE searchable=1`).Scan(&total); err != nil {
		return 0, 0, 0, err
	}
	return source, embedded, total, nil
}

func (r *Repository) markSemanticDegraded() {
	_, _ = r.store.writer.Exec(`UPDATE semantic_index_metadata SET building_version=NULL, state=CASE WHEN active_version IS NULL THEN 'degraded' ELSE 'stale' END, updated_at=? WHERE singleton=1`, formatTime(r.runtime.Now()))
}

func advanceSemanticCorpusGeneration(ctx context.Context, tx *sql.Tx, now string, stale bool) error {
	state := `state`
	if stale {
		state = `CASE WHEN active_version IS NULL THEN 'absent' ELSE 'stale' END`
	}
	result, err := tx.ExecContext(ctx, `UPDATE semantic_index_metadata SET active_version=CASE WHEN active_version IS NULL THEN NULL ELSE active_version+1 END, state=`+state+`, updated_at=? WHERE singleton=1`, now)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil {
		return err
	} else if rows != 1 {
		return errors.New("semantic metadata singleton is missing")
	}
	return nil
}
