package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// PredicateRegistryVersion is the registry identity persisted alongside the
// seeded predicate definitions. A registry change requires a fresh database.
const PredicateRegistryVersion = domain.PredicateRegistryVersion

// seedPredicateRegistry persists the closed typed registry so the database
// carries the same definitions used by extraction, publication, and traversal.
// It is idempotent: definitions are inserted in two phases to satisfy the
// self-referencing inverse predicate foreign key.
func seedPredicateRegistry(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	for _, definition := range domain.PredicateDefinitions() {
		if _, err := tx.ExecContext(ctx, `INSERT INTO predicates(predicate, description, created_at) VALUES (?,?,?) ON CONFLICT(predicate) DO UPDATE SET description=excluded.description`, definition.Name, definition.Description, now); err != nil {
			return fmt.Errorf("seed predicate %s: %w", definition.Name, err)
		}
		subjects, err := json.Marshal(definition.SubjectTypes)
		if err != nil {
			return err
		}
		objects := definition.ObjectTypes
		if objects == nil {
			objects = []string{}
		}
		objectJSON, err := json.Marshal(objects)
		if err != nil {
			return err
		}
		positions, err := json.Marshal(definition.PathPositions)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO predicate_definitions(predicate, version, subject_types_json, object_entity_types_json, object_literal_type, direction, inverse_predicate, cardinality, transitivity, temporal_policy, allowed_path_positions_json, auto_confirm, created_at) VALUES (?,?,?,?,?,?,NULL,?,?,?,?,?,?) ON CONFLICT(predicate) DO UPDATE SET version=excluded.version, subject_types_json=excluded.subject_types_json, object_entity_types_json=excluded.object_entity_types_json, object_literal_type=excluded.object_literal_type, direction=excluded.direction, cardinality=excluded.cardinality, transitivity=excluded.transitivity, temporal_policy=excluded.temporal_policy, allowed_path_positions_json=excluded.allowed_path_positions_json, auto_confirm=excluded.auto_confirm`, definition.Name, PredicateRegistryVersion, string(subjects), string(objectJSON), nullableLiteral(definition.LiteralType), string(definition.Direction), string(definition.Cardinality), string(definition.Transitivity), string(definition.TemporalPolicy), string(positions), string(definition.AutoConfirm), now); err != nil {
			return fmt.Errorf("seed predicate definition %s: %w", definition.Name, err)
		}
	}
	for _, definition := range domain.PredicateDefinitions() {
		if definition.Inverse == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE predicate_definitions SET inverse_predicate=? WHERE predicate=?`, definition.Inverse, definition.Name); err != nil {
			return fmt.Errorf("seed predicate inverse %s: %w", definition.Name, err)
		}
	}
	return tx.Commit()
}

func nullableLiteral(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// predicateDefinition loads the persisted closed definition for a predicate.
func (r *Repository) predicateDefinition(ctx context.Context, q candidateQueryer, predicate string) (domain.PredicateDefinition, error) {
	definition, ok := domain.LookupPredicate(predicate)
	if !ok {
		return domain.PredicateDefinition{}, ErrInvalidFactProposal
	}
	var version, direction, cardinality, transitivity, temporal, subjectsJSON, objectsJSON, positionsJSON, autoConfirm string
	var literal, inverse sql.NullString
	err := q.QueryRowContext(ctx, `SELECT version, subject_types_json, object_entity_types_json, object_literal_type, direction, inverse_predicate, cardinality, transitivity, temporal_policy, allowed_path_positions_json, auto_confirm FROM predicate_definitions WHERE predicate=?`, predicate).Scan(&version, &subjectsJSON, &objectsJSON, &literal, &direction, &inverse, &cardinality, &transitivity, &temporal, &positionsJSON, &autoConfirm)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PredicateDefinition{}, ErrInvalidFactProposal
	}
	if err != nil {
		return domain.PredicateDefinition{}, err
	}
	var subjects, objects []string
	var positions []domain.PredicatePathPosition
	if err := json.Unmarshal([]byte(subjectsJSON), &subjects); err != nil {
		return domain.PredicateDefinition{}, err
	}
	if err := json.Unmarshal([]byte(objectsJSON), &objects); err != nil {
		return domain.PredicateDefinition{}, err
	}
	if err := json.Unmarshal([]byte(positionsJSON), &positions); err != nil {
		return domain.PredicateDefinition{}, err
	}
	definition.SubjectTypes = subjects
	definition.ObjectTypes = objects
	definition.LiteralType = literal.String
	definition.Direction = domain.PredicateDirection(direction)
	definition.Inverse = inverse.String
	definition.Cardinality = domain.PredicateCardinality(cardinality)
	definition.Transitivity = domain.PredicateTransitivity(transitivity)
	definition.TemporalPolicy = domain.PredicateTemporalPolicy(temporal)
	definition.PathPositions = positions
	definition.AutoConfirm = domain.PredicateAutoConfirm(autoConfirm)
	return definition, nil
}

// validPredicateTypePair enforces subject and object type compatibility for a
// closed predicate. An empty object type list means the predicate takes a literal.
func validPredicateTypePair(definition domain.PredicateDefinition, subjectType, objectType string) bool {
	if !validEntityType(subjectType) || !contains(definition.SubjectTypes, subjectType) {
		return false
	}
	if definition.Literal() {
		return objectType == ""
	}
	if objectType == "" || !contains(definition.ObjectTypes, objectType) {
		return false
	}
	return true
}

// normalizePredicateName trims and rejects free-form provider predicates.
func normalizePredicateName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name {
		return "", false
	}
	if _, ok := domain.LookupPredicate(trimmed); !ok {
		return "", false
	}
	return trimmed, true
}
