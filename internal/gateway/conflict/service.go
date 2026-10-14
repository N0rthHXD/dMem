package conflict

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// conflictStates is the closed set of case states a list query may filter on.
var conflictStates = map[string]bool{"open": true, "resolved": true, "dismissed": true}

// resultStates is the closed set of participant lifecycle outcomes a
// resolution may declare.
var resultStates = map[string]bool{"active": true, "archived": true, "contested": true, "superseded": true}

// replacementModes is the closed set of supersede replacement modes.
var replacementModes = map[string]bool{"same_memory_revision": true, "new_memory": true}

// ServiceImpl is the default conflict administration service. It owns request
// validation, protocol-neutral idempotent write identity, and stable error
// mapping; the case state machine, revision fence, and resolution history stay
// in storage.
type ServiceImpl struct {
	store Store
}

// NewService wires the conflict service over a store port.
func NewService(store Store) *ServiceImpl {
	return &ServiceImpl{store: store}
}

// List returns one bounded page of conflict cases for a scope.
func (s *ServiceImpl) List(ctx context.Context, _ gateway.CallContext, query ListQuery) (domain.ConflictCasePage, error) {
	if err := identifier("scope_id", query.SpaceID); err != nil {
		return domain.ConflictCasePage{}, invalidInput(err)
	}
	if query.State != "" && !conflictStates[query.State] {
		return domain.ConflictCasePage{}, invalidInput(errors.New("state is unsupported"))
	}
	if err := query.Page.Validate(); err != nil {
		return domain.ConflictCasePage{}, gateway.WrapError(gateway.ErrorInvalidInput, "page request is invalid", err)
	}
	page, err := s.store.ListConflictCases(ctx, ConflictFilter{
		SpaceID: query.SpaceID,
		State:   query.State,
		Limit:   query.Page.EffectiveLimit(),
		Cursor:  query.Page.Cursor,
	})
	if err != nil {
		return domain.ConflictCasePage{}, normalizeError(err)
	}
	if page.Items == nil {
		page.Items = []domain.ConflictCase{}
	}
	return page, nil
}

// Get reads one conflict case by identifier.
func (s *ServiceImpl) Get(ctx context.Context, _ gateway.CallContext, query GetQuery) (domain.ConflictCase, error) {
	if err := identifier("case_id", query.CaseID); err != nil {
		return domain.ConflictCase{}, invalidInput(err)
	}
	value, err := s.store.GetConflictCase(ctx, query.CaseID)
	if err != nil {
		return domain.ConflictCase{}, normalizeError(err)
	}
	return value, nil
}

