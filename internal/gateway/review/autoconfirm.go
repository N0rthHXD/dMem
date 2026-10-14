package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/N0rthHXD/dMem/internal/domain"
)

const (
	AutoConfirmReviewerID          = "system:auto-review-v1"
	AutoConfirmReasonCode          = "auto_review"
	DefaultAutoConfirmVersion      = "auto-review-v1"
	MaxAutoConfirmVersions         = 32
	MaxAutoConfirmsPerSpacePerHour = 10000
)

const (
	AutoConfirmEligible         = "auto_confirm_eligible"
	AutoConfirmDisabled         = "auto_confirm_disabled"
	AutoConfirmKindExcluded     = "auto_confirm_kind_excluded"
	AutoConfirmSensitivity      = "auto_confirm_sensitivity"
	AutoConfirmBelowConfidence  = "auto_confirm_below_confidence"
	AutoConfirmEvidenceRequired = "auto_confirm_missing_direct_evidence"
	AutoConfirmVersionExcluded  = "auto_confirm_processing_version"
	AutoConfirmActionExcluded   = "auto_confirm_action_not_publishable"
	AutoConfirmProviderOutcome  = "auto_confirm_provider_classified"
	AutoConfirmRateLimited      = "auto_confirm_rate_limited"
)

type AutoConfirmPolicy struct {
	Version            string
	MinConfidence      float64
	Kinds              []domain.MemoryKind
	ProcessingVersions []string
	MaxPerSpacePerHour int
}

func NewAutoConfirmPolicy(enabled bool, version string, minConfidence float64, kinds, processingVersions []string, maxPerSpacePerHour int) AutoConfirmPolicy {
	if !enabled {
		return AutoConfirmPolicy{}
	}
	if strings.TrimSpace(version) == "" {
		version = DefaultAutoConfirmVersion
	}
	policy := AutoConfirmPolicy{
		Version:            version,
		MinConfidence:      minConfidence,
		ProcessingVersions: normalizedVersions(processingVersions),
		MaxPerSpacePerHour: maxPerSpacePerHour,
	}
	seen := make(map[domain.MemoryKind]struct{}, len(kinds))
	for _, value := range kinds {
		kind := domain.MemoryKind(value)
		if !kind.Valid() || domain.ValidAggregateKind(value) {
			continue
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		policy.Kinds = append(policy.Kinds, kind)
	}
	sort.Slice(policy.Kinds, func(i, j int) bool { return policy.Kinds[i] < policy.Kinds[j] })
	return policy
}

func normalizedVersions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}

func (p AutoConfirmPolicy) Valid() bool {
	if strings.TrimSpace(p.Version) == "" {
		return false
	}
	if p.MinConfidence <= 0 || p.MinConfidence > 1 {
		return false
	}
	if p.MaxPerSpacePerHour <= 0 || p.MaxPerSpacePerHour > MaxAutoConfirmsPerSpacePerHour {
		return false
	}
	if len(p.Kinds) == 0 {
		return false
	}
	for _, kind := range p.Kinds {
		if !kind.Valid() || domain.ValidAggregateKind(string(kind)) {
			return false
		}
	}
	if len(p.ProcessingVersions) > MaxAutoConfirmVersions {
		return false
	}
	return true
}

type AutoConfirmDecision struct {
	Eligible   bool
	ReasonCode string
}

func (p AutoConfirmPolicy) Screen(candidate domain.Candidate, revision domain.MemoryRevision) AutoConfirmDecision {
	if !p.Valid() {
		return AutoConfirmDecision{ReasonCode: AutoConfirmDisabled}
	}
	if !p.allowsKind(revision.Kind) {
		return AutoConfirmDecision{ReasonCode: AutoConfirmKindExcluded}
	}
	if revision.Sensitivity != "normal" {
		return AutoConfirmDecision{ReasonCode: AutoConfirmSensitivity}
	}
	if revision.Confidence < p.MinConfidence {
		return AutoConfirmDecision{ReasonCode: AutoConfirmBelowConfidence}
	}
	if !hasDirectEvidence(candidate.Evidence) {
		return AutoConfirmDecision{ReasonCode: AutoConfirmEvidenceRequired}
	}
	if len(p.ProcessingVersions) > 0 && !containsString(p.ProcessingVersions, candidate.ProcessingVersion) {
		return AutoConfirmDecision{ReasonCode: AutoConfirmVersionExcluded}
	}
	return AutoConfirmDecision{Eligible: true, ReasonCode: AutoConfirmEligible}
}

func (p AutoConfirmPolicy) PublishesOutcome(action domain.ConsolidationAction, classification domain.ConsolidationClassification) (bool, string) {
	if classification == domain.ConsolidationProvider {
		return false, AutoConfirmProviderOutcome
	}
	switch action {
	case domain.ConsolidationCreate, domain.ConsolidationAttachEvidence:
		return true, AutoConfirmEligible
	default:
		return false, AutoConfirmActionExcluded
	}
}

func (p AutoConfirmPolicy) ReviewNote(revision domain.MemoryRevision, action domain.ConsolidationAction, similarity *float64) string {
	note := fmt.Sprintf("%s min_confidence=%.4f confidence=%.4f kind=%s action=%s",
		p.Version, p.MinConfidence, revision.Confidence, revision.Kind, action)
	if similarity != nil {
		note += fmt.Sprintf(" similarity=%.4f", *similarity)
	}
	return note
}

func (p AutoConfirmPolicy) allowsKind(kind domain.MemoryKind) bool {
	for _, allowed := range p.Kinds {
		if allowed == kind {
			return true
		}
	}
	return false
}

func hasDirectEvidence(items []domain.RevisionEvidence) bool {
	for _, item := range items {
		if item.Role == domain.EvidenceRoleDirect {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
