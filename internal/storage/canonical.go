package storage

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// CanonicalRevisionQueryer is the read surface needed to load a revision and
// its details when building a canonical retrieval document.
type CanonicalRevisionQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// CanonicalStructuredDocument exposes the canonical retrieval document builder
// to restore and maintenance paths that rebuild derived projections.
func CanonicalStructuredDocument(revision domain.MemoryRevision) string {
	return canonicalStructuredDocument(revision)
}

// CanonicalDocumentForRevision loads a revision and returns its canonical
// retrieval document.
func CanonicalDocumentForRevision(ctx context.Context, q CanonicalRevisionQueryer, memoryID, revisionID string) (string, error) {
	revision, err := getRevision(ctx, q, memoryID, revisionID)
	if err != nil {
		return "", err
	}
	return canonicalStructuredDocument(revision), nil
}

// canonicalDocumentFormatVersion versions the canonical retrieval document used
// for both FTS projections and embedding inputs. A change requires reindexing.
const canonicalDocumentFormatVersion = "canonical-document-v1"

// canonicalSearchHintFormatVersion versions the Agent Tool search-hint
// projection stored in the dedicated FTS hint column. Hints are Agent-generated
// retrieval aids, not canonical content: they never enter the canonical document
// used for embedding or exact-match scoring, and recall reports them as a
// separate matched field.
const canonicalSearchHintFormatVersion = "canonical-search-hints-v1"

// canonicalStructuredDocument builds one deterministic, field-aware document
// from a revision's canonical content and compact structured fields. It is the
// single source for FTS projections and embedding documents so keyword and
// semantic indexes always agree on the canonical form.
func canonicalStructuredDocument(revision domain.MemoryRevision) string {
	var builder strings.Builder
	writeDocumentField(&builder, "kind", string(revision.Kind))
	builder.WriteString(revision.CanonicalContent)
	for _, entity := range revision.SubjectEntityIDs {
		writeDocumentField(&builder, "subject", entity)
	}
	for _, entity := range revision.ObjectEntityIDs {
		writeDocumentField(&builder, "object", entity)
	}
	attributeKeys := make([]string, 0, len(revision.Attributes))
	for key := range revision.Attributes {
		attributeKeys = append(attributeKeys, key)
	}
	sort.Strings(attributeKeys)
	for _, key := range attributeKeys {
		writeDocumentField(&builder, "attribute:"+key, revision.Attributes[key])
	}
	writeDocumentField(&builder, "event_type", revision.EventType)
	if revision.EventTimeStart != nil {
		writeDocumentField(&builder, "event_time_start", formatTime(*revision.EventTimeStart))
	}
	if revision.EventTimeEnd != nil {
		writeDocumentField(&builder, "event_time_end", formatTime(*revision.EventTimeEnd))
	}
	writeDocumentField(&builder, "time_precision", string(revision.TimePrecision))
	writeDocumentField(&builder, "time_source", revision.TimeSource)
	writeDocumentField(&builder, "language", revision.Language)
	writeDocumentField(&builder, "speaker", revision.SourceSpeaker)
	return builder.String()
}

func writeDocumentField(builder *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(name)
	builder.WriteString(":")
	builder.WriteString(value)
}

// canonicalSearchHintDocument renders the ordered search hints attached to one
// revision as the FTS hint column. Hints are already normalized and deduplicated
// by the Agent Tool commit path, so the rendering is a plain deterministic join.
func canonicalSearchHintDocument(hints []string) string {
	if len(hints) == 0 {
		return ""
	}
	return strings.Join(hints, "\n")
}

// revisionSearchHints loads the Agent Tool search hints attached to a revision
// in deterministic order. Revisions written outside the Agent Tool Route have no
// hints and yield an empty projection.
func revisionSearchHints(ctx context.Context, q CanonicalRevisionQueryer, revisionID string) ([]string, error) {
	if revisionID == "" {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT hint FROM agent_tool_search_hints WHERE revision_id=? ORDER BY event_id, local_id, hint_order`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hints []string
	for rows.Next() {
		var hint string
		if err := rows.Scan(&hint); err != nil {
			return nil, err
		}
		hints = append(hints, hint)
	}
	return hints, rows.Err()
}

// RevisionSearchHintDocument loads a revision's search hints and renders the FTS
// hint projection. It is exported for restore and maintenance rebuild paths.
func RevisionSearchHintDocument(ctx context.Context, q CanonicalRevisionQueryer, revisionID string) (string, error) {
	hints, err := revisionSearchHints(ctx, q, revisionID)
	if err != nil {
		return "", err
	}
	return canonicalSearchHintDocument(hints), nil
}

// canonicalDocumentHash returns the content hash bound to a canonical document.
func canonicalDocumentHash(revision domain.MemoryRevision) string {
	return canonicalContentHash(canonicalStructuredDocument(revision))
}
