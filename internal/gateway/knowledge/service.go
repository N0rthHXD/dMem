package knowledge

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

var (
	validRelationDirections = map[string]bool{"": true, "incoming": true, "outgoing": true, "both": true}
	validFactStates         = map[string]bool{"": true, "active": true, "dismissed": true, "superseded": true}
	validConflictStates     = map[string]bool{"": true, "open": true, "resolved": true, "dismissed": true}
	validResolutionTypes    = map[string]bool{"keep_both": true, "dismiss": true, "revise_one": true, "supersede": true, "reconcile": true}
	validReplacementModes   = map[string]bool{"same_memory_revision": true, "new_memory": true}
)

// ReadServiceImpl is the protocol-independent Knowledge read service. It owns
// request validation, protocol-neutral paging defaults, scope visibility checks,
// and stable error mapping, and delegates SQL and FTS to the ReadStore port.
type ReadServiceImpl struct {
	store ReadStore
}

// NewReadService wires the Knowledge read service over a read-only store port.
func NewReadService(store ReadStore) *ReadServiceImpl {
	return &ReadServiceImpl{store: store}
}

// GetEntity reads one entity and enforces the requested scope when present.
func (s *ReadServiceImpl) GetEntity(ctx context.Context, call gateway.CallContext, query GetEntityQuery) (domain.Entity, error) {
	if strings.TrimSpace(query.EntityID) == "" {
		return domain.Entity{}, gateway.NewError(gateway.ErrorInvalidInput, "entity_id is required")
	}
	entity, err := s.store.GetEntity(ctx, query)
	if err != nil {
		return domain.Entity{}, normalizeError(err)
	}
	if query.SpaceID != "" && entity.SpaceID != query.SpaceID {
		return domain.Entity{}, gateway.NewError(gateway.ErrorNotFound, "entity not found in scope")
	}
	return entity, nil
}

