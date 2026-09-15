package provider

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// ADR 0035 replacement provider contracts. These types define the structured
// memory candidate and typed graph output that supersede the text-first
// Candidate and broad-relation graph contracts. They are validated here so the
// job layer only ever sees deterministic, bounded proposals.

const (
	StructuredProcessingContractVersion = "processing-structured-v1"
	TypedGraphContractVersion           = "graph-typed-v1"
	PredicateRegistryVersion            = "predicate-registry-v1"
)

const (
	defaultStructuredMaxCandidates = 32
	defaultStructuredMaxChars      = 4096
	defaultStructuredMaxEvidence   = 100
	defaultStructuredMaxEntityRefs = 64
	defaultGraphMaxItems           = 256
)

const maxContractIdentityChars = 512

var (
	errInvalidStructuredOutput = errors.New("invalid structured provider output")
	errInvalidGraphOutput      = errors.New("invalid typed graph provider output")
	errUnknownPredicate        = errors.New("unknown graph predicate")
	errPredicateTypeMismatch   = errors.New("graph predicate type mismatch")
	errUnknownEventType        = errors.New("unknown graph event type")
	errUnknownEntityType       = errors.New("unknown graph entity type")
)

// TimeProposal is a provider's proposed time interval together with the source
// wording and normalized precision. Start and end are inclusive. A nil bound
// means open-ended.
type TimeProposal struct {
	Expression string
	Start      *time.Time
	End        *time.Time
	Precision  domain.TimePrecision
	Source     string
}

// Normalize trims free text and fills unknown precision. It never invents a
// bound; it only normalizes what the provider proposed.
func (t TimeProposal) Normalize() (TimeProposal, error) {
	normalized := t
	normalized.Expression = strings.TrimSpace(t.Expression)
	normalized.Source = strings.TrimSpace(t.Source)
	if normalized.Precision == "" {
		normalized.Precision = domain.TimePrecisionUnknown
	}
	if !normalized.Precision.Valid() {
		return TimeProposal{}, fmt.Errorf("%w: bad precision %q", errInvalidStructuredOutput, normalized.Precision)
	}
	if normalized.Start != nil && normalized.End != nil && normalized.End.Before(*normalized.Start) {
		return TimeProposal{}, fmt.Errorf("%w: end before start", errInvalidStructuredOutput)
	}
	if !boundedOptionalContractString(normalized.Expression) || !boundedOptionalContractString(normalized.Source) {
		return TimeProposal{}, fmt.Errorf("%w: time wording out of bounds", errInvalidStructuredOutput)
	}
	return normalized, nil
}

// StructuredCandidateBounds bounds one structured processing response.
type StructuredCandidateBounds struct {
	MaxCandidates           int
	MaxContentChars         int
	MaxTotalChars           int
	MaxEvidencePerCandidate int
	MaxEntityRefs           int
	MaxAttributes           int
	AllowedKinds            []domain.MemoryKind
}

func (b StructuredCandidateBounds) withDefaults() StructuredCandidateBounds {
	if b.MaxCandidates <= 0 {
		b.MaxCandidates = defaultStructuredMaxCandidates
	}
	if b.MaxContentChars <= 0 {
		b.MaxContentChars = defaultStructuredMaxChars
	}
	if b.MaxTotalChars <= 0 {
		b.MaxTotalChars = b.MaxContentChars * b.MaxCandidates
	}
	if b.MaxEvidencePerCandidate <= 0 {
		b.MaxEvidencePerCandidate = defaultStructuredMaxEvidence
	}
	if b.MaxEntityRefs <= 0 {
		b.MaxEntityRefs = defaultStructuredMaxEntityRefs
	}
	if b.MaxAttributes <= 0 {
		b.MaxAttributes = 32
	}
	if len(b.AllowedKinds) == 0 {
		b.AllowedKinds = []domain.MemoryKind{
			domain.MemoryKindFact, domain.MemoryKindEvent, domain.MemoryKindPreference,
			domain.MemoryKindPlan, domain.MemoryKindProcedure,
		}
	}
	return b
}

