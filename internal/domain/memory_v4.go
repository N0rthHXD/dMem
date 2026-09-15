package domain

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// MemoryKind is the explicit retrieval role of a memory revision (ADR 0035).
type MemoryKind string

const (
	MemoryKindFact       MemoryKind = "fact"
	MemoryKindEvent      MemoryKind = "event"
	MemoryKindPreference MemoryKind = "preference"
	MemoryKindPlan       MemoryKind = "plan"
	MemoryKindProcedure  MemoryKind = "procedure"
	MemoryKindScene      MemoryKind = "scene"
	MemoryKindTopic      MemoryKind = "topic"
	MemoryKindTimeline   MemoryKind = "timeline"
)

func (k MemoryKind) Valid() bool {
	switch k {
	case MemoryKindFact, MemoryKindEvent, MemoryKindPreference, MemoryKindPlan,
		MemoryKindProcedure, MemoryKindScene, MemoryKindTopic, MemoryKindTimeline:
		return true
	default:
		return false
	}
}

// EvidenceRole describes how a cited span supports a revision (ADR 0035).
type EvidenceRole string

const (
	EvidenceRoleDirect         EvidenceRole = "direct"
	EvidenceRoleDerived        EvidenceRole = "derived"
	EvidenceRoleTemporalAnchor EvidenceRole = "temporal_anchor"
	EvidenceRoleContext        EvidenceRole = "context"
)

func (r EvidenceRole) Valid() bool {
	switch r {
	case EvidenceRoleDirect, EvidenceRoleDerived, EvidenceRoleTemporalAnchor, EvidenceRoleContext:
		return true
	default:
		return false
	}
}

// TimePrecision is the normalized precision of an event or valid interval.
type TimePrecision string

const (
	TimePrecisionExact       TimePrecision = "exact"
	TimePrecisionDay         TimePrecision = "day"
	TimePrecisionMonth       TimePrecision = "month"
	TimePrecisionYear        TimePrecision = "year"
	TimePrecisionApproximate TimePrecision = "approximate"
	TimePrecisionUnknown     TimePrecision = "unknown"
)

func (p TimePrecision) Valid() bool {
	switch p {
	case TimePrecisionExact, TimePrecisionDay, TimePrecisionMonth, TimePrecisionYear,
		TimePrecisionApproximate, TimePrecisionUnknown:
		return true
	default:
		return false
	}
}

// RevisionEvidence is a stable ordered evidence span with a support role.
// Order is defined by event timestamp, event sequence, start offset, evidence ID.
type RevisionEvidence struct {
	EvidenceID    string       `json:"evidence_id"`
	EventID       string       `json:"event_id"`
	EventTime     *time.Time   `json:"event_time,omitempty"`
	EventSequence *int64       `json:"event_sequence,omitempty"`
	Role          EvidenceRole `json:"role"`
	StartOffset   *int64       `json:"start_offset,omitempty"`
	EndOffset     *int64       `json:"end_offset,omitempty"`
}

func (e RevisionEvidence) Valid() bool {
	if e.EventID == "" || !e.Role.Valid() {
		return false
	}
	if (e.StartOffset == nil) != (e.EndOffset == nil) {
		return false
	}
	if e.StartOffset != nil && *e.EndOffset <= *e.StartOffset {
		return false
	}
	return true
}

// MemoryRevision is immutable revision authority with normalized retrieval fields.
type MemoryRevision struct {
	RevisionID             string             `json:"revision_id"`
	MemoryID               string             `json:"memory_id"`
	ParentRevisionID       *string            `json:"parent_revision_id,omitempty"`
	RestoredFromRevisionID *string            `json:"restored_from_revision_id,omitempty"`
	Kind                   MemoryKind         `json:"kind"`
	DisplayContent         string             `json:"display_content"`
	CanonicalContent       string             `json:"canonical_content"`
	CanonicalHash          string             `json:"canonical_hash,omitempty"`
	SubjectEntityIDs       []string           `json:"subject_entity_ids,omitempty"`
	ObjectEntityIDs        []string           `json:"object_entity_ids,omitempty"`
	EventType              string             `json:"event_type,omitempty"`
	Attributes             map[string]string  `json:"attributes,omitempty"`
	EventTimeStart         *time.Time         `json:"event_time_start,omitempty"`
	EventTimeEnd           *time.Time         `json:"event_time_end,omitempty"`
	TimePrecision          TimePrecision      `json:"time_precision,omitempty"`
	TimeSource             string             `json:"time_source,omitempty"`
	ValidFrom              *time.Time         `json:"valid_from,omitempty"`
	ValidUntil             *time.Time         `json:"valid_until,omitempty"`
	SourceSpeaker          string             `json:"source_speaker,omitempty"`
	Language               string             `json:"language,omitempty"`
	Sensitivity            string             `json:"sensitivity,omitempty"`
	Specificity            float64            `json:"specificity"`
	Confidence             float64            `json:"confidence"`
	Evidence               []RevisionEvidence `json:"evidence"`
	CreatedBy              string             `json:"created_by"`
	ProcessingVersion      string             `json:"processing_version"`
	ProcessorIdentity      string             `json:"processor_identity,omitempty"`
	CreatedAt              string             `json:"created_at"`
}

