package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/consolidation"
)

const (
	qualityAlgorithmVersion   = "memory-quality-v1"
	duplicateAlgorithmVersion = "duplicate-cluster-v1"
)

// ProjectionHealth reports the rebuildable quality and duplicate projection
// state. Projections never change eligibility; they only order candidates.
type ProjectionHealth struct {
	State                     string `json:"state"`
	QualityState              string `json:"quality_state"`
	DuplicateState            string `json:"duplicate_state"`
	QualityGeneration         int64  `json:"quality_generation"`
	DuplicateGeneration       int64  `json:"duplicate_generation"`
	QualityAlgorithmVersion   string `json:"quality_algorithm_version"`
	DuplicateAlgorithmVersion string `json:"duplicate_algorithm_version"`
}

func advanceProjectionGeneration(ctx context.Context, tx *sql.Tx, now string) error {
	result, err := tx.ExecContext(ctx, `UPDATE projection_metadata SET quality_generation=quality_generation+1, duplicate_generation=duplicate_generation+1, quality_state='stale', duplicate_state='stale', updated_at=? WHERE singleton=1`, now)
	if err != nil {
		return err
	}
	if !oneRow(result) {
		return errors.New("projection metadata singleton is missing")
	}
	return nil
}

func (r *Repository) ProjectionHealth(ctx context.Context) (ProjectionHealth, error) {
	var result ProjectionHealth
	err := r.store.reader.QueryRowContext(ctx, `SELECT quality_state, duplicate_state, quality_generation, duplicate_generation, quality_algorithm_version, duplicate_algorithm_version FROM projection_metadata WHERE singleton=1`).Scan(
		&result.QualityState, &result.DuplicateState, &result.QualityGeneration, &result.DuplicateGeneration, &result.QualityAlgorithmVersion, &result.DuplicateAlgorithmVersion)
	result.State = result.QualityState
	return result, err
}

// qualityInput is the authority snapshot used to compute one quality row.
type qualityInput struct {
	memoryID, revisionID, spaceID string
	state                         string
	confidence, specificity       float64
	precision                     string
	canonicalHash                 string
}

// RebuildQuality recomputes the rebuildable memory quality projection without
// changing eligibility. It is safe to run repeatedly and produces identical
// rows for the same authority and generation.
func (r *Repository) RebuildQuality(ctx context.Context) error {
	return r.rebuildQuality(ctx, nil)
}

// RebuildQualityClaimed fences a quality rebuild to a claimed maintenance job.
func (r *Repository) RebuildQualityClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildQuality(ctx, &claim)
}

