package agenttool

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

type ServiceImpl struct {
	store      Store
	recall     RecallPort
	authorizer Authorizer
}

var _ Service = (*ServiceImpl)(nil)

func NewService(store Store, recall RecallPort, authorizer Authorizer) *ServiceImpl {
	return &ServiceImpl{store: store, recall: recall, authorizer: authorizer}
}

func (s *ServiceImpl) Recall(ctx context.Context, call gateway.CallContext, command RecallCommand) (RecallResult, error) {
	if err := ValidateRecall(command); err != nil {
		return RecallResult{}, err
	}
	operationID := call.RequestID
	if operationID == "" {
		hash, err := CanonicalRecallHash(command)
		if err != nil {
			return RecallResult{}, gateway.WrapError(gateway.ErrorInvalidInput, "recall command could not be canonically encoded", err)
		}
		operationID = string(OperationRecall) + ":" + hash
	}
	limit := command.Limit
	if limit == 0 {
		limit = DefaultRecallLimit
	}
	intent := command.Intent
	if intent == "" {
		intent = domain.RecallIntentAuto
	}
	if s.recall == nil {
		return RecallResult{}, gateway.NewError(gateway.ErrorCapabilityUnavailable, "recall is unavailable")
	}
	record, err := s.recall.Recall(ctx, call, RecallParams{
		OperationID: operationID, Query: command.Query, ScopeIDs: append([]string(nil), command.ScopeIDs...),
		Intent: intent, EntityHints: append([]string(nil), command.EntityHints...),
		ReferenceTime: utcTime(command.ReferenceTime), Limit: limit, Cursor: command.Cursor, IncludeEvidence: true,
	})
	if err != nil {
		return RecallResult{}, normalizeServiceError("recall failed", err)
	}
	if record.OperationID != "" && record.OperationID != operationID {
		return RecallResult{}, gateway.NewError(gateway.ErrorInternal, "recall returned a mismatched operation identity")
	}
	result := RecallResult{OperationID: operationID, NextCursor: record.NextCursor, DegradedCapabilities: degradedCapabilities(record)}
	result.Memories = make([]RecalledMemory, len(record.Memories))
	for i, memory := range record.Memories {
		result.Memories[i] = RecalledMemory{
			MemoryID: memory.MemoryID, RevisionID: memory.RevisionID, Kind: memory.Kind, Content: memory.Content,
			ValidFrom: memory.ValidFrom, ValidUntil: memory.ValidUntil, Confidence: memory.Confidence,
			Evidence: evidenceReferences(memory.Evidence),
		}
	}
	return result, nil
}

func (s *ServiceImpl) MemoryCommit(ctx context.Context, call gateway.CallContext, command MemoryCommitCommand) (MemoryCommitResult, error) {
	identity, err := NewMemoryCommitWriteIdentity(call, command)
	if err != nil {
		return MemoryCommitResult{}, err
	}
	if err := s.authorizeWrite(ctx, call, command.ScopeID); err != nil {
		return MemoryCommitResult{}, err
	}
	if s.store == nil {
		return MemoryCommitResult{}, gateway.NewError(gateway.ErrorCapabilityUnavailable, "memory commit is unavailable")
	}
	record, replay, err := s.store.CommitMemory(ctx, identity, CommitParams{
		ScopeID: command.ScopeID, Source: command.Source, Memories: command.Memories,
		Entities: command.Entities, Facts: command.Facts, Relations: command.Relations,
	})
	if err != nil {
		return MemoryCommitResult{}, normalizeServiceError("memory commit failed", err)
	}
	return MemoryCommitResult{
		WriteResult: gateway.WriteResult{OperationID: record.OperationID, Replay: replay}, SourceEventID: record.SourceEventID,
		Memories: record.Memories, Entities: record.Entities, Facts: record.Facts, Relations: record.Relations,
	}, nil
}

