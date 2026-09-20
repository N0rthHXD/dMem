package agenttool

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

func ValidateRecall(command RecallCommand) error {
	if err := boundedText("query", command.Query, 1, MaxQueryRunes); err != nil {
		return err
	}
	if len(command.ScopeIDs) == 0 || len(command.ScopeIDs) > MaxScopeIDs {
		return invalid("scope_ids must contain between 1 and %d items", MaxScopeIDs)
	}
	if command.Intent != "" && !command.Intent.Valid() {
		return invalid("intent is unsupported")
	}
	if command.Limit < 0 || command.Limit > MaxRecallLimit {
		return invalid("limit must be between 1 and %d when supplied", MaxRecallLimit)
	}
	if len(command.EntityHints) > MaxEntityHints {
		return invalid("entity hints exceed the limit")
	}
	return validateUniqueStrings("scope_ids", command.ScopeIDs, MaxIdentifierRunes)
}

func ValidateMemoryCommit(command MemoryCommitCommand) error {
	if err := identifier("scope_id", command.ScopeID); err != nil {
		return err
	}
	if command.Source.Type != "agent_tool" {
		return invalid("source.type must be agent_tool")
	}
	for name, value := range map[string]string{
		"source.name": command.Source.Name, "source.conversation_id": command.Source.ConversationID,
		"source.turn_id": command.Source.TurnID,
	} {
		if err := boundedText(name, value, 1, MaxSourceIdentityRunes); err != nil {
			return err
		}
	}
	if command.Source.ToolCallID != "" {
		if err := boundedText("source.tool_call_id", command.Source.ToolCallID, 1, MaxSourceIdentityRunes); err != nil {
			return err
		}
	}
	if command.Source.OccurredAt.IsZero() {
		return invalid("source.occurred_at is required")
	}
	if err := validateMetadata(command.Source.Metadata, 1); err != nil {
		return err
	}
	if len(command.Memories) == 0 || len(command.Memories) > MaxMemories {
		return invalid("memories must contain between 1 and %d items", MaxMemories)
	}
	if len(command.Entities) > MaxEntities || len(command.Facts) > MaxFacts || len(command.Relations) > MaxRelations {
		return invalid("knowledge suggestions exceed their limits")
	}
	memoryIDs := make(map[string]struct{}, len(command.Memories))
	for _, memory := range command.Memories {
		if err := identifier("memory.local_id", memory.LocalID); err != nil {
			return err
		}
		if _, exists := memoryIDs[memory.LocalID]; exists {
			return invalid("duplicate memory local_id")
		}
		memoryIDs[memory.LocalID] = struct{}{}
		if !memory.Kind.Valid() || !memory.Sensitivity.Valid() || !finiteUnit(memory.Confidence) {
			return invalid("memory kind, sensitivity, or confidence is invalid")
		}
		if err := boundedText("memory.content", memory.Content, 1, MaxContentRunes); err != nil {
			return err
		}
		if len(memory.SearchHints) > MaxSearchHints {
			return invalid("search_hints exceed the limit")
		}
		for _, hint := range memory.SearchHints {
			if err := boundedText("search_hint", hint, 1, MaxSearchHintRunes); err != nil {
				return err
			}
		}
		if invalidWindow(memory.ValidFrom, memory.ValidUntil) {
			return invalid("memory valid time window is invalid")
		}
	}
	entityIDs := make(map[string]EntityType, len(command.Entities))
	for _, entity := range command.Entities {
		if err := identifier("entity.local_id", entity.LocalID); err != nil {
			return err
		}
		if _, exists := entityIDs[entity.LocalID]; exists {
			return invalid("duplicate entity local_id")
		}
		if !entity.Type.Valid() || !finiteUnit(entity.Confidence) {
			return invalid("entity type or confidence is invalid")
		}
		if err := boundedText("entity.name", entity.Name, 1, MaxSearchHintRunes); err != nil {
			return err
		}
		if len(entity.Aliases) > MaxAliases {
			return invalid("entity aliases exceed the limit")
		}
		if err := validateUniqueStrings("entity aliases", entity.Aliases, MaxSearchHintRunes); err != nil {
			return err
		}
		entityIDs[entity.LocalID] = entity.Type
	}
	factIDs := make(map[string]struct{}, len(command.Facts))
	for _, fact := range command.Facts {
		if _, exists := factIDs[fact.LocalID]; exists {
			return invalid("duplicate fact local_id")
		}
		if err := validateFact(fact, entityIDs, memoryIDs); err != nil {
			return err
		}
		factIDs[fact.LocalID] = struct{}{}
	}
	relationIDs := make(map[string]struct{}, len(command.Relations))
	for _, relation := range command.Relations {
		if _, exists := relationIDs[relation.LocalID]; exists {
			return invalid("duplicate relation local_id")
		}
		if err := validateRelation(relation, memoryIDs); err != nil {
			return err
		}
		relationIDs[relation.LocalID] = struct{}{}
	}
	return nil
}

