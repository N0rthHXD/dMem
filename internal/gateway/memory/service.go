package memory

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// ServiceImpl is the default protocol-independent Memory service. It owns
// semantic validation, protocol-neutral defaults, scope checks, idempotency
// identity, and stable error mapping, and delegates atomic persistence to the
// AuthorityStore port.
type ServiceImpl struct {
	authority AuthorityStore
	now       func() time.Time
}

// NewService wires the Memory service over an authority port.
func NewService(authority AuthorityStore) *ServiceImpl {
	return &ServiceImpl{authority: authority, now: time.Now}
}

// Remember validates the command, applies protocol-neutral defaults, derives the
// idempotent write identity from the caller-supplied command, and atomically
// persists the memory, its audit record, and any derived-work jobs.
func (s *ServiceImpl) Remember(ctx context.Context, call gateway.CallContext, command RememberCommand) (RememberResult, error) {
	prepared := s.applyDefaults(command, call)
	if err := validateRemember(prepared); err != nil {
		return RememberResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationRemember, command)
	if err != nil {
		return RememberResult{}, gateway.WriteIdentityError(err)
	}
	record, replay, err := s.authority.Remember(ctx, identity, rememberParams(prepared))
	if err != nil {
		return RememberResult{}, normalizeError(err)
	}
	return RememberResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay},
		Memory:      record.Memory,
		EventID:     record.EventID,
	}, nil
}

// Get reads one current memory projection by identifier.
func (s *ServiceImpl) Get(ctx context.Context, call gateway.CallContext, query GetQuery) (domain.Memory, error) {
	if strings.TrimSpace(query.MemoryID) == "" {
		return domain.Memory{}, gateway.NewError(gateway.ErrorInvalidInput, "memory_id is required")
	}
	memory, err := s.authority.GetMemory(ctx, query.MemoryID)
	if err != nil {
		return domain.Memory{}, normalizeError(err)
	}
	return memory, nil
}

// Forget marks one memory as forgotten with an auditable reason.
func (s *ServiceImpl) Forget(ctx context.Context, call gateway.CallContext, command ForgetCommand) (ForgetResult, error) {
	if strings.TrimSpace(command.MemoryID) == "" || utf8.RuneCountInString(command.Reason) < 1 || utf8.RuneCountInString(command.Reason) > 1024 {
		return ForgetResult{}, gateway.NewError(gateway.ErrorInvalidInput, "memory_id and a reason of at most 1024 characters are required")
	}
	identity, err := gateway.NewWriteIdentity(call, OperationForget, command)
	if err != nil {
		return ForgetResult{}, gateway.WriteIdentityError(err)
	}
	memory, replay, err := s.authority.ForgetMemory(ctx, identity, command.MemoryID, command.Reason)
	if err != nil {
		return ForgetResult{}, normalizeError(err)
	}
	return ForgetResult{
		WriteResult: gateway.WriteResult{OperationID: call.RequestID, Replay: replay},
		Memory:      memory,
	}, nil
}

func (s *ServiceImpl) applyDefaults(command RememberCommand, call gateway.CallContext) RememberCommand {
	if command.Kind == "" {
		command.Kind = DefaultKind
	}
	if command.Sensitivity == "" {
		command.Sensitivity = DefaultSensitivity
	}
	if command.SourceName == "" {
		command.SourceName = call.CallerID()
	}
	if command.OccurredAt == "" {
		command.OccurredAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	if command.Metadata == nil {
		command.Metadata = map[string]any{}
	}
	if command.Reason == "" {
		command.Reason = DefaultReason
	}
	return command
}

func rememberParams(command RememberCommand) RememberParams {
	return RememberParams{
		SpaceID:         command.SpaceID,
		Content:         command.Content,
		Kind:            command.Kind,
		Sensitivity:     command.Sensitivity,
		ValidFrom:       command.ValidFrom,
		ValidUntil:      command.ValidUntil,
		SourceName:      command.SourceName,
		SourceEventID:   command.SourceEventID,
		SourceStreamKey: command.SourceStreamKey,
		Sequence:        command.Sequence,
		OccurredAt:      command.OccurredAt,
		Metadata:        command.Metadata,
		Reason:          command.Reason,
	}
}

func validateRemember(command RememberCommand) error {
	if command.SpaceID == "" || strings.TrimSpace(command.Content) == "" {
		return errors.New("space_id and content are required")
	}
	kind := domain.MemoryKind(command.Kind)
	if !kind.Valid() || domain.ValidAggregateKind(command.Kind) || command.Sensitivity != "normal" && command.Sensitivity != "sensitive" {
		return errors.New("kind or sensitivity is invalid")
	}
	if command.SourceName == "" || utf8.RuneCountInString(command.SourceName) > 128 || utf8.RuneCountInString(command.Reason) < 1 || utf8.RuneCountInString(command.Reason) > 1024 {
		return errors.New("source_name or reason is invalid")
	}
	if _, err := time.Parse(time.RFC3339, command.OccurredAt); err != nil {
		return errors.New("occurred_at must be RFC3339")
	}
	if command.Sequence != nil && *command.Sequence < 0 {
		return errors.New("sequence must be a non-negative integer")
	}
	if utf8.RuneCountInString(command.SourceStreamKey) > 256 {
		return errors.New("source_stream_key is too long")
	}
	return validateTimeRange(command.ValidFrom, command.ValidUntil)
}

func validateTimeRange(from, until *string) error {
	var start, end *time.Time
	for value, target := range map[*string]**time.Time{from: &start, until: &end} {
		if value == nil {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, *value)
		if err != nil {
			return errors.New("valid time must be RFC3339")
		}
		*target = &parsed
	}
	if start != nil && end != nil && !end.After(*start) {
		return errors.New("valid_until must be after valid_from")
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
	return gateway.WrapError(gateway.ErrorInternal, "memory operation failed", err)
}
