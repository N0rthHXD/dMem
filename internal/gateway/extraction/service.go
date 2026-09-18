package extraction

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway"
)

// sourceTypes is the protocol-independent set of accepted raw-event sources.
var sourceTypes = map[string]bool{
	"message": true, "conversation": true, "tool_call": true, "tool_result": true,
	"file_reference": true, "collector_event": true, "manual": true,
}

// ServiceImpl is the default protocol-independent Extraction ingress service.
// It owns semantic validation, protocol-neutral defaults, idempotent write
// identity, and stable error mapping, and delegates the atomic event, job,
// audit, and idempotency write to the EventStore port. Provider processing is
// never reachable from this inbound surface.
type ServiceImpl struct {
	events EventStore
	now    func() time.Time
}

// NewService wires the Extraction ingress service over an event store port.
func NewService(events EventStore) *ServiceImpl {
	return &ServiceImpl{events: events, now: time.Now}
}

// Ingest validates one raw event, applies protocol-neutral defaults, derives the
// idempotent write identity, and atomically persists the event with its
// extraction job, audit record, and idempotency result.
func (s *ServiceImpl) Ingest(ctx context.Context, call gateway.CallContext, command IngestCommand) (IngestResult, error) {
	prepared := s.applyDefaults(command)
	if err := validateIngest(prepared); err != nil {
		return IngestResult{}, gateway.WrapError(gateway.ErrorInvalidInput, err.Error(), err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationIngest, command)
	if err != nil {
		return IngestResult{}, gateway.WriteIdentityError(err)
	}
	record, replay, err := s.events.IngestEvent(ctx, identity, IngestParams{
		SpaceID:         prepared.SpaceID,
		SourceType:      prepared.SourceType,
		SourceName:      prepared.SourceName,
		SourceEventID:   prepared.SourceEventID,
		SourceStreamKey: prepared.SourceStreamKey,
		Sequence:        prepared.Sequence,
		OccurredAt:      prepared.OccurredAt,
		Content:         prepared.Content,
		Metadata:        prepared.Metadata,
	})
	if err != nil {
		return IngestResult{}, normalizeError(err)
	}
	operationID := record.OperationID
	if operationID == "" {
		operationID = call.RequestID
	}
	return IngestResult{
		WriteResult: gateway.WriteResult{OperationID: operationID, Replay: replay},
		EventID:     record.EventID,
		Status:      record.Status,
		JobIDs:      record.JobIDs,
		AcceptedAt:  record.AcceptedAt,
	}, nil
}

// GetEvent reads one raw event by identifier.
func (s *ServiceImpl) GetEvent(ctx context.Context, call gateway.CallContext, query GetEventQuery) (domain.RawEvent, error) {
	if strings.TrimSpace(query.EventID) == "" {
		return domain.RawEvent{}, gateway.NewError(gateway.ErrorInvalidInput, "event_id is required")
	}
	event, err := s.events.GetEvent(ctx, query.EventID)
	if err != nil {
		return domain.RawEvent{}, normalizeError(err)
	}
	return event, nil
}

func (s *ServiceImpl) applyDefaults(command IngestCommand) IngestCommand {
	if command.OccurredAt == "" {
		command.OccurredAt = s.now().UTC().Format(time.RFC3339Nano)
	} else if parsed, err := time.Parse(time.RFC3339, command.OccurredAt); err == nil {
		command.OccurredAt = parsed.UTC().Format(time.RFC3339Nano)
	}
	if command.Metadata == nil {
		command.Metadata = map[string]any{}
	}
	return command
}

func validateIngest(command IngestCommand) error {
	if strings.TrimSpace(command.SpaceID) == "" {
		return errors.New("space_id is required")
	}
	if !sourceTypes[command.SourceType] {
		return errors.New("source_type is invalid")
	}
	if utf8.RuneCountInString(command.SourceName) < 1 || utf8.RuneCountInString(command.SourceName) > 128 {
		return errors.New("source_name length must be between 1 and 128")
	}
	if command.SourceEventID != nil && utf8.RuneCountInString(*command.SourceEventID) > 256 {
		return errors.New("source_event_id is too long")
	}
	if utf8.RuneCountInString(command.SourceStreamKey) > 256 {
		return errors.New("source_stream_key is too long")
	}
	if command.Sequence != nil && *command.Sequence < 0 {
		return errors.New("sequence must be a non-negative integer")
	}
	if _, err := time.Parse(time.RFC3339, command.OccurredAt); err != nil {
		return errors.New("occurred_at must be RFC3339")
	}
	return validateContent(command.Content)
}

func validateContent(content domain.Content) error {
	if content.MediaType == "" {
		return errors.New("content.media_type is required")
	}
	if content.DeclaredSize != nil && *content.DeclaredSize < 0 {
		return errors.New("declared_size must not be negative")
	}
	switch content.Kind {
	case "text":
		if content.ReferenceURI != "" || content.DeclaredSize != nil || content.DeclaredHash != "" {
			return errors.New("text content contains incompatible fields")
		}
	case "json":
		if content.Value == nil || content.Text != "" || content.ReferenceURI != "" || content.DeclaredSize != nil || content.DeclaredHash != "" {
			return errors.New("json content requires value only")
		}
	case "reference":
		if content.ReferenceURI == "" || content.Text != "" || content.Value != nil {
			return errors.New("reference content requires reference_uri")
		}
	default:
		return errors.New("unsupported content kind")
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
	return gateway.WrapError(gateway.ErrorInternal, "extraction operation failed", err)
}