func ValidateFeedback(command FeedbackCommand) error {
	if err := identifiers(command.ScopeID, command.MemoryID, command.RevisionID); err != nil {
		return err
	}
	if !command.Kind.Valid() {
		return invalid("feedback kind is unsupported")
	}
	return boundedText("reason", command.Reason, 0, MaxReasonRunes)
}

func ValidateForget(command ForgetCommand) error {
	if err := identifiers(command.ScopeID, command.MemoryID, command.ExpectedRevisionID); err != nil {
		return err
	}
	return boundedText("reason", command.Reason, 1, MaxReasonRunes)
}

func (s Sensitivity) Valid() bool { return s == SensitivityNormal || s == SensitivitySensitive }

func (t EntityType) Valid() bool {
	switch t {
	case EntityPerson, EntityOrganization, EntityPlace, EntityProject, EntityProduct, EntityEvent, EntityConcept, EntityOther:
		return true
	default:
		return false
	}
}

func (t RelationType) Valid() bool {
	switch t {
	case RelationSupports, RelationContradicts, RelationSupersedes, RelationDerivedFrom:
		return true
	default:
		return false
	}
}

func (k FeedbackKind) Valid() bool {
	switch k {
	case FeedbackUsed, FeedbackNotUsed, FeedbackIncorrect, FeedbackOutdated, FeedbackDuplicate:
		return true
	default:
		return false
	}
}

func validateFact(fact FactSuggestion, entityIDs map[string]EntityType, memoryIDs map[string]struct{}) error {
	if err := identifier("fact.local_id", fact.LocalID); err != nil {
		return err
	}
	definition, ok := domain.LookupPredicate(fact.Predicate)
	if !ok || !finiteUnit(fact.Confidence) {
		return invalid("fact predicate or confidence is invalid")
	}
	if err := validateEntityReference(fact.Subject, entityIDs); err != nil {
		return err
	}
	if fact.Subject.LocalID != "" && !containsString(definition.SubjectTypes, string(entityIDs[fact.Subject.LocalID])) {
		return invalid("fact subject type is incompatible with predicate")
	}
	if _, ok := memoryIDs[fact.EvidenceMemory]; !ok {
		return invalid("fact evidence_memory does not resolve")
	}
	objectCount := 0
	if fact.Object.Entity != nil {
		objectCount++
	}
	if fact.Object.StringValue != nil {
		objectCount++
	}
	if fact.Object.NumberValue != nil {
		objectCount++
	}
	if fact.Object.BooleanValue != nil {
		objectCount++
	}
	if objectCount != 1 {
		return invalid("fact object must contain exactly one value")
	}
	if definition.Literal() == (fact.Object.Entity != nil) {
		return invalid("fact object does not match predicate type")
	}
	if fact.Object.Entity != nil {
		if err := validateEntityReference(*fact.Object.Entity, entityIDs); err != nil {
			return err
		}
		if fact.Object.Entity.LocalID != "" && !containsString(definition.ObjectTypes, string(entityIDs[fact.Object.Entity.LocalID])) {
			return invalid("fact object type is incompatible with predicate")
		}
	} else {
		switch definition.LiteralType {
		case "string":
			if fact.Object.StringValue == nil {
				return invalid("fact literal does not match predicate type")
			}
		case "number":
			if fact.Object.NumberValue == nil {
				return invalid("fact literal does not match predicate type")
			}
		case "boolean":
			if fact.Object.BooleanValue == nil {
				return invalid("fact literal does not match predicate type")
			}
		}
	}
	if fact.Object.NumberValue != nil && (math.IsNaN(*fact.Object.NumberValue) || math.IsInf(*fact.Object.NumberValue, 0)) {
		return invalid("fact number must be finite")
	}
	if invalidWindow(fact.ValidFrom, fact.ValidUntil) {
		return invalid("fact valid time window is invalid")
	}
	return nil
}