func (r *Repository) rebuildQuality(ctx context.Context, claim *ClaimedJob) error {
	if claim != nil {
		if err := r.requireJobClaim(ctx, r.store.reader, *claim); err != nil {
			return err
		}
	}
	generation, err := r.projectionGeneration(ctx, "quality")
	if err != nil {
		return err
	}
	if err := r.setProjectionRebuilding(ctx, "quality"); err != nil {
		return err
	}
	inputs, err := r.qualityInputs(ctx)
	if err != nil {
		r.markProjectionDegraded("quality")
		return err
	}
	type row struct {
		input      qualityInput
		value      float64
		components [8]float64
	}
	rows := make([]row, 0, len(inputs))
	for _, input := range inputs {
		components, value, err := r.qualityComponents(ctx, input)
		if err != nil {
			r.markProjectionDegraded("quality")
			return err
		}
		rows = append(rows, row{input: input, value: value, components: components})
	}
	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		r.markProjectionDegraded("quality")
		return err
	}
	defer tx.Rollback()
	if err := r.requireProjectionGeneration(ctx, tx, generation); err != nil {
		return err
	}
	if claim != nil {
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_quality`); err != nil {
		return err
	}
	for _, item := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_quality(memory_id,revision_id,evidence_strength,extraction_confidence,specificity,temporal_precision,source_diversity,review_status,contradiction_penalty,duplicate_density,quality,generation,algorithm_version,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.input.memoryID, item.input.revisionID, item.components[0], item.components[1], item.components[2], item.components[3], item.components[4], item.components[5], item.components[6], item.components[7], item.value, generation, qualityAlgorithmVersion, now); err != nil {
			return err
		}
	}
	if err := r.setProjectionState(ctx, tx, "quality", "ready", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) qualityInputs(ctx context.Context) ([]qualityInput, error) {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT p.memory_id, p.revision_id, p.space_id, p.current_state, rv.confidence, rv.specificity, coalesce(rv.time_precision,''), rv.canonical_hash
		FROM memory_projections p JOIN memory_revisions rv ON rv.memory_id=p.memory_id AND rv.revision_id=p.revision_id
		ORDER BY p.memory_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs []qualityInput
	for rows.Next() {
		var item qualityInput
		if err := rows.Scan(&item.memoryID, &item.revisionID, &item.spaceID, &item.state, &item.confidence, &item.specificity, &item.precision, &item.canonicalHash); err != nil {
			return nil, err
		}
		inputs = append(inputs, item)
	}
	return inputs, rows.Err()
}

func (r *Repository) qualityComponents(ctx context.Context, input qualityInput) ([8]float64, float64, error) {
	var direct, derived, anchor, contextCount, sourceCount int
	err := r.store.reader.QueryRowContext(ctx, `SELECT
		coalesce(sum(CASE WHEN me.role='direct' THEN 1 ELSE 0 END),0),
		coalesce(sum(CASE WHEN me.role='derived' THEN 1 ELSE 0 END),0),
		coalesce(sum(CASE WHEN me.role='temporal_anchor' THEN 1 ELSE 0 END),0),
		coalesce(sum(CASE WHEN me.role='context' THEN 1 ELSE 0 END),0),
		coalesce(count(DISTINCT e.source_name),0)
		FROM memory_evidence me JOIN raw_events e ON e.event_id=me.event_id WHERE me.revision_id=?`, input.revisionID).Scan(&direct, &derived, &anchor, &contextCount, &sourceCount)
	if err != nil {
		return [8]float64{}, 0, err
	}
	var contradictions int
	if err := r.store.reader.QueryRowContext(ctx, `SELECT count(*) FROM conflict_case_participants ccp JOIN conflict_cases cc ON cc.case_id=ccp.case_id WHERE ccp.memory_id=? AND cc.state='open'`, input.memoryID).Scan(&contradictions); err != nil {
		return [8]float64{}, 0, err
	}
	var groupSize int
	if err := r.store.reader.QueryRowContext(ctx, `SELECT count(*) FROM memory_projections p2 JOIN memory_revisions rv2 ON rv2.memory_id=p2.memory_id AND rv2.revision_id=p2.revision_id WHERE p2.space_id=? AND rv2.canonical_hash=?`, input.spaceID, input.canonicalHash).Scan(&groupSize); err != nil {
		return [8]float64{}, 0, err
	}
	evidence := clamp01((float64(direct) + 0.5*float64(derived) + 0.15*float64(anchor)) / 2)
	confidence := clamp01(input.confidence)
	specificity := clamp01(input.specificity)
	temporal := float64(consolidation.TimePrecisionRank(domain.TimePrecision(input.precision))) / 5
	diversity := clamp01(float64(sourceCount) / 3)
	review := reviewStatusValue(input.state)
	contradiction := clamp01(float64(contradictions) * 0.7)
	duplicate := clamp01((float64(groupSize) - 1) / math.Max(1, float64(groupSize)))
	quality := clamp01(0.30*evidence + 0.20*confidence + 0.15*specificity + 0.10*temporal + 0.10*diversity + 0.15*review - 0.20*contradiction - 0.15*duplicate)
	return [8]float64{evidence, confidence, specificity, temporal, diversity, review, contradiction, duplicate}, quality, nil
}

func reviewStatusValue(state string) float64 {
	switch state {
	case "active":
		return 1.0
	case "contested":
		return 0.6
	case "archived":
		return 0.4
	case "expired", "superseded":
		return 0.3
	case "retracted":
		return 0.1
	case "deleted":
		return 0.0
	default:
		return 0.5
	}
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (r *Repository) projectionGeneration(ctx context.Context, kind string) (int64, error) {
	column := "quality_generation"
	if kind == "duplicate" {
		column = "duplicate_generation"
	}
	var generation int64
	if err := r.store.reader.QueryRowContext(ctx, `SELECT `+column+` FROM projection_metadata WHERE singleton=1`).Scan(&generation); err != nil {
		return 0, err
	}
	return generation, nil
}

func (r *Repository) requireProjectionGeneration(ctx context.Context, tx *sql.Tx, generation int64) error {
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT quality_generation FROM projection_metadata WHERE singleton=1`).Scan(&current); err != nil {
		return err
	}
	if current != generation {
		return ErrStaleGeneration
	}
	return nil
}

func (r *Repository) setProjectionRebuilding(ctx context.Context, kind string) error {
	_, err := r.store.writer.ExecContext(ctx, `UPDATE projection_metadata SET `+projectionStateColumn(kind)+`='rebuilding', updated_at=? WHERE singleton=1`, formatTime(r.runtime.Now()))
	return err
}

func (r *Repository) setProjectionState(ctx context.Context, tx *sql.Tx, kind, state, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE projection_metadata SET `+projectionStateColumn(kind)+`=?, updated_at=? WHERE singleton=1`, state, now)
	return err
}

func (r *Repository) markProjectionDegraded(kind string) {
	_, _ = r.store.writer.ExecContext(context.Background(), `UPDATE projection_metadata SET `+projectionStateColumn(kind)+`='degraded', updated_at=? WHERE singleton=1`, formatTime(r.runtime.Now()))
}

func projectionStateColumn(kind string) string {
	if kind == "duplicate" {
		return "duplicate_state"
	}
	return "quality_state"
}