var (
	ErrKindRequired            = errors.New("memory kind is required or unsupported")
	ErrCanonicalContentEmpty   = errors.New("canonical content is required")
	ErrEventWindowInvalid      = errors.New("event time window is invalid")
	ErrValidWindowInvalid      = errors.New("valid time window is invalid")
	ErrConfidenceOutOfRange    = errors.New("confidence must be within [0,1]")
	ErrSpecificityOutOfRange   = errors.New("specificity must be within [0,1]")
	ErrNoAuthoritativeEvidence = errors.New("revision requires direct or derived evidence")
	ErrEvidenceInvalid         = errors.New("evidence span is invalid")
	ErrTimePrecisionInvalid    = errors.New("time precision is unsupported")
)

// ValidateMemoryRevision applies the deterministic checks required before a
// proposal may be published as authority.
func ValidateMemoryRevision(rev MemoryRevision) error {
	if !rev.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrKindRequired, rev.Kind)
	}
	if strings.TrimSpace(rev.CanonicalContent) == "" {
		return ErrCanonicalContentEmpty
	}
	if rev.Confidence < 0 || rev.Confidence > 1 || math.IsNaN(rev.Confidence) || math.IsInf(rev.Confidence, 0) {
		return fmt.Errorf("%w: %v", ErrConfidenceOutOfRange, rev.Confidence)
	}
	if rev.Specificity < 0 || rev.Specificity > 1 || math.IsNaN(rev.Specificity) || math.IsInf(rev.Specificity, 0) {
		return fmt.Errorf("%w: %v", ErrSpecificityOutOfRange, rev.Specificity)
	}
	if rev.TimePrecision != "" && !rev.TimePrecision.Valid() {
		return fmt.Errorf("%w: %q", ErrTimePrecisionInvalid, rev.TimePrecision)
	}
	if rev.EventTimeStart != nil && rev.EventTimeEnd != nil && !rev.EventTimeEnd.After(*rev.EventTimeStart) {
		return ErrEventWindowInvalid
	}
	if rev.ValidFrom != nil && rev.ValidUntil != nil && !rev.ValidUntil.After(*rev.ValidFrom) {
		return ErrValidWindowInvalid
	}
	for _, ev := range rev.Evidence {
		if !ev.Valid() {
			return fmt.Errorf("%w: %q", ErrEvidenceInvalid, ev.EvidenceID)
		}
	}
	if !HasAuthoritativeEvidence(rev.Evidence) {
		return ErrNoAuthoritativeEvidence
	}
	return nil
}

// HasAuthoritativeEvidence reports whether the evidence set can establish
// authority: at least one direct or derived span. Context-only evidence cannot.
func HasAuthoritativeEvidence(evidence []RevisionEvidence) bool {
	for _, ev := range evidence {
		if ev.Role == EvidenceRoleDirect || ev.Role == EvidenceRoleDerived {
			return true
		}
	}
	return false
}

// OrderRevisionEvidence sorts evidence into the stable order defined by ADR 0035:
// event timestamp, event sequence, start offset, evidence ID. Missing values sort
// last and equal keys fall back to the stable evidence ID.
func OrderRevisionEvidence(evidence []RevisionEvidence) {
	sort.SliceStable(evidence, func(i, j int) bool {
		a, b := evidence[i], evidence[j]
		if c := orderTime(a.EventTime, b.EventTime); c != 0 {
			return c < 0
		}
		if c := orderInt64(a.EventSequence, b.EventSequence); c != 0 {
			return c < 0
		}
		if c := orderInt64(a.StartOffset, b.StartOffset); c != 0 {
			return c < 0
		}
		return a.EvidenceID < b.EvidenceID
	})
}

func orderTime(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case a.Before(*b):
		return -1
	case a.After(*b):
		return 1
	default:
		return 0
	}
}

func orderInt64(a, b *int64) int {
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
