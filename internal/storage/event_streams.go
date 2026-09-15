package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// sourceStreamKey resolves the caller-supplied or derived stream identity for an
// event. A missing caller key falls back to source type and name so every
// ingested event belongs to a stable stream.
func sourceStreamKey(sourceType, sourceName, key string) string {
	if key != "" {
		return key
	}
	if sourceName != "" {
		return sourceType + ":" + sourceName
	}
	return sourceType
}

// sourceStreamID derives a deterministic stream identity from the owning space
// and stream key so ingestion and import recreate the same logical stream.
func sourceStreamID(spaceID, key string) string {
	hash := sha256.Sum256([]byte("source_stream\x00" + spaceID + "\x00" + key))
	return "stream-" + hex.EncodeToString(hash[:])
}

// resolveSourceStream returns the stream identity for a key, creating it once
// per space. It must run inside a write transaction.
func resolveSourceStream(ctx context.Context, tx *sql.Tx, now, spaceID, sourceType, sourceName, key string) (string, error) {
	key = sourceStreamKey(sourceType, sourceName, key)
	id := sourceStreamID(spaceID, key)
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_streams(source_stream_id,space_id,source_type,source_name,source_stream_key,created_at) VALUES (?,?,?,?,?,?) ON CONFLICT(space_id,source_stream_key) DO NOTHING`, id, spaceID, sourceType, sourceName, key, now); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT source_stream_id FROM source_streams WHERE space_id=? AND source_stream_key=?`, spaceID, key).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// nextEventSequence assigns the next stable sequence within one source stream.
// It must run inside the same transaction that inserts the event.
func nextEventSequence(ctx context.Context, tx *sql.Tx, streamID string) (int64, error) {
	var next int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(sequence),-1)+1 FROM raw_events WHERE source_stream_id=?`, streamID).Scan(&next); err != nil {
		return 0, err
	}
	return next, nil
}

// eventSequence resolves the effective sequence for an event: the caller value
// when present, otherwise a transactionally assigned value.
func eventSequence(ctx context.Context, tx *sql.Tx, streamID string, supplied *int64) (int64, error) {
	if supplied != nil {
		return *supplied, nil
	}
	return nextEventSequence(ctx, tx, streamID)
}

// orderContextEvents sorts processing context by source stream, sequence,
// timestamp, and stable ID. Equal timestamps therefore keep dialogue order
// instead of falling back to stable-ID order.
func orderContextEvents(events []domain.ProcessingEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.SourceStreamID != b.SourceStreamID {
			return a.SourceStreamID < b.SourceStreamID
		}
		if c := compareEventSequence(a.Sequence, b.Sequence); c != 0 {
			return c < 0
		}
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		return a.EventID < b.EventID
	})
}

func compareEventSequence(a, b *int64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	default:
		return 0
	}
}
