package feedback

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// ServiceImpl is the default protocol-independent Feedback service. It owns
// purpose/type/reason combination validation, decision-context rules, optional
// corrective-proposal rules, idempotency identity, and stable error mapping, and
// delegates persistence and aggregate scheduling to the Store port.
type ServiceImpl struct {
	store Store
}

// NewService wires the Feedback service over a feedback store port.
func NewService(store Store) *ServiceImpl {
	return &ServiceImpl{store: store}
}

// Append validates the feedback combination and decision context, derives the
// idempotent write identity, and atomically appends the feedback event while
// scheduling aggregate work against the current revision.
func (s *ServiceImpl) Append(ctx context.Context, call gateway.CallContext, command AppendCommand) (AppendResult, error) {
	if err := validateAppend(command); err != nil {
		return AppendResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationAppend, command)
	if err != nil {
		return AppendResult{}, gateway.WriteIdentityError(err)
	}
	response, replay, err := s.store.AppendFeedback(ctx, identity, AppendParams{
		SpaceID:         command.SpaceID,
		MemoryID:        command.MemoryID,
		Purpose:         command.Purpose,
		FeedbackType:    command.FeedbackType,
		ReasonCode:      command.ReasonCode,
		Note:            command.Note,
		Decision:        command.Decision,
		CreateProposal:  command.CreateProposal,
		SourceEventID:   command.SourceEventID,
		ProposedContent: command.ProposedContent,
	})
	if err != nil {
		return AppendResult{}, normalizeError(err)
	}
	return AppendResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay},
		Response:    response,
	}, nil
}

// Aggregate returns the aggregate projection health.
func (s *ServiceImpl) Aggregate(ctx context.Context, call gateway.CallContext, query AggregateQuery) (AggregateResult, error) {
	health, err := s.store.FeedbackAggregateHealth(ctx)
	if err != nil {
		return AggregateResult{}, normalizeError(err)
	}
	return AggregateResult{Health: health}, nil
}

func validateAppend(command AppendCommand) error {
	if command.SpaceID == "" || command.MemoryID == "" || !domain.ValidFeedbackCombination(command.Purpose, command.FeedbackType, command.ReasonCode) {
		return errors.New("feedback purpose, target, type, or reason is invalid")
	}
	if command.Note != nil && utf8.RuneCountInString(*command.Note) > 2000 {
		return errors.New("feedback note is too long")
	}
	if command.CreateProposal && (!domain.CanProposeEvolution(command.Purpose, command.FeedbackType) || command.SourceEventID == "" || strings.TrimSpace(command.ProposedContent) == "") {
		return errors.New("feedback proposal requires a corrective memory_quality type, source_event_id, and proposed_content")
	}
	if !domain.FeedbackPurposeRequiresDecision(command.Purpose) {
		if command.Decision != nil {
			return errors.New("memory_quality_feedback must not carry retrieval decision context")
		}
		return nil
	}
	if command.Decision == nil {
		return errors.New("retrieval and answer feedback require a decision context")
	}
	decision := command.Decision
	if decision.SearchOperationID == "" || len(decision.QueryHash) != 64 || decision.QueryType == "" || decision.RankingIdentity == "" || decision.FeatureIdentity == "" || decision.ContextIdentity == "" || decision.ResultRank < 1 || (decision.SelectionState != "selected" && decision.SelectionState != "rejected") || len(decision.MatchedSignals) == 0 {
		return errors.New("feedback decision context is invalid")
	}
	if command.Purpose == domain.FeedbackPurposeAnswer && decision.AnswerIdentity == "" {
		return errors.New("answer feedback requires an answer_identity")
	}
	return nil
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, "feedback operation failed", err)
}