// ListEntities lists entities in one required scope.
func (s *ReadServiceImpl) ListEntities(ctx context.Context, call gateway.CallContext, query ListEntitiesQuery) (domain.EntityPage, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return domain.EntityPage{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	if err := query.Page.Validate(); err != nil {
		return domain.EntityPage{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	query.Page.Limit = query.Page.EffectiveLimit()
	page, err := s.store.ListEntities(ctx, query)
	if err != nil {
		return domain.EntityPage{}, normalizeError(err)
	}
	return page, nil
}

// GetRelation reads one relation, optionally bound to a scope.
func (s *ReadServiceImpl) GetRelation(ctx context.Context, call gateway.CallContext, query GetRelationQuery) (domain.MemoryRelation, error) {
	if strings.TrimSpace(query.RelationID) == "" {
		return domain.MemoryRelation{}, gateway.NewError(gateway.ErrorInvalidInput, "relation_id is required")
	}
	relation, err := s.store.GetRelation(ctx, query)
	if err != nil {
		return domain.MemoryRelation{}, normalizeError(err)
	}
	if query.SpaceID != "" && relation.SpaceID != query.SpaceID {
		return domain.MemoryRelation{}, gateway.NewError(gateway.ErrorNotFound, "relation not found in scope")
	}
	return relation, nil
}

// ListRelations lists relations in one required scope.
func (s *ReadServiceImpl) ListRelations(ctx context.Context, call gateway.CallContext, query ListRelationsQuery) (domain.RelationPage, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return domain.RelationPage{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	if !validRelationDirections[query.Direction] {
		return domain.RelationPage{}, gateway.NewError(gateway.ErrorInvalidInput, "direction is invalid")
	}
	if err := query.Page.Validate(); err != nil {
		return domain.RelationPage{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	query.Page.Limit = query.Page.EffectiveLimit()
	page, err := s.store.ListRelations(ctx, query)
	if err != nil {
		return domain.RelationPage{}, normalizeError(err)
	}
	return page, nil
}

// GetEntityFact reads one entity fact and enforces the requested scope when
// present.
func (s *ReadServiceImpl) GetEntityFact(ctx context.Context, call gateway.CallContext, query GetEntityFactQuery) (domain.EntityFact, error) {
	if strings.TrimSpace(query.FactID) == "" {
		return domain.EntityFact{}, gateway.NewError(gateway.ErrorInvalidInput, "fact_id is required")
	}
	fact, err := s.store.GetEntityFact(ctx, query)
	if err != nil {
		return domain.EntityFact{}, normalizeError(err)
	}
	if query.SpaceID != "" && fact.SpaceID != query.SpaceID {
		return domain.EntityFact{}, gateway.NewError(gateway.ErrorNotFound, "fact not found in scope")
	}
	return fact, nil
}

// ListEntityFacts lists entity facts in one required scope.
func (s *ReadServiceImpl) ListEntityFacts(ctx context.Context, call gateway.CallContext, query ListEntityFactsQuery) (domain.EntityFactPage, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return domain.EntityFactPage{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	if !validFactStates[query.State] {
		return domain.EntityFactPage{}, gateway.NewError(gateway.ErrorInvalidInput, "state is invalid")
	}
	if !validRelationDirections[query.Direction] {
		return domain.EntityFactPage{}, gateway.NewError(gateway.ErrorInvalidInput, "direction is invalid")
	}
	if err := query.Page.Validate(); err != nil {
		return domain.EntityFactPage{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	if query.ReferenceTime != "" {
		parsed, err := time.Parse(time.RFC3339, query.ReferenceTime)
		if err != nil {
			return domain.EntityFactPage{}, gateway.NewError(gateway.ErrorInvalidInput, "reference_time must be RFC3339")
		}
		query.ReferenceTime = parsed.UTC().Format(time.RFC3339Nano)
	}
	query.Page.Limit = query.Page.EffectiveLimit()
	page, err := s.store.ListEntityFacts(ctx, query)
	if err != nil {
		return domain.EntityFactPage{}, normalizeError(err)
	}
	return page, nil
}

// GetConflictCase reads one conflict case.
func (s *ReadServiceImpl) GetConflictCase(ctx context.Context, call gateway.CallContext, query GetConflictCaseQuery) (domain.ConflictCase, error) {
	if strings.TrimSpace(query.CaseID) == "" {
		return domain.ConflictCase{}, gateway.NewError(gateway.ErrorInvalidInput, "case_id is required")
	}
	found, err := s.store.GetConflictCase(ctx, query)
	if err != nil {
		return domain.ConflictCase{}, normalizeError(err)
	}
	return found, nil
}

// ListConflictCases lists conflict cases in one required scope.
func (s *ReadServiceImpl) ListConflictCases(ctx context.Context, call gateway.CallContext, query ListConflictCasesQuery) (domain.ConflictCasePage, error) {
	if strings.TrimSpace(query.SpaceID) == "" {
		return domain.ConflictCasePage{}, gateway.NewError(gateway.ErrorInvalidInput, "space_id is required")
	}
	if !validConflictStates[query.State] {
		return domain.ConflictCasePage{}, gateway.NewError(gateway.ErrorInvalidInput, "state is invalid")
	}
	if err := query.Page.Validate(); err != nil {
		return domain.ConflictCasePage{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	query.Page.Limit = query.Page.EffectiveLimit()
	page, err := s.store.ListConflictCases(ctx, query)
	if err != nil {
		return domain.ConflictCasePage{}, normalizeError(err)
	}
	return page, nil
}

// ReviewServiceImpl is the protocol-independent proposal review and
// conflict-resolution service. It is a separate surface from the read service so
// a protocol can expose graph reads without gaining mutation authority, and it
// is never exposed through the initial Agent HTTP API.
type ReviewServiceImpl struct {
	store ReviewStore
}

// NewReviewService wires the Knowledge review service over a review store port.
func NewReviewService(store ReviewStore) *ReviewServiceImpl {
	return &ReviewServiceImpl{store: store}
}

// ReviewEntityProposal confirms or rejects one entity proposal.
func (s *ReviewServiceImpl) ReviewEntityProposal(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand) (ReviewEntityResult, error) {
	if err := validateProposalReview(command); err != nil {
		return ReviewEntityResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationReviewEntityProposal, command)
	if err != nil {
		return ReviewEntityResult{}, gateway.WriteIdentityError(err)
	}
	proposal, replay, err := s.store.ReviewEntityProposal(ctx, identity, command.ProposalID, reviewParams(command))
	if err != nil {
		return ReviewEntityResult{}, normalizeError(err)
	}
	return ReviewEntityResult{WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay}, Proposal: proposal}, nil
}

// ReviewRelationProposal confirms or rejects one relation proposal.
func (s *ReviewServiceImpl) ReviewRelationProposal(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand) (ReviewRelationResult, error) {
	if err := validateProposalReview(command); err != nil {
		return ReviewRelationResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationReviewRelationProposal, command)
	if err != nil {
		return ReviewRelationResult{}, gateway.WriteIdentityError(err)
	}
	proposal, replay, err := s.store.ReviewRelationProposal(ctx, identity, command.ProposalID, reviewParams(command))
	if err != nil {
		return ReviewRelationResult{}, normalizeError(err)
	}
	return ReviewRelationResult{WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay}, Proposal: proposal}, nil
}

// ReviewFactProposal confirms or rejects one fact proposal.
func (s *ReviewServiceImpl) ReviewFactProposal(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand) (ReviewFactResult, error) {
	if err := validateProposalReview(command); err != nil {
		return ReviewFactResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationReviewFactProposal, command)
	if err != nil {
		return ReviewFactResult{}, gateway.WriteIdentityError(err)
	}
	proposal, replay, err := s.store.ReviewFactProposal(ctx, identity, command.ProposalID, reviewParams(command))
	if err != nil {
		return ReviewFactResult{}, normalizeError(err)
	}
	return ReviewFactResult{WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay}, Proposal: proposal}, nil
}

// ReviewEvolutionProposal confirms or rejects one evolution proposal.
func (s *ReviewServiceImpl) ReviewEvolutionProposal(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand) (ReviewEvolutionResult, error) {
	if err := validateProposalReview(command); err != nil {
		return ReviewEvolutionResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationReviewEvolutionProposal, command)
	if err != nil {
		return ReviewEvolutionResult{}, gateway.WriteIdentityError(err)
	}
	proposal, replay, err := s.store.ReviewEvolutionProposal(ctx, identity, command.ProposalID, reviewParams(command))
	if err != nil {
		return ReviewEvolutionResult{}, normalizeError(err)
	}
	return ReviewEvolutionResult{WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay}, Proposal: proposal}, nil
}

// ReviewAggregateProposal confirms or rejects one aggregate proposal.
func (s *ReviewServiceImpl) ReviewAggregateProposal(ctx context.Context, call gateway.CallContext, command ReviewProposalCommand) (ReviewAggregateResult, error) {
	if err := validateProposalReview(command); err != nil {
		return ReviewAggregateResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationReviewAggregateProposal, command)
	if err != nil {
		return ReviewAggregateResult{}, gateway.WriteIdentityError(err)
	}
	proposal, replay, err := s.store.ReviewAggregateProposal(ctx, identity, command.ProposalID, reviewParams(command))
	if err != nil {
		return ReviewAggregateResult{}, normalizeError(err)
	}
	return ReviewAggregateResult{WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay}, Proposal: proposal}, nil
}

// ResolveConflict resolves one conflict case with an explicit, reviewed
// resolution command.
func (s *ReviewServiceImpl) ResolveConflict(ctx context.Context, call gateway.CallContext, command ResolveConflictCommand) (ResolveConflictResult, error) {
	prepared, err := validateResolveConflict(command)
	if err != nil {
		return ResolveConflictResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationResolveConflict, command)
	if err != nil {
		return ResolveConflictResult{}, gateway.WriteIdentityError(err)
	}
	resolved, replay, err := s.store.ResolveConflict(ctx, identity, command.CaseID, prepared.ConflictResolutionCommand)
	if err != nil {
		return ResolveConflictResult{}, normalizeError(err)
	}
	return ResolveConflictResult{WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay}, Case: resolved}, nil
}

func reviewParams(command ReviewProposalCommand) ReviewParams {
	return ReviewParams{Decision: command.Decision, ReasonCode: command.ReasonCode, Note: command.Note}
}

func validateProposalReview(command ReviewProposalCommand) error {
	if strings.TrimSpace(command.ProposalID) == "" {
		return errors.New("proposal_id is required")
	}
	if !command.Decision.Valid() {
		return errors.New("decision must be confirm or reject")
	}
	if utf8.RuneCountInString(command.ReasonCode) < 1 || utf8.RuneCountInString(command.ReasonCode) > 128 {
		return errors.New("reason_code length must be between 1 and 128")
	}
	if command.Note != nil && utf8.RuneCountInString(*command.Note) > 1024 {
		return errors.New("note is too long")
	}
	return nil
}

// validateResolveConflict applies the protocol-independent conflict-resolution
// rules and normalizes valid-time bounds. Participant-specific result-state
// validation remains with the atomic store because it needs the current case.
func validateResolveConflict(command ResolveConflictCommand) (ResolveConflictCommand, error) {
	if strings.TrimSpace(command.CaseID) == "" {
		return command, errors.New("case_id is required")
	}
	if !validResolutionTypes[command.ResolutionType] {
		return command, errors.New("resolution_type is unsupported")
	}
	if utf8.RuneCountInString(command.Reason) < 1 || utf8.RuneCountInString(command.Reason) > 1024 {
		return command, errors.New("reason length must be between 1 and 1024")
	}
	destructive := command.ResolutionType == "revise_one" || command.ResolutionType == "supersede" || command.ResolutionType == "reconcile"
	if !destructive {
		if command.SelectedTarget != nil || command.ProposedContent != nil || command.ValidFrom != nil || command.ValidUntil != nil || len(command.Evidence) != 0 || command.ReplacementMode != nil {
			return command, errors.New("resolution content is not valid for this resolution type")
		}
		return command, nil
	}
	if command.ProposedContent == nil || strings.TrimSpace(*command.ProposedContent) == "" {
		return command, errors.New("proposed_content is required")
	}
	from, until, err := normalizeValidTime(command.ValidFrom, command.ValidUntil)
	if err != nil {
		return command, err
	}
	command.ValidFrom, command.ValidUntil = from, until
	if len(command.Evidence) == 0 {
		return command, errors.New("evidence must contain at least one item")
	}
	for _, evidence := range command.Evidence {
		if evidence.EventID == "" || !evidence.Role.Valid() ||
			(evidence.StartOffset == nil) != (evidence.EndOffset == nil) ||
			evidence.StartOffset != nil && (*evidence.StartOffset < 0 || *evidence.EndOffset <= *evidence.StartOffset) {
			return command, errors.New("evidence is invalid")
		}
	}
	if command.ResolutionType == "reconcile" {
		if command.SelectedTarget != nil || command.ReplacementMode != nil {
			return command, errors.New("reconcile does not accept selected_target or replacement_mode")
		}
		return command, nil
	}
	if command.SelectedTarget == nil || command.SelectedTarget.MemoryID == "" || command.SelectedTarget.ExpectedRevisionID == "" {
		return command, errors.New("selected_target is required and must identify a participant revision")
	}
	if command.ResolutionType == "supersede" {
		if command.ReplacementMode == nil || !validReplacementModes[*command.ReplacementMode] {
			return command, errors.New("replacement_mode must be same_memory_revision or new_memory")
		}
	} else if command.ReplacementMode != nil {
		return command, errors.New("replacement_mode is only valid for supersede")
	}
	return command, nil
}

func normalizeValidTime(from, until *string) (*string, *string, error) {
	normalize := func(value *string) (*string, *time.Time, error) {
		if value == nil {
			return nil, nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, *value)
		if err != nil {
			return nil, nil, errors.New("valid time must be RFC3339")
		}
		formatted := parsed.UTC().Format(time.RFC3339Nano)
		return &formatted, &parsed, nil
	}
	start, startTime, err := normalize(from)
	if err != nil {
		return nil, nil, err
	}
	end, endTime, err := normalize(until)
	if err != nil {
		return nil, nil, err
	}
	if startTime != nil && endTime != nil && !endTime.After(*startTime) {
		return nil, nil, errors.New("valid_until must be after valid_from")
	}
	return start, end, nil
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, "knowledge operation failed", err)
}