func validateRelation(relation RelationSuggestion, memories map[string]struct{}) error {
	if err := identifier("relation.local_id", relation.LocalID); err != nil {
		return err
	}
	if !relation.Type.Valid() || !finiteUnit(relation.Confidence) {
		return invalid("relation type or confidence is invalid")
	}
	if err := validateMemoryReference(relation.Source, memories); err != nil {
		return err
	}
	if err := validateMemoryReference(relation.Target, memories); err != nil {
		return err
	}
	if relation.Source == relation.Target {
		return invalid("relation cannot reference itself")
	}
	if _, ok := memories[relation.EvidenceMemory]; !ok {
		return invalid("relation evidence_memory does not resolve")
	}
	return nil
}

func validateEntityReference(ref EntityReference, local map[string]EntityType) error {
	if (ref.LocalID == "") == (ref.EntityID == "") {
		return invalid("entity reference must select exactly one identity")
	}
	if ref.LocalID != "" {
		if _, ok := local[ref.LocalID]; !ok {
			return invalid("entity local reference does not resolve")
		}
		return nil
	}
	return identifier("entity_id", ref.EntityID)
}

func validateMemoryReference(ref MemoryReference, local map[string]struct{}) error {
	if ref.LocalID != "" {
		if ref.MemoryID != "" || ref.RevisionID != "" {
			return invalid("memory reference must select one identity")
		}
		if _, ok := local[ref.LocalID]; !ok {
			return invalid("memory local reference does not resolve")
		}
		return nil
	}
	if ref.MemoryID == "" || ref.RevisionID == "" {
		return invalid("persistent memory reference requires memory_id and revision_id")
	}
	return identifiers(ref.MemoryID, ref.RevisionID)
}

func validateUniqueStrings(name string, values []string, max int) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		if err := boundedText(name, value, 1, max); err != nil {
			return err
		}
		if _, ok := seen[value]; ok {
			return invalid("%s contains duplicates", name)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateMetadata(value any, depth int) error {
	if value == nil {
		return nil
	}
	if depth > MaxMetadataDepth {
		return invalid("metadata exceeds maximum depth")
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > MaxMetadataKeys {
			return invalid("metadata has too many keys")
		}
		for key, child := range typed {
			if err := boundedText("metadata key", key, 1, MaxIdentifierRunes); err != nil {
				return err
			}
			if err := validateMetadata(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > MaxMetadataKeys {
			return invalid("metadata array is too large")
		}
		for _, child := range typed {
			if err := validateMetadata(child, depth+1); err != nil {
				return err
			}
		}
	case string, bool, nil, json.Number:
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return invalid("metadata number must be finite")
		}
	default:
		return invalid("metadata contains an unsupported value")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxMetadataBytes {
		return invalid("metadata exceeds encoded size limit")
	}
	return nil
}

func invalidWindow(from, until *time.Time) bool {
	return from != nil && until != nil && !until.After(*from)
}

func finiteUnit(value float64) bool {
	return value >= 0 && value <= 1 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func identifiers(values ...string) error {
	for _, value := range values {
		if err := identifier("identifier", value); err != nil {
			return err
		}
	}
	return nil
}
func identifier(name, value string) error { return boundedText(name, value, 1, MaxIdentifierRunes) }

func boundedText(name, value string, min, max int) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return invalid("%s must be valid UTF-8 without surrounding whitespace", name)
	}
	length := utf8.RuneCountInString(value)
	if length < min || length > max {
		return invalid("%s length must be between %d and %d", name, min, max)
	}
	return nil
}

func invalid(format string, args ...any) error {
	return gateway.NewError(gateway.ErrorInvalidInput, fmt.Sprintf(format, args...))
}
