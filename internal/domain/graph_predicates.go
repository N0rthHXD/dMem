package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PredicateRegistryVersion identifies the closed typed predicate registry that
// graph extraction, publication, traversal, and scoring all share. A change to
// any predicate definition changes this identity.
const PredicateRegistryVersion = "predicate-registry-v2"

type PredicateDirection string

const (
	PredicateDirected  PredicateDirection = "directed"
	PredicateSymmetric PredicateDirection = "symmetric"
)

func (d PredicateDirection) Valid() bool {
	return d == PredicateDirected || d == PredicateSymmetric
}

type PredicateCardinality string

const (
	PredicateCardinalityOne  PredicateCardinality = "one"
	PredicateCardinalityMany PredicateCardinality = "many"
)

func (c PredicateCardinality) Valid() bool {
	return c == PredicateCardinalityOne || c == PredicateCardinalityMany
}

// PredicateTransitivity controls whether the predicate may be composed along a
// multi-hop path. "permitted" is reserved for equivalence-like predicates.
type PredicateTransitivity string

const (
	PredicateTransitivityNone      PredicateTransitivity = "none"
	PredicateTransitivityPermitted PredicateTransitivity = "permitted"
)

func (t PredicateTransitivity) Valid() bool {
	return t == PredicateTransitivityNone || t == PredicateTransitivityPermitted
}

// PredicateTemporalPolicy describes which time interval a fact of this predicate
// is bound to. Static facts have no temporal validity.
type PredicateTemporalPolicy string

const (
	PredicateTemporalStatic    PredicateTemporalPolicy = "static"
	PredicateTemporalValidTime PredicateTemporalPolicy = "valid_time"
	PredicateTemporalEventTime PredicateTemporalPolicy = "event_time"
)

func (p PredicateTemporalPolicy) Valid() bool {
	return p == PredicateTemporalStatic || p == PredicateTemporalValidTime || p == PredicateTemporalEventTime
}

// PredicatePathPosition is one position a predicate may occupy inside a bounded
// traversal. "first" is the hop out of a resolved query entity, "second" is a
// subsequent hop, and "intermediate" permits the predicate to connect two hops.
type PredicatePathPosition string

const (
	PredicatePathFirst        PredicatePathPosition = "first"
	PredicatePathSecond       PredicatePathPosition = "second"
	PredicatePathIntermediate PredicatePathPosition = "intermediate"
)

func (p PredicatePathPosition) Valid() bool {
	return p == PredicatePathFirst || p == PredicatePathSecond || p == PredicatePathIntermediate
}

// PredicateAutoConfirm is the closed publication policy for a predicate.
type PredicateAutoConfirm string

const (
	PredicateAutoConfirmNever      PredicateAutoConfirm = "never"
	PredicateAutoConfirmExactMatch PredicateAutoConfirm = "exact_match"
	PredicateAutoConfirmThreshold  PredicateAutoConfirm = "threshold"
)

func (a PredicateAutoConfirm) Valid() bool {
	return a == PredicateAutoConfirmNever || a == PredicateAutoConfirmExactMatch || a == PredicateAutoConfirmThreshold
}

// PredicateDefinition is one closed, directional predicate. Provider output that
// names a predicate outside the registry is rejected, and publication,
// traversal, and scoring all consult these fields.
type PredicateDefinition struct {
	Name           string
	Description    string
	SubjectTypes   []string
	ObjectTypes    []string
	LiteralType    string
	Direction      PredicateDirection
	Inverse        string
	Cardinality    PredicateCardinality
	Transitivity   PredicateTransitivity
	TemporalPolicy PredicateTemporalPolicy
	PathPositions  []PredicatePathPosition
	AutoConfirm    PredicateAutoConfirm
	// MinConfidence is the publication threshold used when AutoConfirm is
	// PredicateAutoConfirmThreshold.
	MinConfidence float64
}

// Literal reports whether the predicate object is a typed literal rather than an
// entity reference.
func (d PredicateDefinition) Literal() bool { return d.LiteralType != "" }

// AllowsPathPosition reports whether the predicate may occupy position p.
func (d PredicateDefinition) AllowsPathPosition(p PredicatePathPosition) bool {
	for _, allowed := range d.PathPositions {
		if allowed == p {
			return true
		}
	}
	return false
}