// Resolve closes one open conflict case under the expected-participant fence.
func (s *ServiceImpl) Resolve(ctx context.Context, call gateway.CallContext, command ResolveCommand) (ResolveResult, error) {
	if err := validateResolve(command); err != nil {
		return ResolveResult{}, invalidInput(err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationResolve, command)
	if err != nil {
		return ResolveResult{}, gateway.WriteIdentityError(err)
	}
	resolved, operationID, replay, err := s.store.ResolveConflict(ctx, identity, ResolveParams{
		CaseID:          command.CaseID,
		ResolutionType:  string(command.ResolutionType),
		ExpectedTargets: command.ExpectedTargets,
		SelectedTarget:  command.SelectedTarget,
		ProposedContent: command.ProposedContent,
		ValidFrom:       command.ValidFrom,
		ValidUntil:      command.ValidUntil,
		Evidence:        evidenceParams(command.Evidence),
		ReplacementMode: command.ReplacementMode,
		ResultStates:    command.ResultStates,
		Reason:          command.Reason,
	})
	if err != nil {
		return ResolveResult{}, normalizeError(err)
	}
	return ResolveResult{WriteResult: gateway.WriteResult{OperationID: operationID, Replay: replay}, Case: resolved}, nil
}

func validateResolve(command ResolveCommand) error {
	if err := identifier("case_id", command.CaseID); err != nil {
		return err
	}
	if !command.ResolutionType.Valid() {
		return errors.New("resolution_type is unsupported")
	}
	if err := text("reason", command.Reason, MaxReasonRunes); err != nil {
		return err
	}
	if len(command.ExpectedTargets) < 1 || len(command.ExpectedTargets) > MaxTargets {
		return fmt.Errorf("expected_targets must contain between 1 and %d items", MaxTargets)
	}
	seen := map[string]bool{}
	for _, target := range command.ExpectedTargets {
		if err := identifiers(target.MemoryID, target.ExpectedRevisionID); err != nil {
			return err
		}
		if seen[target.MemoryID] {
			return errors.New("expected_targets contains duplicate memory ids")
		}
		seen[target.MemoryID] = true
	}
	if command.SelectedTarget != nil {
		if err := identifiers(command.SelectedTarget.MemoryID, command.SelectedTarget.ExpectedRevisionID); err != nil {
			return err
		}
	}
	if command.ProposedContent != nil {
		if err := text("proposed_content", *command.ProposedContent, MaxContentRunes); err != nil {
			return err
		}
	}
	if err := window(command.ValidFrom, command.ValidUntil); err != nil {
		return err
	}
	if command.ReplacementMode != nil && !replacementModes[*command.ReplacementMode] {
		return errors.New("replacement_mode is unsupported")
	}
	if err := validateEvidenceShape(command.Evidence); err != nil {
		return err
	}
	if err := validateResolutionShape(command); err != nil {
		return err
	}
	return validateResultStates(command.ResultStates)
}

// validateResolutionShape mirrors the storage contract for which resolution
// inputs are required or forbidden. Storage remains the authority for the
// participant set and exact revision fence.
func validateResolutionShape(command ResolveCommand) error {
	if !command.ResolutionType.Destructive() {
		if command.SelectedTarget != nil || command.ProposedContent != nil || command.ValidFrom != nil || command.ValidUntil != nil || len(command.Evidence) != 0 || command.ReplacementMode != nil {
			return errors.New("non-destructive resolution must not carry replacement inputs")
		}
		return nil
	}
	if command.ProposedContent == nil || len(command.Evidence) == 0 {
		return errors.New("destructive resolution requires proposed_content and evidence")
	}
	switch command.ResolutionType {
	case ResolutionReconcile:
		if command.SelectedTarget != nil || command.ReplacementMode != nil {
			return errors.New("reconcile must not select a target or replacement mode")
		}
	case ResolutionSupersede:
		if command.SelectedTarget == nil || command.ReplacementMode == nil {
			return errors.New("supersede requires selected_target and replacement_mode")
		}
	default:
		if command.SelectedTarget == nil || command.ReplacementMode != nil {
			return errors.New("revise_one requires selected_target and no replacement_mode")
		}
	}
	return nil
}

func validateResultStates(states map[string]string) error {
	if len(states) == 0 {
		return errors.New("result_states is required")
	}
	for memoryID, state := range states {
		if err := identifier("result_states key", memoryID); err != nil {
			return err
		}
		if !resultStates[state] {
			return errors.New("result_states value is unsupported")
		}
	}
	return nil
}

func validateEvidenceShape(evidence []Evidence) error {
	if len(evidence) > MaxEvidence {
		return fmt.Errorf("evidence must contain at most %d items", MaxEvidence)
	}
	for _, item := range evidence {
		if err := identifier("evidence.event_id", item.EventID); err != nil {
			return err
		}
		if item.Role != "" && !item.Role.Valid() {
			return errors.New("evidence role is unsupported")
		}
		if (item.StartOffset == nil) != (item.EndOffset == nil) {
			return errors.New("evidence span requires both start_offset and end_offset")
		}
		if item.StartOffset != nil && (*item.StartOffset < 0 || *item.EndOffset <= *item.StartOffset) {
			return errors.New("evidence span is invalid")
		}
	}
	return nil
}

func evidenceParams(evidence []Evidence) []EvidenceParams {
	result := make([]EvidenceParams, len(evidence))
	for i, item := range evidence {
		result[i] = EvidenceParams{EventID: item.EventID, Role: item.Role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return result
}

// window validates optional RFC3339 valid-time bounds and rejects a
// non-increasing interval.
func window(from, until *string) error {
	parsedFrom, err := parseOptionalTime(from)
	if err != nil {
		return errors.New("valid_from must be an RFC3339 timestamp")
	}
	parsedUntil, err := parseOptionalTime(until)
	if err != nil {
		return errors.New("valid_until must be an RFC3339 timestamp")
	}
	if parsedFrom != nil && parsedUntil != nil && !parsedUntil.After(*parsedFrom) {
		return errors.New("valid time window is invalid")
	}
	return nil
}

func parseOptionalTime(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// identifier requires a trimmed, non-empty, bounded opaque identifier.
func identifier(name, value string) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return errors.New(name + " must be valid UTF-8 without surrounding whitespace")
	}
	if length := utf8.RuneCountInString(value); length < 1 || length > MaxIdentifierRunes {
		return fmt.Errorf("%s length must be between 1 and %d", name, MaxIdentifierRunes)
	}
	return nil
}

func identifiers(values ...string) error {
	for _, value := range values {
		if err := identifier("identifier", value); err != nil {
			return err
		}
	}
	return nil
}

// text requires non-empty valid UTF-8 content within a rune bound.
func text(name, value string, max int) error {
	if !utf8.ValidString(value) {
		return errors.New(name + " must be valid UTF-8")
	}
	if length := utf8.RuneCountInString(value); length < 1 || length > max {
		return fmt.Errorf("%s length must be between 1 and %d", name, max)
	}
	return nil
}

func invalidInput(err error) error {
	return gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, "conflict operation failed", err)
}
