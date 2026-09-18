package consolidation

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// Rules are the versioned deterministic consolidation thresholds that classify a
// structured candidate against existing memory revisions. They are a
// protocol-independent configuration value: the persistence adapter supplies
// them and the classifier never reads configuration itself.
type Rules struct {
	Version               string
	ExactHashEnabled      bool
	SameSourceEventCosine float64
	SameSubjectKindCosine float64
}

// DefaultRules returns the built-in deterministic consolidation thresholds.
func DefaultRules() Rules {
	return Rules{
		Version:               "consolidation-v1",
		ExactHashEnabled:      true,
		SameSourceEventCosine: 0.90,
		SameSubjectKindCosine: 0.94,
	}
}

// Similarity carries optional semantic proximity. When OK is false no cosine was
// computed and semantic-only rules must not fire.
type Similarity struct {
	Cosine float64
	OK     bool
}

// EvidenceRange is one bounded evidence span of a neighbor revision.
type EvidenceRange struct {
	EventID     string
	StartOffset *int64
	EndOffset   *int64
}

// Neighbor is one bounded recall hit offered to deterministic classification
// and, when the rules leave the candidate unresolved, to the optional provider
// classifier. It contains only what classification and provider proposals need;
// persistence rows and SQL stay in the runtime adapter.
type Neighbor struct {
	MemoryID         string
	RevisionID       string
	Kind             domain.MemoryKind
	CanonicalHash    string
	CanonicalContent string
	Subjects         []string
	Objects          []string
	Attributes       map[string]string
	EvidenceEventIDs []string
	EvidenceRanges   []EvidenceRange
	ValidFrom        *time.Time
	ValidUntil       *time.Time
	Specificity      float64
	Precision        domain.TimePrecision
	Similarity       *float64
}

// Outcome is a consolidation decision. It remains a proposal until the atomic
// confirmation path records it; the classifier never mutates authority.
type Outcome struct {
	Action           domain.ConsolidationAction
	ReasonCode       string
	TargetMemoryID   string
	TargetRevisionID string
	Similarity       *float64
	Classification   domain.ConsolidationClassification
}

// NovelOutcome is the default "create" decision for a candidate with no
// deterministic match.
func NovelOutcome() Outcome {
	return Outcome{Action: domain.ConsolidationCreate, ReasonCode: "novel", Classification: domain.ConsolidationNone}
}

// ClassifyDeterministic applies the deterministic exact rules first. Semantic
// similarity can only reinforce a deterministic tuple or shared-evidence match;
// cosine alone never supersedes, contests, deletes, or attaches.
func ClassifyDeterministic(rules Rules, candidate domain.MemoryRevision, neighbor *Neighbor, sim Similarity) (domain.ConsolidationAction, string) {
	if neighbor == nil {
		return domain.ConsolidationCreate, "novel"
	}
	candidateHash := candidate.CanonicalHash
	if candidateHash == "" {
		candidateHash = CanonicalContentHash(candidate.CanonicalContent)
	}
	sameKind := candidate.Kind == neighbor.Kind
	sameSubjectKind := sameKind && EqualStringSets(candidate.SubjectEntityIDs, neighbor.Subjects)
	sameEvent := sharesEvent(candidate.Evidence, neighbor)
	attrConflict := AttributesConflict(candidate.Attributes, neighbor.Attributes)
	timeCompatible := validTimeCompatible(candidate, neighbor)
	timeOrdered := validTimeOrdered(candidate, neighbor)

	if rules.ExactHashEnabled && candidateHash != "" && candidateHash == neighbor.CanonicalHash && sameKind {
		if attrConflict {
			return domain.ConsolidationRefine, "exact_hash_attribute_delta"
		}
		if evidenceSubset(candidate.Evidence, neighbor) {
			return domain.ConsolidationReject, "redundant_evidence"
		}
		return domain.ConsolidationAttachEvidence, "exact_canonical_hash"
	}

	if sameSubjectKind && attrConflict {
		switch {
		case timeOrdered:
			return domain.ConsolidationSupersede, "ordered_attribute_conflict"
		case timeCompatible:
			return domain.ConsolidationContest, "overlapping_attribute_conflict"
		default:
			return domain.ConsolidationCreate, "disjoint_attribute_conflict"
		}
	}

	if sameEvent && sim.OK && sim.Cosine >= rules.SameSourceEventCosine {
		if attrConflict {
			return domain.ConsolidationCreate, "cosine_conflict_no_merge"
		}
		if evidenceSubset(candidate.Evidence, neighbor) {
			return domain.ConsolidationReject, "redundant_evidence"
		}
		return domain.ConsolidationAttachEvidence, "same_source_event"
	}

	if sameSubjectKind && timeCompatible && sim.OK && sim.Cosine >= rules.SameSubjectKindCosine {
		if attrConflict {
			return domain.ConsolidationCreate, "cosine_conflict_no_merge"
		}
		if evidenceSubset(candidate.Evidence, neighbor) {
			return domain.ConsolidationReject, "redundant_evidence"
		}
		if morePrecise(candidate, neighbor) {
			return domain.ConsolidationRefine, "more_precise"
		}
		return domain.ConsolidationAttachEvidence, "subject_kind_semantic"
	}

	return domain.ConsolidationCreate, "novel"
}