// Valid checks that one definition is internally consistent.
func (d PredicateDefinition) Valid() bool {
	if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Name) != d.Name {
		return false
	}
	if !d.Direction.Valid() || !d.Cardinality.Valid() || !d.Transitivity.Valid() ||
		!d.TemporalPolicy.Valid() || !d.AutoConfirm.Valid() {
		return false
	}
	if d.Literal() {
		if d.ObjectTypes != nil || !validContractEntityTypes(d.SubjectTypes) {
			return false
		}
	} else {
		if len(d.ObjectTypes) == 0 || !validContractEntityTypes(d.SubjectTypes) || !validContractEntityTypes(d.ObjectTypes) {
			return false
		}
	}
	if d.Direction == PredicateSymmetric && d.Inverse != "" {
		return false
	}
	if d.Inverse == d.Name {
		return false
	}
	if len(d.PathPositions) == 0 {
		return false
	}
	for _, position := range d.PathPositions {
		if !position.Valid() {
			return false
		}
	}
	if d.AutoConfirm == PredicateAutoConfirmThreshold {
		if d.MinConfidence <= 0 || d.MinConfidence > 1 {
			return false
		}
	}
	return true
}

func validContractEntityTypes(types []string) bool {
	if len(types) == 0 {
		return false
	}
	for _, t := range types {
		switch t {
		case "person", "organization", "place", "project", "product", "event", "concept", "other":
		default:
			return false
		}
	}
	return true
}

func pred(name, description string, subjects []string, objects []string, literal string, direction PredicateDirection, inverse string, cardinality PredicateCardinality, transitivity PredicateTransitivity, temporal PredicateTemporalPolicy, autoConfirm PredicateAutoConfirm, minConfidence float64) PredicateDefinition {
	return PredicateDefinition{
		Name: name, Description: description, SubjectTypes: subjects, ObjectTypes: objects,
		LiteralType: literal, Direction: direction, Inverse: inverse, Cardinality: cardinality,
		Transitivity: transitivity, TemporalPolicy: temporal, AutoConfirm: autoConfirm,
		MinConfidence: minConfidence,
		PathPositions: []PredicatePathPosition{PredicatePathFirst, PredicatePathSecond},
	}
}

// predicateRegistry is the single closed source of truth. Provider output,
// publication policy, traversal eligibility, and scoring all consult it.
var predicateRegistry = buildPredicateRegistry()

func buildPredicateRegistry() map[string]PredicateDefinition {
	person := []string{"person"}
	org := []string{"organization"}
	place := []string{"place"}
	work := []string{"project", "product", "concept"}
	actor := []string{"person", "organization"}
	identity := []string{"person", "organization", "place", "project", "product", "event", "concept"}
	definitions := []PredicateDefinition{
		pred("same_as", "two mentions refer to the same identity", identity, identity, "", PredicateSymmetric, "", PredicateCardinalityOne, PredicateTransitivityPermitted, PredicateTemporalStatic, PredicateAutoConfirmExactMatch, 0),
		pred("member_of", "an actor is a member of an organization", []string{"person", "organization"}, org, "", PredicateDirected, "has_member", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.85),
		pred("has_member", "an organization has a member", org, []string{"person", "organization"}, "", PredicateDirected, "member_of", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmNever, 0),
		pred("works_for", "a person works for an organization", person, org, "", PredicateDirected, "employs", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.85),
		pred("employs", "an organization employs a person", org, person, "", PredicateDirected, "works_for", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmNever, 0),
		pred("reports_to", "a person reports to another person", person, person, "", PredicateDirected, "manages", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.8),
		pred("manages", "a person manages another person", person, person, "", PredicateDirected, "reports_to", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmNever, 0),
		pred("leads", "a person leads an organization or project", person, []string{"organization", "project"}, "", PredicateDirected, "", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.8),
		pred("located_at", "an entity is located at a place", []string{"person", "organization", "event", "place"}, place, "", PredicateDirected, "", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.85),
		pred("born_in", "a person was born in a place", person, place, "", PredicateDirected, "", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalStatic, PredicateAutoConfirmThreshold, 0.85),
		pred("owned_by", "a work is owned by an actor", []string{"project", "product", "organization"}, actor, "", PredicateDirected, "owns", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.85),
		pred("owns", "an actor owns a work", actor, []string{"project", "product", "organization"}, "", PredicateDirected, "owned_by", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmNever, 0),
		pred("prefers", "a person states a preference for something", person, []string{"concept", "product", "organization", "place", "event"}, "", PredicateDirected, "", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.85),
		pred("dislikes", "a person states a dislike for something", person, []string{"concept", "product", "organization", "place", "event"}, "", PredicateDirected, "", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.85),
		pred("works_on", "an actor works on a work", actor, work, "", PredicateDirected, "", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.8),
		pred("created", "an actor created a work", actor, work, "", PredicateDirected, "created_by", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalEventTime, PredicateAutoConfirmThreshold, 0.85),
		pred("created_by", "a work or event was created by an actor", []string{"project", "product", "event"}, actor, "", PredicateDirected, "created", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalStatic, PredicateAutoConfirmThreshold, 0.85),
		pred("participates_in", "an actor participates in an event, project, or concept", actor, []string{"event", "project", "concept"}, "", PredicateDirected, "has_participant", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalEventTime, PredicateAutoConfirmThreshold, 0.8),
		pred("has_participant", "an event or project has an actor participant", []string{"event", "project", "concept"}, actor, "", PredicateDirected, "participates_in", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalEventTime, PredicateAutoConfirmNever, 0),
		pred("plan_targets", "a plan targets an entity", []string{"concept"}, []string{"project", "product", "event", "place"}, "", PredicateDirected, "", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.8),
		pred("has_status", "an entity has a status literal", []string{"project", "product", "concept"}, nil, "string", PredicateDirected, "", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.8),
		pred("is_active", "an entity active-state literal", []string{"project", "concept"}, nil, "boolean", PredicateDirected, "", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.8),
		pred("occurred_at", "an event occurred at a place", []string{"event"}, place, "", PredicateDirected, "", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalEventTime, PredicateAutoConfirmThreshold, 0.85),
		pred("occurred_during", "an event occurred during a time literal", []string{"event"}, nil, "string", PredicateDirected, "", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalEventTime, PredicateAutoConfirmThreshold, 0.85),
		pred("has_participant_count", "an event has a participant count literal", []string{"event"}, nil, "number", PredicateDirected, "", PredicateCardinalityOne, PredicateTransitivityNone, PredicateTemporalEventTime, PredicateAutoConfirmThreshold, 0.8),
		pred("depends_on", "a work depends on another work", work, work, "", PredicateDirected, "required_by", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmThreshold, 0.8),
		pred("required_by", "a work is required by another work", work, work, "", PredicateDirected, "depends_on", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalValidTime, PredicateAutoConfirmNever, 0),
		pred("derived_from", "a work is derived from another work", work, work, "", PredicateDirected, "", PredicateCardinalityMany, PredicateTransitivityNone, PredicateTemporalStatic, PredicateAutoConfirmNever, 0),
	}
	registry := make(map[string]PredicateDefinition, len(definitions))
	for _, definition := range definitions {
		switch definition.Name {
		case "same_as":
			definition.PathPositions = []PredicatePathPosition{PredicatePathFirst, PredicatePathSecond, PredicatePathIntermediate}
		case "derived_from":
			// Provenance is a direct first-hop lookup, never a compositional
			// connector, so it may not occupy a downstream path position.
			definition.PathPositions = []PredicatePathPosition{PredicatePathFirst}
		}
		registry[definition.Name] = definition
	}
	return registry
}

