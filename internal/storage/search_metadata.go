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
	}
	for _, read := range reads {
		if err := tx.QueryRowContext(ctx, read.query).Scan(read.value); err != nil {
			return searchMetadata{}, err
		}
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