// ConflictDetected reports whether a candidate has a deterministic attribute
// conflict with any neighbor under the same kind and subject set. Destructive
// provider actions require this independently detected conflict; cosine alone
// never authorizes them.
func ConflictDetected(candidate domain.MemoryRevision, neighbors []Neighbor) bool {
	for i := range neighbors {
		if candidate.Kind == neighbors[i].Kind &&
			EqualStringSets(candidate.SubjectEntityIDs, neighbors[i].Subjects) &&
			AttributesConflict(candidate.Attributes, neighbors[i].Attributes) {
			return true
		}
	}
	return false
}

// ValidateProviderProposal accepts a provider proposal only when the
// deterministic rules left the candidate unresolved. Destructive actions
// require an independently detected deterministic conflict.
func ValidateProviderProposal(deterministic Outcome, proposal Classification, conflictDetected bool) (Outcome, bool) {
	if deterministic.Action != domain.ConsolidationCreate {
		return Outcome{}, false
	}
	if !proposal.Action.Valid() || proposal.ReasonCode == "" {
		return Outcome{}, false
	}
	switch proposal.Action {
	case domain.ConsolidationSupersede, domain.ConsolidationContest:
		if !conflictDetected {
			return Outcome{}, false
		}
	}
	return Outcome{
		Action:           proposal.Action,
		ReasonCode:       "provider_" + proposal.ReasonCode,
		TargetMemoryID:   proposal.MemoryID,
		TargetRevisionID: proposal.RevisionID,
		Similarity:       proposal.Similarity,
		Classification:   domain.ConsolidationProvider,
	}, true
}

// CanonicalContentHash returns the stable canonical hash of revision content.
func CanonicalContentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

// EqualStringSets reports whether two string slices contain the same values,
// ignoring order.
func EqualStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// AttributesConflict reports whether two attribute maps disagree on a shared key.
func AttributesConflict(a, b map[string]string) bool {
	for key, value := range a {
		if other, ok := b[key]; ok && other != value {
			return true
		}
	}
	return false
}

// IntervalsOverlap reports whether two optional valid-time intervals overlap. A
// missing bound is open-ended.
func IntervalsOverlap(aStart, aEnd, bStart, bEnd *time.Time) bool {
	if aStart != nil && bEnd != nil && !aStart.Before(*bEnd) {
		return false
	}
	if bStart != nil && aEnd != nil && !bStart.Before(*aEnd) {
		return false
	}
	return true
}

func sharesEvent(evidence []domain.RevisionEvidence, neighbor *Neighbor) bool {
	if len(evidence) == 0 || len(neighbor.EvidenceRanges) == 0 {
		return false
	}
	for _, item := range evidence {
		for _, existing := range neighbor.EvidenceRanges {
			if existing.EventID == item.EventID {
				return true
			}
		}
	}
	return false
}

func evidenceSubset(evidence []domain.RevisionEvidence, neighbor *Neighbor) bool {
	if len(evidence) == 0 {
		return false
	}
	for _, item := range evidence {
		found := false
		for _, existing := range neighbor.EvidenceRanges {
			if existing.EventID == item.EventID && equalOptionalInt64(existing.StartOffset, item.StartOffset) && equalOptionalInt64(existing.EndOffset, item.EndOffset) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func equalOptionalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func validTimeCompatible(candidate domain.MemoryRevision, neighbor *Neighbor) bool {
	return IntervalsOverlap(candidate.ValidFrom, candidate.ValidUntil, neighbor.ValidFrom, neighbor.ValidUntil)
}

func validTimeOrdered(candidate domain.MemoryRevision, neighbor *Neighbor) bool {
	if candidate.ValidFrom != nil && neighbor.ValidUntil != nil && candidate.ValidFrom.After(*neighbor.ValidUntil) {
		return true
	}
	if neighbor.ValidFrom != nil && candidate.ValidUntil != nil && neighbor.ValidFrom.After(*candidate.ValidUntil) {
		return true
	}
	return false
}

func morePrecise(candidate domain.MemoryRevision, neighbor *Neighbor) bool {
	if len(candidate.Attributes) > len(neighbor.Attributes) {
		return true
	}
	if candidate.Specificity > neighbor.Specificity {
		return true
	}
	return TimePrecisionRank(candidate.TimePrecision) > TimePrecisionRank(neighbor.Precision)
}

// TimePrecisionRank orders valid-time precision from most to least specific.
func TimePrecisionRank(precision domain.TimePrecision) int {
	switch precision {
	case domain.TimePrecisionExact:
		return 5
	case domain.TimePrecisionDay:
		return 4
	case domain.TimePrecisionMonth:
		return 3
	case domain.TimePrecisionYear:
		return 2
	case domain.TimePrecisionApproximate:
		return 1
	default:
		return 0
	}
}
