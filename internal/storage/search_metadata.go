package storage

import (
	"context"
	"database/sql"
	"errors"
)

type searchMetadata struct {
	corpusGeneration         int64
	feedbackAggregateVersion int64
	entityIndexVersion       int64
	relationIndexVersion     int64
	graphIndexVersion        int64
	entityIndexGeneration    int64
	relationIndexGeneration  int64
	graphIndexGeneration     int64
	graphIndexState          string
	entityIndexState         string
	relationIndexState       string
	entityNormalization      string
	graphSourceFactCount     int64
	graphSourceEntityCount   int64
	graphSourceRelationCount int64
	activeFactCount          int64
	activeEntityCount        int64
	activeRelationCount      int64
}

func readSearchMetadata(ctx context.Context, tx *sql.Tx) (searchMetadata, error) {
	var result searchMetadata
	reads := []struct {
		query string
		value *int64
	}{
		{`SELECT generation FROM corpus_metadata WHERE singleton=1`, &result.corpusGeneration},
		{`SELECT generation FROM feedback_aggregate_metadata WHERE singleton=1`, &result.feedbackAggregateVersion},
		{`SELECT active_version FROM entity_index_metadata WHERE singleton=1`, &result.entityIndexVersion},
		{`SELECT active_version FROM relation_index_metadata WHERE singleton=1`, &result.relationIndexVersion},
		{`SELECT active_version FROM graph_index_metadata WHERE singleton=1`, &result.graphIndexVersion},
		{`SELECT generation FROM entity_index_metadata WHERE singleton=1`, &result.entityIndexGeneration},
		{`SELECT generation FROM relation_index_metadata WHERE singleton=1`, &result.relationIndexGeneration},
		{`SELECT generation FROM graph_index_metadata WHERE singleton=1`, &result.graphIndexGeneration},
		{`SELECT source_fact_count FROM graph_index_metadata WHERE singleton=1`, &result.graphSourceFactCount},
		{`SELECT source_entity_count FROM graph_index_metadata WHERE singleton=1`, &result.graphSourceEntityCount},
		{`SELECT source_relation_count FROM graph_index_metadata WHERE singleton=1`, &result.graphSourceRelationCount},
		{`SELECT count(*) FROM entity_facts WHERE state='active'`, &result.activeFactCount},
		{`SELECT count(*) FROM entities WHERE state='active'`, &result.activeEntityCount},
		{`SELECT count(*) FROM memory_relations WHERE state='active'`, &result.activeRelationCount},
	}
	for _, read := range reads {
		if err := tx.QueryRowContext(ctx, read.query).Scan(read.value); err != nil {
			return searchMetadata{}, err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT state FROM graph_index_metadata WHERE singleton=1`).Scan(&result.graphIndexState); err != nil {
		return searchMetadata{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT state,normalization_version FROM entity_index_metadata WHERE singleton=1`).Scan(&result.entityIndexState, &result.entityNormalization); err != nil {
		return searchMetadata{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT state FROM relation_index_metadata WHERE singleton=1`).Scan(&result.relationIndexState); err != nil {
		return searchMetadata{}, err
	}
	return result, nil
}

func advanceCorpusGeneration(ctx context.Context, tx *sql.Tx, now string) error {
	result, err := tx.ExecContext(ctx, `UPDATE corpus_metadata SET generation=generation+1, updated_at=? WHERE singleton=1`, now)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("corpus metadata singleton is missing")
	}
	return nil
}