func (s *ServiceImpl) Feedback(ctx context.Context, call gateway.CallContext, command FeedbackCommand) (FeedbackResult, error) {
	identity, err := NewFeedbackWriteIdentity(call, command)
	if err != nil {
		return FeedbackResult{}, err
	}
	if err := s.authorizeWrite(ctx, call, command.ScopeID); err != nil {
		return FeedbackResult{}, err
	}
	if s.store == nil {
		return FeedbackResult{}, gateway.NewError(gateway.ErrorCapabilityUnavailable, "feedback is unavailable")
	}
	record, replay, err := s.store.AppendFeedback(ctx, identity, FeedbackParams(command))
	if err != nil {
		return FeedbackResult{}, normalizeServiceError("feedback failed", err)
	}
	return FeedbackResult{WriteResult: gateway.WriteResult{OperationID: record.OperationID, Replay: replay}, FeedbackID: record.FeedbackID, MemoryID: record.MemoryID, RevisionID: record.RevisionID, Kind: record.Kind}, nil
}

func (s *ServiceImpl) Forget(ctx context.Context, call gateway.CallContext, command ForgetCommand) (ForgetResult, error) {
	identity, err := NewForgetWriteIdentity(call, command)
	if err != nil {
		return ForgetResult{}, err
	}
	if err := s.authorizeWrite(ctx, call, command.ScopeID); err != nil {
		return ForgetResult{}, err
	}
	if s.store == nil {
		return ForgetResult{}, gateway.NewError(gateway.ErrorCapabilityUnavailable, "forget is unavailable")
	}
	record, replay, err := s.store.ForgetMemory(ctx, identity, ForgetParams(command))
	if err != nil {
		return ForgetResult{}, normalizeServiceError("forget failed", err)
	}
	return ForgetResult{WriteResult: gateway.WriteResult{OperationID: record.OperationID, Replay: replay}, MemoryID: record.MemoryID, RevisionID: record.RevisionID, State: record.State}, nil
}

func (s *ServiceImpl) authorizeWrite(ctx context.Context, call gateway.CallContext, scopeID string) error {
	if s.authorizer == nil {
		return gateway.NewError(gateway.ErrorPermissionDenied, "authority write authorization is unavailable")
	}
	decision, err := s.authorizer.Authorize(ctx, call, AuthorizationRequest{Capability: CapabilityAuthorityWrite, ScopeIDs: []string{scopeID}})
	if err != nil {
		return normalizeServiceError("authority write authorization failed", err)
	}
	if !decision.Allowed {
		return gateway.NewError(gateway.ErrorPermissionDenied, "authority write denied")
	}
	return nil
}

func evidenceReferences(evidence []domain.RevisionEvidence) []EvidenceReference {
	if len(evidence) > MaxEvidencePerMemory {
		evidence = evidence[:MaxEvidencePerMemory]
	}
	result := make([]EvidenceReference, len(evidence))
	for i, item := range evidence {
		result[i] = EvidenceReference{
			EvidenceID: item.EvidenceID, EventID: item.EventID, Role: item.Role, EventTime: item.EventTime,
			EventSequence: item.EventSequence, StartOffset: item.StartOffset, EndOffset: item.EndOffset,
		}
	}
	return result
}

func degradedCapabilities(record RecallRecord) []Capability {
	requested := make(map[Capability]bool, len(record.RequestedCapabilities))
	executed := make(map[Capability]bool, len(record.ExecutedCapabilities))
	degraded := make(map[Capability]bool)
	for _, capability := range record.RequestedCapabilities {
		requested[capability] = true
	}
	for _, capability := range record.ExecutedCapabilities {
		executed[capability] = true
	}
	for _, reason := range record.DegradationReasons {
		for _, capability := range []Capability{CapabilityKeyword, CapabilityGraph, CapabilitySemantic} {
			if strings.HasPrefix(reason, string(capability)+"_") || reason == string(capability) {
				degraded[capability] = true
			}
		}
	}
	result := make([]Capability, 0, len(requested)+len(degraded))
	for capability := range requested {
		if !executed[capability] {
			degraded[capability] = true
		}
	}
	for capability := range degraded {
		result = append(result, capability)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func normalizeServiceError(message string, err error) error {
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return gateway.WrapError(gateway.ErrorInternal, message, err)
}
