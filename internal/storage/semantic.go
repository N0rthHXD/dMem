package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/vector"
)

const semanticIndexName = "memory_semantic"

type EmbeddingIdentity struct {
	MemoryID, RevisionID, ContentHash, Provider, Model string
	Dimension, IndexVersion                            int
}

type ProjectionSnapshot struct {
	MemoryID, RevisionID, SpaceID, Content, ContentHash string
}

type SemanticStatus struct {
	State, Provider, Model                                          string
	ActiveVersion, Dimension, SourceRevisionCount, PolicyGeneration int
}

func (r *Repository) SemanticHealth(ctx context.Context) (string, int) {
	if r.emb().Provider == nil || r.emb().Index == nil {
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
	active := r.emb().Index.Status(status.ActiveVersion)
	if active.State != vector.StateReady && active.State != vector.StateStale {
		return "unavailable", status.ActiveVersion
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

func (r *Repository) semanticQueryable(status SemanticStatus) bool {
	if r.emb().Provider == nil || r.emb().Index == nil || status.ActiveVersion < 1 {
		return false
	}
	active := r.emb().Index.Status(status.ActiveVersion)
	return active.State == vector.StateReady || active.State == vector.StateStale
}

func (r *Repository) SemanticStatus(ctx context.Context) (SemanticStatus, error) {
	var result SemanticStatus
	var active, dimension sql.NullInt64
	var providerName, model sql.NullString
	err := r.store.reader.QueryRowContext(ctx, `SELECT state, active_version, provider_identity, model_identity, dimension, source_revision_count, coalesce(policy_generation,0) FROM semantic_index_metadata WHERE singleton=1`).Scan(&result.State, &active, &providerName, &model, &dimension, &result.SourceRevisionCount, &result.PolicyGeneration)
	if err != nil {
		return result, err
	}
	result.ActiveVersion, result.Dimension = int(active.Int64), int(dimension.Int64)
	result.Provider, result.Model = providerName.String, model.String
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

func (r *Repository) ProjectionSnapshot(ctx context.Context) ([]ProjectionSnapshot, error) {
	runtime := r.emb()
	rows, err := r.store.reader.QueryContext(ctx, `SELECT p.memory_id, p.revision_id, p.space_id, p.display_content, p.content_hash FROM memory_projections p WHERE p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.policy_generation=? AND e.eligible=1) ORDER BY p.memory_id`, runtime.PolicyGeneration)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProjectionSnapshot
	for rows.Next() {
		var item ProjectionSnapshot
		if err := rows.Scan(&item.MemoryID, &item.RevisionID, &item.SpaceID, &item.Content, &item.ContentHash); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func validEmbedding(values []float32) ([]float64, error) {
	if len(values) == 0 {
		return nil, errors.New("empty embedding")
	}
	result := make([]float64, len(values))
	nonzero := false
	for i, value := range values {
		result[i] = float64(value)
		if math.IsNaN(result[i]) || math.IsInf(result[i], 0) {
			return nil, errors.New("non-finite embedding")
		}
		nonzero = nonzero || value != 0
	}
	if !nonzero {
		return nil, errors.New("zero embedding")
	}
	return result, nil
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
	encoded, err := json.Marshal(values)
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
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(active_version, 1) FROM semantic_index_metadata WHERE singleton=1`).Scan(&version); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_embeddings(memory_id, revision_id, content_hash, compatibility_identity, configuration_identity, provider_identity, model_identity, dimension, vector_json, processed_at, index_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(memory_id, revision_id, content_hash, compatibility_identity) DO UPDATE SET dimension=excluded.dimension, vector_json=excluded.vector_json, processed_at=excluded.processed_at, index_version=excluded.index_version, provider_identity=excluded.provider_identity, model_identity=excluded.model_identity, configuration_identity=excluded.configuration_identity`, memoryID, revisionID, hash, runtime.CompatibilityIdentity, runtime.ConfigurationIdentity, runtime.ProviderIdentity, runtime.ModelIdentity, len(values), string(encoded), now, version)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE memory_projections SET embedding_state='current', embedding_provider=?, embedding_model=?, embedding_dimension=?, embedding_content_hash=? WHERE memory_id=? AND revision_id=? AND content_hash=?`, runtime.ProviderIdentity, runtime.ModelIdentity, len(values), hash, memoryID, revisionID, hash)
	if err != nil {
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
	if runtime.Provider == nil || runtime.Index == nil {
		return ErrIndexDegraded
	}
	snapshot, err := r.ProjectionSnapshot(ctx)
	if err != nil {
		return err
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
		for _, item := range snapshot {
			var exists int
			if err := r.store.reader.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM memory_embeddings WHERE memory_id=? AND revision_id=? AND content_hash=? AND compatibility_identity=?)`, item.MemoryID, item.RevisionID, item.ContentHash, runtime.CompatibilityIdentity).Scan(&exists); err != nil {
				r.markSemanticDegraded()
				return err
			}
			if exists != 0 && status.ActiveVersion > 0 && status.State == "ready" && runtime.PolicyGeneration != status.PolicyGeneration {
				continue
			}
			if err := r.embedMemory(ctx, nil, item.RevisionID); err != nil {
				r.markSemanticDegraded()
				return err
			}
		}
	}
	rows, err := r.store.reader.QueryContext(ctx, `SELECT p.memory_id, p.revision_id, p.space_id, p.content_hash, e.dimension, e.vector_json FROM memory_projections p JOIN memory_embeddings e ON e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.content_hash=p.content_hash WHERE p.searchable=1 AND p.embedding_state='current' AND e.compatibility_identity=? AND EXISTS (SELECT 1 FROM embedding_eligibility eligibility WHERE eligibility.memory_id=p.memory_id AND eligibility.revision_id=p.revision_id AND eligibility.policy_generation=? AND eligibility.eligible=1) ORDER BY p.memory_id`, runtime.CompatibilityIdentity, runtime.PolicyGeneration)
	if err != nil {
		if !errors.Is(err, ErrLeaseLost) {
			r.markSemanticDegraded()
		}
		return err
	}
	defer rows.Close()
	var records []vector.Record
	dimension := 0
	for rows.Next() {
		var metadata vector.Metadata
		var raw string
		if err := rows.Scan(&metadata.MemoryID, &metadata.RevisionID, &metadata.SpaceID, &metadata.ContentHash, &metadata.Dimension, &raw); err != nil {
			return err
		}
		metadata.Provider, metadata.Model, metadata.IndexVersion = runtime.ProviderIdentity, runtime.ModelIdentity, version
		var values []float64
		if json.Unmarshal([]byte(raw), &values) != nil {
			r.markSemanticDegraded()
			return errors.New("malformed stored embedding")
		}
		if dimension == 0 {
			dimension = metadata.Dimension
		}
		if metadata.Dimension != dimension || len(values) != dimension {
			r.markSemanticDegraded()
			return errors.New("embedding dimension mismatch")
		}
		records = append(records, vector.Record{Metadata: metadata, Vector: values})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(records) == 0 {
		dimension = status.Dimension
		if dimension < 1 {
			dimension = 1
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
	if err := runtime.Index.Build(version, vector.Snapshot{Provider: runtime.ProviderIdentity, Model: runtime.ModelIdentity, Dimension: dimension, Records: records}); err != nil {
		r.markSemanticDegraded()
		return err
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
	var currentCount int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections p WHERE p.searchable=1 AND EXISTS (SELECT 1 FROM embedding_eligibility e WHERE e.memory_id=p.memory_id AND e.revision_id=p.revision_id AND e.policy_generation=? AND e.eligible=1)`, runtime.PolicyGeneration).Scan(&currentCount)
	if err == nil && currentCount != len(records) {
		err = errors.New("semantic rebuild source changed")
	}
	if err == nil {
		for _, record := range records {
			var exists int
			err = tx.QueryRowContext(ctx, `SELECT 1 FROM memory_projections WHERE searchable=1 AND memory_id=? AND revision_id=? AND space_id=? AND content_hash=? AND embedding_state='current' AND embedding_provider=? AND embedding_model=? AND embedding_dimension=?`, record.Metadata.MemoryID, record.Metadata.RevisionID, record.Metadata.SpaceID, record.Metadata.ContentHash, runtime.ProviderIdentity, runtime.ModelIdentity, dimension).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				err = errors.New("semantic rebuild source changed")
			}
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		_ = tx.Rollback()
		r.markSemanticDegraded()
		return err
	}
	if err := runtime.Index.Activate(version); err != nil {
		_ = tx.Rollback()
		r.markSemanticDegraded()
		return err
	}
	if claim != nil {
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			r.restoreSemanticActivationFor(runtime, status.ActiveVersion, version)
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE semantic_index_metadata SET active_version=?, building_version=NULL, state='ready', provider_identity=?, model_identity=?, dimension=?, compatibility_identity=?, policy_generation=?, source_revision_count=?, eligible_revision_count=?, embedded_revision_count=?, excluded_revision_count=(SELECT count(*) FROM memory_projections WHERE searchable=1)-?, activated_at=?, updated_at=? WHERE singleton=1 AND building_version=?`, version, runtime.ProviderIdentity, runtime.ModelIdentity, dimension, runtime.CompatibilityIdentity, runtime.PolicyGeneration, len(records), len(records), len(records), len(records), formatTime(r.runtime.Now()), formatTime(r.runtime.Now()), version)
	if err != nil || !oneRow(result) {
		r.restoreSemanticActivationFor(runtime, status.ActiveVersion, version)
		return fmt.Errorf("activate semantic metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		r.restoreSemanticActivationFor(runtime, status.ActiveVersion, version)
		return err
	}
	return nil
}

func (r *Repository) restoreSemanticActivation(previous, rejected int) {
	r.restoreSemanticActivationFor(r.emb(), previous, rejected)
}

func (r *Repository) restoreSemanticActivationFor(runtime *embeddingRuntime, previous, rejected int) {
	if runtime == nil || runtime.Index == nil {
		return
	}
	if previous > 0 {
		_ = runtime.Index.Activate(previous)
		return
	}
	_ = runtime.Index.Deactivate(rejected)
}

func (r *Repository) markSemanticDegraded() {
	_, _ = r.store.writer.Exec(`UPDATE semantic_index_metadata SET building_version=NULL, state=CASE WHEN active_version IS NULL THEN 'degraded' ELSE 'stale' END, updated_at=? WHERE singleton=1`, formatTime(r.runtime.Now()))
}