// LookupPredicate returns the closed definition for a predicate name.
func LookupPredicate(name string) (PredicateDefinition, bool) {
	definition, ok := predicateRegistry[strings.TrimSpace(name)]
	return definition, ok
}

// PredicateNames returns every registered predicate name in sorted order.
func PredicateNames() []string {
	names := make([]string, 0, len(predicateRegistry))
	for name := range predicateRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PredicatePathNames returns the registered entity predicates that may occupy
// the given path position, sorted by name. Literal predicates are excluded
// because they have no traversable object entity.
func PredicatePathNames(position PredicatePathPosition) []string {
	names := make([]string, 0, len(predicateRegistry))
	for name, definition := range predicateRegistry {
		if definition.Literal() {
			continue
		}
		if definition.AllowsPathPosition(position) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// PredicateDefinitions returns every definition sorted by name.
func PredicateDefinitions() []PredicateDefinition {
	definitions := make([]PredicateDefinition, 0, len(predicateRegistry))
	for _, definition := range predicateRegistry {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions
}

// ValidatePredicateRegistry verifies that the closed registry is internally
// consistent: every definition is valid and every inverse reference resolves.
func ValidatePredicateRegistry() error {
	for name, definition := range predicateRegistry {
		if !definition.Valid() {
			return fmt.Errorf("predicate %q is invalid", name)
		}
		if definition.Inverse != "" {
			inverse, ok := predicateRegistry[definition.Inverse]
			if !ok {
				return fmt.Errorf("predicate %q references unknown inverse %q", name, definition.Inverse)
			}
			if inverse.Inverse != name {
				return fmt.Errorf("predicate %q inverse %q is not symmetric", name, definition.Inverse)
			}
		}
	}
	return nil
}

// PredicateRegistryFingerprint is a deterministic hash of every predicate
// definition. It is embedded in algorithm identity and stored metadata so a
// registry change invalidates dependent derived structures.
func PredicateRegistryFingerprint() string {
	encoded, _ := json.Marshal(PredicateDefinitions())
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// PredicateRegistryIdentity returns the versioned identity of the live registry.
func PredicateRegistryIdentity() string {
	return PredicateRegistryVersion + ":" + PredicateRegistryFingerprint()
}