// DecodeStructuredCandidates strictly decodes a structured processing response
// and returns deterministic, validated revisions. Unknown JSON fields, extra
// JSON values, unsupported kinds, non-finite scores, inverted windows, and
// context-only evidence are rejected.
func DecodeStructuredCandidates(data []byte, bounds StructuredCandidateBounds) ([]domain.MemoryRevision, error) {
	bounds = bounds.withDefaults()
	var output structuredProcessOutputWire
	if err := decodeSingleStrict(data, &output); err != nil {
		return nil, fmt.Errorf("%w: malformed output", errInvalidStructuredOutput)
	}
	switch output.Category {
	case "no_memory":
		if len(output.Candidates) != 0 {
			return nil, fmt.Errorf("%w: unexpected candidates", errInvalidStructuredOutput)
		}
		return nil, nil
	case "candidates_extracted":
	default:
		return nil, fmt.Errorf("%w: invalid category", errInvalidStructuredOutput)
	}
	if len(output.Candidates) == 0 || len(output.Candidates) > bounds.MaxCandidates {
		return nil, fmt.Errorf("%w: candidate count out of bounds", errInvalidStructuredOutput)
	}
	allowed := make(map[domain.MemoryKind]struct{}, len(bounds.AllowedKinds))
	for _, kind := range bounds.AllowedKinds {
		allowed[kind] = struct{}{}
	}
	revisions := make([]domain.MemoryRevision, 0, len(output.Candidates))
	totalChars := 0
	for _, candidate := range output.Candidates {
		revision, err := normalizeStructuredCandidate(candidate, bounds, allowed, &totalChars)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	return revisions, nil
}

func normalizeStructuredCandidate(wire structuredCandidateWire, bounds StructuredCandidateBounds, allowed map[domain.MemoryKind]struct{}, totalChars *int) (domain.MemoryRevision, error) {
	kind := domain.MemoryKind(strings.TrimSpace(wire.Kind))
	if _, ok := allowed[kind]; !ok {
		return domain.MemoryRevision{}, fmt.Errorf("%w: unsupported kind %q", errInvalidStructuredOutput, wire.Kind)
	}
	display := strings.TrimSpace(wire.DisplayContent)
	canonical := strings.TrimSpace(wire.CanonicalContent)
	if display == "" || canonical == "" {
		return domain.MemoryRevision{}, fmt.Errorf("%w: empty content", errInvalidStructuredOutput)
	}
	if utf8.RuneCountInString(display) > bounds.MaxContentChars || utf8.RuneCountInString(canonical) > bounds.MaxContentChars {
		return domain.MemoryRevision{}, fmt.Errorf("%w: candidate content out of bounds", errInvalidStructuredOutput)
	}
	chars := utf8.RuneCountInString(canonical)
	*totalChars += chars
	if *totalChars > bounds.MaxTotalChars {
		return domain.MemoryRevision{}, fmt.Errorf("%w: total content out of bounds", errInvalidStructuredOutput)
	}
	if !finiteUnit(wire.Confidence) || !finiteUnit(wire.Specificity) {
		return domain.MemoryRevision{}, fmt.Errorf("%w: invalid score", errInvalidStructuredOutput)
	}
	if wire.Sensitivity != "normal" && wire.Sensitivity != "sensitive" {
		return domain.MemoryRevision{}, fmt.Errorf("%w: invalid sensitivity", errInvalidStructuredOutput)
	}
	subjects, err := normalizeEntityRefs(wire.SubjectEntityIDs, bounds.MaxEntityRefs)
	if err != nil {
		return domain.MemoryRevision{}, err
	}
	objects, err := normalizeEntityRefs(wire.ObjectEntityIDs, bounds.MaxEntityRefs)
	if err != nil {
		return domain.MemoryRevision{}, err
	}
	attributes, err := normalizeAttributes(wire.Attributes, bounds.MaxAttributes)
	if err != nil {
		return domain.MemoryRevision{}, err
	}
	eventTime, err := normalizeOptionalTime(wire.EventTime)
	if err != nil {
		return domain.MemoryRevision{}, err
	}
	validTime, err := normalizeOptionalTime(wire.ValidTime)
	if err != nil {
		return domain.MemoryRevision{}, err
	}
	if len(wire.Evidence) == 0 || len(wire.Evidence) > bounds.MaxEvidencePerCandidate {
		return domain.MemoryRevision{}, fmt.Errorf("%w: evidence count out of bounds", errInvalidStructuredOutput)
	}
	evidence := make([]domain.RevisionEvidence, 0, len(wire.Evidence))
	for _, item := range wire.Evidence {
		entry, err := normalizeStructuredEvidence(item)
		if err != nil {
			return domain.MemoryRevision{}, err
		}
		evidence = append(evidence, entry)
	}
	revision := domain.MemoryRevision{
		Kind:             kind,
		DisplayContent:   display,
		CanonicalContent: canonical,
		SubjectEntityIDs: subjects,
		ObjectEntityIDs:  objects,
		EventType:        strings.TrimSpace(wire.EventType),
		Attributes:       attributes,
		SourceSpeaker:    strings.TrimSpace(wire.SourceSpeaker),
		Language:         strings.TrimSpace(wire.Language),
		Sensitivity:      wire.Sensitivity,
		Specificity:      wire.Specificity,
		Confidence:       wire.Confidence,
		Evidence:         evidence,
	}
	if eventTime != nil {
		revision.EventTimeStart = eventTime.Start
		revision.EventTimeEnd = eventTime.End
		revision.TimePrecision = eventTime.Precision
		revision.TimeSource = eventTime.Source
	}
	if validTime != nil {
		revision.ValidFrom = validTime.Start
		revision.ValidUntil = validTime.End
	}
	if err := domain.ValidateMemoryRevision(revision); err != nil {
		return domain.MemoryRevision{}, fmt.Errorf("%w: %v", errInvalidStructuredOutput, err)
	}
	return revision, nil
}

func normalizeStructuredEvidence(wire structuredEvidenceWire) (domain.RevisionEvidence, error) {
	evidenceID := strings.TrimSpace(wire.EvidenceID)
	eventID := strings.TrimSpace(wire.EventID)
	if evidenceID == "" || eventID == "" {
		return domain.RevisionEvidence{}, fmt.Errorf("%w: evidence identity required", errInvalidStructuredOutput)
	}
	role := domain.EvidenceRole(strings.TrimSpace(wire.Role))
	if !role.Valid() {
		return domain.RevisionEvidence{}, fmt.Errorf("%w: invalid evidence role %q", errInvalidStructuredOutput, wire.Role)
	}
	if (wire.StartOffset == nil) != (wire.EndOffset == nil) {
		return domain.RevisionEvidence{}, fmt.Errorf("%w: half evidence range", errInvalidStructuredOutput)
	}
	if wire.StartOffset != nil && (*wire.StartOffset < 0 || *wire.EndOffset <= *wire.StartOffset) {
		return domain.RevisionEvidence{}, fmt.Errorf("%w: invalid evidence range", errInvalidStructuredOutput)
	}
	return domain.RevisionEvidence{
		EvidenceID:    evidenceID,
		EventID:       eventID,
		EventTime:     wire.EventTime,
		EventSequence: wire.EventSequence,
		Role:          role,
		StartOffset:   wire.StartOffset,
		EndOffset:     wire.EndOffset,
	}, nil
}

func normalizeOptionalTime(wire *timeProposalWire) (*TimeProposal, error) {
	if wire == nil {
		return nil, nil
	}
	proposal := TimeProposal{
		Expression: wire.Expression,
		Start:      wire.Start,
		End:        wire.End,
		Precision:  domain.TimePrecision(strings.TrimSpace(wire.Precision)),
		Source:     wire.Source,
	}
	normalized, err := proposal.Normalize()
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

func normalizeEntityRefs(values []string, maximum int) ([]string, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("%w: entity refs out of bounds", errInvalidStructuredOutput)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || !boundedContractString(trimmed) {
			return nil, fmt.Errorf("%w: invalid entity ref", errInvalidStructuredOutput)
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeAttributes(values map[string]string, maximum int) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maximum {
		return nil, fmt.Errorf("%w: attributes out of bounds", errInvalidStructuredOutput)
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || !boundedContractString(trimmedKey) || !boundedOptionalContractString(trimmedValue) {
			return nil, fmt.Errorf("%w: invalid attribute", errInvalidStructuredOutput)
		}
		result[trimmedKey] = trimmedValue
	}
	return result, nil
}

// TypedGraphBounds bounds one typed graph extraction response.
type TypedGraphBounds struct {
	MaxEntities  int
	MaxMentions  int
	MaxEvents    int
	MaxFacts     int
	MaxRelations int
	MaxAliases   int
}

func (b TypedGraphBounds) withDefaults() TypedGraphBounds {
	if b.MaxEntities <= 0 {
		b.MaxEntities = defaultGraphMaxItems
	}
	if b.MaxMentions <= 0 {
		b.MaxMentions = defaultGraphMaxItems
	}
	if b.MaxEvents <= 0 {
		b.MaxEvents = defaultGraphMaxItems
	}
	if b.MaxFacts <= 0 {
		b.MaxFacts = defaultGraphMaxItems
	}
	if b.MaxRelations <= 0 {
		b.MaxRelations = defaultGraphMaxItems
	}
	if b.MaxAliases <= 0 {
		b.MaxAliases = 16
	}
	return b
}

// TypedGraphEntity is a candidate entity with a closed type.
type TypedGraphEntity struct {
	LocalID       string
	CanonicalName string
	EntityType    string
	Aliases       []string
	Confidence    float64
}

// TypedGraphMention is a normalized surface mention of a typed entity inside
// the source content. Offsets are 0-based rune offsets into the source content.
type TypedGraphMention struct {
	EntityLocalID string
	Text          string
	StartRune     int
	EndRune       int
	Confidence    float64
}

// TypedGraphEvent is a candidate event with participants, an optional place,
// and a normalized time proposal.
type TypedGraphEvent struct {
	LocalID             string
	EventType           string
	ParticipantLocalIDs []string
	PlaceLocalID        string
	Time                *TimeProposal
	Confidence          float64
}

// TypedGraphFact is a candidate entity fact. Exactly one of an entity object or
// a typed literal object is populated.
type TypedGraphFact struct {
	SubjectLocalID    string
	Predicate         string
	ObjectLocalID     string
	ObjectLiteral     string
	ObjectLiteralType string
	Confidence        float64
	ValidTime         *TimeProposal
}

// TypedGraphRelation is a revision relation. Broad related_to is not a valid
// relation type.
type TypedGraphRelation struct {
	RelationType     string
	TargetMemoryID   string
	TargetRevisionID string
	Confidence       float64
}

// TypedGraphResult is the validated typed graph proposal set.
type TypedGraphResult struct {
	Entities  []TypedGraphEntity
	Mentions  []TypedGraphMention
	Events    []TypedGraphEvent
	Facts     []TypedGraphFact
	Relations []TypedGraphRelation
}

// PredicateSpec defines one closed, directional graph predicate.
type PredicateSpec struct {
	Name         string
	SubjectTypes []string
	ObjectTypes  []string
	LiteralType  string
	Inverse      string
}

var typedPredicateRegistry = map[string]PredicateSpec{
	"member_of":             {Name: "member_of", SubjectTypes: []string{"person", "organization"}, ObjectTypes: []string{"organization"}},
	"located_at":            {Name: "located_at", SubjectTypes: []string{"person", "organization", "event", "place"}, ObjectTypes: []string{"place"}},
	"owned_by":              {Name: "owned_by", SubjectTypes: []string{"project", "product", "organization"}, ObjectTypes: []string{"person", "organization"}},
	"participates_in":       {Name: "participates_in", SubjectTypes: []string{"person", "organization"}, ObjectTypes: []string{"event", "project", "procedure"}},
	"created_by":            {Name: "created_by", SubjectTypes: []string{"project", "product", "event"}, ObjectTypes: []string{"person", "organization"}},
	"depends_on":            {Name: "depends_on", SubjectTypes: []string{"project", "product", "procedure"}, ObjectTypes: []string{"project", "product", "procedure"}},
	"has_status":            {Name: "has_status", SubjectTypes: []string{"project", "product", "plan"}, LiteralType: "string"},
	"has_participant_count": {Name: "has_participant_count", SubjectTypes: []string{"event"}, LiteralType: "number"},
	"is_active":             {Name: "is_active", SubjectTypes: []string{"project", "plan"}, LiteralType: "boolean"},
}

// PredicateNames returns the sorted closed predicate set for identity hashing.
func PredicateNames() []string {
	names := make([]string, 0, len(typedPredicateRegistry))
	for name := range typedPredicateRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LookupPredicate returns the closed definition for a predicate name.
func LookupPredicate(name string) (PredicateSpec, bool) {
	spec, ok := typedPredicateRegistry[strings.TrimSpace(name)]
	return spec, ok
}

var typedEventTypes = map[string]struct{}{
	"meeting": {}, "message": {}, "creation": {}, "decision": {},
	"travel": {}, "transaction": {}, "deployment": {}, "change": {}, "other": {},
}

var typedRelationTypes = map[string]struct{}{
	"supports": {}, "contradicts": {}, "supersedes": {}, "derived_from": {},
}

// DecodeTypedGraphOutput strictly decodes and validates a typed graph response.
// Unknown fields, unknown entity/event types, unknown or type-incompatible
// predicates, invalid references, and out-of-bounds output are rejected.
func DecodeTypedGraphOutput(data []byte, bounds TypedGraphBounds) (TypedGraphResult, error) {
	bounds = bounds.withDefaults()
	var output typedGraphOutputWire
	if err := decodeSingleStrict(data, &output); err != nil {
		return TypedGraphResult{}, fmt.Errorf("%w: malformed output", errInvalidGraphOutput)
	}
	result := TypedGraphResult{}
	entities := make(map[string]TypedGraphEntity, len(output.Entities))
	for _, item := range output.Entities {
		entity, err := normalizeTypedEntity(item, bounds)
		if err != nil {
			return TypedGraphResult{}, err
		}
		if _, duplicate := entities[entity.LocalID]; duplicate {
			return TypedGraphResult{}, fmt.Errorf("%w: duplicate entity %q", errInvalidGraphOutput, entity.LocalID)
		}
		entities[entity.LocalID] = entity
		result.Entities = append(result.Entities, entity)
	}
	for _, item := range output.Mentions {
		mention, err := normalizeTypedMention(item, entities)
		if err != nil {
			return TypedGraphResult{}, err
		}
		result.Mentions = append(result.Mentions, mention)
	}
	for _, item := range output.Events {
		event, err := normalizeTypedEvent(item, entities)
		if err != nil {
			return TypedGraphResult{}, err
		}
		result.Events = append(result.Events, event)
	}
	for _, item := range output.Facts {
		fact, err := normalizeTypedFact(item, entities)
		if err != nil {
			return TypedGraphResult{}, err
		}
		result.Facts = append(result.Facts, fact)
	}
	for _, item := range output.Relations {
		relation, err := normalizeTypedRelation(item)
		if err != nil {
			return TypedGraphResult{}, err
		}
		result.Relations = append(result.Relations, relation)
	}
	if len(result.Entities) > bounds.MaxEntities || len(result.Mentions) > bounds.MaxMentions ||
		len(result.Events) > bounds.MaxEvents || len(result.Facts) > bounds.MaxFacts ||
		len(result.Relations) > bounds.MaxRelations {
		return TypedGraphResult{}, fmt.Errorf("%w: output out of bounds", errInvalidGraphOutput)
	}
	return result, nil
}

func normalizeTypedMention(wire typedMentionWire, entities map[string]TypedGraphEntity) (TypedGraphMention, error) {
	entityID := strings.TrimSpace(wire.EntityLocalID)
	if _, ok := entities[entityID]; !ok {
		return TypedGraphMention{}, fmt.Errorf("%w: unknown mention entity %q", errInvalidGraphOutput, entityID)
	}
	if !boundedContractString(wire.Text) || !finiteUnit(wire.Confidence) {
		return TypedGraphMention{}, fmt.Errorf("%w: invalid mention", errInvalidGraphOutput)
	}
	if wire.StartRune < 0 || wire.EndRune <= wire.StartRune {
		return TypedGraphMention{}, fmt.Errorf("%w: invalid mention range", errInvalidGraphOutput)
	}
	return TypedGraphMention{EntityLocalID: entityID, Text: wire.Text, StartRune: wire.StartRune, EndRune: wire.EndRune, Confidence: wire.Confidence}, nil
}

func normalizeTypedEntity(wire typedEntityWire, bounds TypedGraphBounds) (TypedGraphEntity, error) {
	localID := strings.TrimSpace(wire.LocalID)
	canonical := strings.TrimSpace(wire.CanonicalName)
	entityType := strings.TrimSpace(wire.EntityType)
	if localID == "" || !boundedContractString(localID) || canonical == "" || !boundedContractString(canonical) {
		return TypedGraphEntity{}, fmt.Errorf("%w: invalid entity", errInvalidGraphOutput)
	}
	if _, ok := typedEntityTypes[entityType]; !ok {
		return TypedGraphEntity{}, fmt.Errorf("%w: %q", errUnknownEntityType, entityType)
	}
	if !finiteUnit(wire.Confidence) {
		return TypedGraphEntity{}, fmt.Errorf("%w: invalid entity confidence", errInvalidGraphOutput)
	}
	if len(wire.Aliases) > bounds.MaxAliases {
		return TypedGraphEntity{}, fmt.Errorf("%w: aliases out of bounds", errInvalidGraphOutput)
	}
	seen := make(map[string]struct{}, len(wire.Aliases))
	aliases := make([]string, 0, len(wire.Aliases))
	for _, alias := range wire.Aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" || !boundedContractString(trimmed) {
			return TypedGraphEntity{}, fmt.Errorf("%w: invalid alias", errInvalidGraphOutput)
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		aliases = append(aliases, trimmed)
	}
	return TypedGraphEntity{LocalID: localID, CanonicalName: canonical, EntityType: entityType, Aliases: aliases, Confidence: wire.Confidence}, nil
}

func normalizeTypedEvent(wire typedEventWire, entities map[string]TypedGraphEntity) (TypedGraphEvent, error) {
	localID := strings.TrimSpace(wire.LocalID)
	eventType := strings.TrimSpace(wire.EventType)
	if localID == "" || !boundedContractString(localID) {
		return TypedGraphEvent{}, fmt.Errorf("%w: invalid event", errInvalidGraphOutput)
	}
	if _, ok := typedEventTypes[eventType]; !ok {
		return TypedGraphEvent{}, fmt.Errorf("%w: %q", errUnknownEventType, eventType)
	}
	if !finiteUnit(wire.Confidence) {
		return TypedGraphEvent{}, fmt.Errorf("%w: invalid event confidence", errInvalidGraphOutput)
	}
	participants, err := referencedEntities(wire.ParticipantLocalIDs, entities, "")
	if err != nil {
		return TypedGraphEvent{}, err
	}
	place := strings.TrimSpace(wire.PlaceLocalID)
	if place != "" {
		entity, ok := entities[place]
		if !ok {
			return TypedGraphEvent{}, fmt.Errorf("%w: unknown event place", errInvalidGraphOutput)
		}
		if entity.EntityType != "place" {
			return TypedGraphEvent{}, fmt.Errorf("%w: event place is not a place", errInvalidGraphOutput)
		}
	}
	eventTime, err := normalizeOptionalTime(wire.Time)
	if err != nil {
		return TypedGraphEvent{}, err
	}
	return TypedGraphEvent{LocalID: localID, EventType: eventType, ParticipantLocalIDs: participants, PlaceLocalID: place, Time: eventTime, Confidence: wire.Confidence}, nil
}

func normalizeTypedFact(wire typedFactWire, entities map[string]TypedGraphEntity) (TypedGraphFact, error) {
	subjectID := strings.TrimSpace(wire.SubjectLocalID)
	subject, ok := entities[subjectID]
	if !ok {
		return TypedGraphFact{}, fmt.Errorf("%w: unknown fact subject", errInvalidGraphOutput)
	}
	spec, ok := LookupPredicate(wire.Predicate)
	if !ok {
		return TypedGraphFact{}, fmt.Errorf("%w: %q", errUnknownPredicate, wire.Predicate)
	}
	if !containsString(spec.SubjectTypes, subject.EntityType) {
		return TypedGraphFact{}, fmt.Errorf("%w: subject type %q for %q", errPredicateTypeMismatch, subject.EntityType, spec.Name)
	}
	if !finiteUnit(wire.Confidence) {
		return TypedGraphFact{}, fmt.Errorf("%w: invalid fact confidence", errInvalidGraphOutput)
	}
	objectID := strings.TrimSpace(wire.ObjectLocalID)
	hasObject := objectID != ""
	hasLiteral := wire.ObjectLiteral != ""
	if hasObject == hasLiteral {
		return TypedGraphFact{}, fmt.Errorf("%w: fact needs exactly one object", errInvalidGraphOutput)
	}
	if hasObject {
		if spec.LiteralType != "" {
			return TypedGraphFact{}, fmt.Errorf("%w: %q expects a literal", errPredicateTypeMismatch, spec.Name)
		}
		object, ok := entities[objectID]
		if !ok {
			return TypedGraphFact{}, fmt.Errorf("%w: unknown fact object", errInvalidGraphOutput)
		}
		if objectID == subjectID {
			return TypedGraphFact{}, fmt.Errorf("%w: self fact", errInvalidGraphOutput)
		}
		if !containsString(spec.ObjectTypes, object.EntityType) {
			return TypedGraphFact{}, fmt.Errorf("%w: object type %q for %q", errPredicateTypeMismatch, object.EntityType, spec.Name)
		}
	} else {
		if !boundedContractString(wire.ObjectLiteral) {
			return TypedGraphFact{}, fmt.Errorf("%w: invalid fact literal", errInvalidGraphOutput)
		}
		if spec.LiteralType == "" || wire.ObjectLiteralType != spec.LiteralType {
			return TypedGraphFact{}, fmt.Errorf("%w: literal type %q for %q", errPredicateTypeMismatch, wire.ObjectLiteralType, spec.Name)
		}
	}
	validTime, err := normalizeOptionalTime(wire.ValidTime)
	if err != nil {
		return TypedGraphFact{}, err
	}
	return TypedGraphFact{
		SubjectLocalID: subjectID, Predicate: spec.Name,
		ObjectLocalID: objectID, ObjectLiteral: wire.ObjectLiteral, ObjectLiteralType: wire.ObjectLiteralType,
		Confidence: wire.Confidence, ValidTime: validTime,
	}, nil
}

func normalizeTypedRelation(wire typedRelationWire) (TypedGraphRelation, error) {
	relationType := strings.TrimSpace(wire.RelationType)
	if _, ok := typedRelationTypes[relationType]; !ok {
		return TypedGraphRelation{}, fmt.Errorf("%w: invalid relation type %q", errInvalidGraphOutput, relationType)
	}
	targetMemoryID := strings.TrimSpace(wire.TargetMemoryID)
	targetRevisionID := strings.TrimSpace(wire.TargetRevisionID)
	if targetMemoryID == "" || targetRevisionID == "" || !boundedContractString(targetMemoryID) || !boundedContractString(targetRevisionID) {
		return TypedGraphRelation{}, fmt.Errorf("%w: invalid relation target", errInvalidGraphOutput)
	}
	if !finiteUnit(wire.Confidence) {
		return TypedGraphRelation{}, fmt.Errorf("%w: invalid relation confidence", errInvalidGraphOutput)
	}
	return TypedGraphRelation{RelationType: relationType, TargetMemoryID: targetMemoryID, TargetRevisionID: targetRevisionID, Confidence: wire.Confidence}, nil
}

func referencedEntities(ids []string, entities map[string]TypedGraphEntity, requiredType string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		entity, ok := entities[trimmed]
		if !ok {
			return nil, fmt.Errorf("%w: unknown entity reference %q", errInvalidGraphOutput, trimmed)
		}
		if requiredType != "" && entity.EntityType != requiredType {
			return nil, fmt.Errorf("%w: entity %q is not %q", errInvalidGraphOutput, trimmed, requiredType)
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func finiteUnit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func boundedContractString(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxContractIdentityChars
}

func boundedOptionalContractString(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxContractIdentityChars
}

var typedEntityTypes = map[string]struct{}{
	"person": {}, "organization": {}, "place": {}, "project": {},
	"product": {}, "event": {}, "concept": {}, "other": {},
}

type structuredProcessOutputWire struct {
	Category   string                    `json:"category"`
	Candidates []structuredCandidateWire `json:"candidates"`
}

type structuredCandidateWire struct {
	Kind             string                   `json:"kind"`
	DisplayContent   string                   `json:"display_content"`
	CanonicalContent string                   `json:"canonical_content"`
	SubjectEntityIDs []string                 `json:"subject_entity_ids"`
	ObjectEntityIDs  []string                 `json:"object_entity_ids"`
	EventType        string                   `json:"event_type"`
	Attributes       map[string]string        `json:"attributes"`
	EventTime        *timeProposalWire        `json:"event_time"`
	ValidTime        *timeProposalWire        `json:"valid_time"`
	SourceSpeaker    string                   `json:"source_speaker"`
	Language         string                   `json:"language"`
	Sensitivity      string                   `json:"sensitivity"`
	Specificity      float64                  `json:"specificity"`
	Confidence       float64                  `json:"confidence"`
	Evidence         []structuredEvidenceWire `json:"evidence"`
}

type timeProposalWire struct {
	Expression string     `json:"expression"`
	Start      *time.Time `json:"start"`
	End        *time.Time `json:"end"`
	Precision  string     `json:"precision"`
	Source     string     `json:"source"`
}

type structuredEvidenceWire struct {
	EvidenceID    string     `json:"evidence_id"`
	EventID       string     `json:"event_id"`
	EventTime     *time.Time `json:"event_time"`
	EventSequence *int64     `json:"event_sequence"`
	Role          string     `json:"role"`
	StartOffset   *int64     `json:"start_offset"`
	EndOffset     *int64     `json:"end_offset"`
}

type typedGraphOutputWire struct {
	Entities  []typedEntityWire   `json:"entities"`
	Mentions  []typedMentionWire  `json:"mentions"`
	Events    []typedEventWire    `json:"events"`
	Facts     []typedFactWire     `json:"facts"`
	Relations []typedRelationWire `json:"relations"`
}

type typedMentionWire struct {
	EntityLocalID string  `json:"entity_local_id"`
	Text          string  `json:"text"`
	StartRune     int     `json:"start_rune"`
	EndRune       int     `json:"end_rune"`
	Confidence    float64 `json:"confidence"`
}

type typedEntityWire struct {
	LocalID       string   `json:"local_id"`
	CanonicalName string   `json:"canonical_name"`
	EntityType    string   `json:"entity_type"`
	Aliases       []string `json:"aliases"`
	Confidence    float64  `json:"confidence"`
}

type typedEventWire struct {
	LocalID             string            `json:"local_id"`
	EventType           string            `json:"event_type"`
	ParticipantLocalIDs []string          `json:"participant_local_ids"`
	PlaceLocalID        string            `json:"place_local_id"`
	Time                *timeProposalWire `json:"time"`
	Confidence          float64           `json:"confidence"`
}

type typedFactWire struct {
	SubjectLocalID    string            `json:"subject_local_id"`
	Predicate         string            `json:"predicate"`
	ObjectLocalID     string            `json:"object_local_id"`
	ObjectLiteral     string            `json:"object_literal"`
	ObjectLiteralType string            `json:"object_literal_type"`
	Confidence        float64           `json:"confidence"`
	ValidTime         *timeProposalWire `json:"valid_time"`
}

type typedRelationWire struct {
	RelationType     string  `json:"relation_type"`
	TargetMemoryID   string  `json:"target_memory_id"`
	TargetRevisionID string  `json:"target_revision_id"`
	Confidence       float64 `json:"confidence"`
}
