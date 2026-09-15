package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// entityGraphStatisticsVersion identifies the projection format. A change to the
// formula requires a graph index rebuild at a new generation.
const entityGraphStatisticsVersion = "entity-graph-statistics-v2"

// entityHubHalfLife is the degree/mention scale at which the inverse-frequency
// penalty falls to one half. Low-degree entities stay near neutral so a direct
// fact is not punished for its subject, while generic hubs decay sharply.
const entityHubHalfLife = 5.0

// rebuildEntityGraphStatistics rebuilds the per-entity mention-frequency and
// degree projection inside the graph index generation. High degree reduces the
// inverse-degree weight so generic hubs cannot dominate traversal or scoring.
func (r *Repository) rebuildEntityGraphStatistics(ctx context.Context, tx *sql.Tx, now string) error {
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM graph_index_metadata WHERE singleton=1`).Scan(&generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entity_graph_statistics`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entity_graph_statistics(entity_id, space_id, mention_count, degree, inverse_mention_frequency, inverse_degree, generation, updated_at)
		SELECT e.entity_id, e.space_id, coalesce(m.mention_count, 0), coalesce(d.degree, 0),
			1.0 / (1.0 + coalesce(m.mention_count, 0) / ?), 1.0 / (1.0 + coalesce(d.degree, 0) / ?), ?, ?
		FROM entities e
		LEFT JOIN (SELECT entity_id, count(*) AS mention_count FROM entity_mentions GROUP BY entity_id) m ON m.entity_id=e.entity_id
		LEFT JOIN (
			SELECT entity_id, count(*) AS degree FROM (
				SELECT subject_entity_id AS entity_id FROM entity_facts WHERE state='active'
				UNION ALL
				SELECT object_entity_id AS entity_id FROM entity_facts WHERE state='active' AND object_entity_id IS NOT NULL
			) GROUP BY entity_id
		) d ON d.entity_id=e.entity_id
		WHERE e.state='active'`, entityHubHalfLife, entityHubHalfLife, generation, now); err != nil {
		return err
	}
	return nil
}

// EntityGraphStatistic returns the current projection for one entity. A missing
// row is reported with ErrNotFound so callers can fall back to live counts.
func (r *Repository) EntityGraphStatistic(ctx context.Context, entityID string) (domain.EntityGraphStatistic, error) {
	var statistic domain.EntityGraphStatistic
	err := r.store.reader.QueryRowContext(ctx, `SELECT entity_id, space_id, mention_count, degree, inverse_mention_frequency, inverse_degree, generation, updated_at FROM entity_graph_statistics WHERE entity_id=?`, entityID).Scan(&statistic.EntityID, &statistic.SpaceID, &statistic.MentionCount, &statistic.Degree, &statistic.InverseMentionFrequency, &statistic.InverseDegree, &statistic.Generation, &statistic.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return statistic, ErrNotFound
	}
	return statistic, err
}
