package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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
	if err := validateMetadata(command.Metadata, 1); err != nil {
		return err
	}
	return validateContent(command.Content)
}

// validateMetadata bounds the shape of caller-supplied event metadata. It never
// inspects or echoes values, so a rejected payload cannot leak through an error
// message.
func validateMetadata(value any, depth int) error {
	if value == nil {
		return nil
	}
	if depth > MaxMetadataDepth {
		return errors.New("metadata exceeds maximum depth")
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > MaxMetadataKeys {
			return errors.New("metadata has too many keys")
		}
		for key, child := range typed {
			if utf8.RuneCountInString(key) < 1 || utf8.RuneCountInString(key) > 128 {
				return errors.New("metadata key length must be between 1 and 128")
			}
			if err := validateMetadata(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > MaxMetadataKeys {
			return errors.New("metadata array is too large")
		}
		for _, child := range typed {
			if err := validateMetadata(child, depth+1); err != nil {
				return err
			}
		}
	case string, bool, nil, json.Number:
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return errors.New("metadata number must be finite")
		}
	default:
		return errors.New("metadata contains an unsupported value")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxMetadataBytes {
		return errors.New("metadata exceeds encoded size limit")
	}
	return nil
}

func validateContent(content domain.Content) error {
	if content.MediaType == "" {
		return errors.New("content.media_type is required")
	}
	if utf8.RuneCountInString(content.MediaType) > 128 {
		return errors.New("content.media_type is too long")
	}
	if content.DeclaredSize != nil && *content.DeclaredSize < 0 {
		return errors.New("declared_size must not be negative")
	}
	switch content.Kind {
	case "text":
		if content.ReferenceURI != "" || content.DeclaredSize != nil || content.DeclaredHash != "" {
			return errors.New("text content contains incompatible fields")
		}
		if utf8.RuneCountInString(content.Text) == 0 {
			return errors.New("text content must not be empty")
		}
		if utf8.RuneCountInString(content.Text) > MaxContentRunes {
			return errors.New("content.text exceeds the maximum length")
		}
	case "json":
		if content.Value == nil || content.Text != "" || content.ReferenceURI != "" || content.DeclaredSize != nil || content.DeclaredHash != "" {
			return errors.New("json content requires value only")
		}
		if err := validateJSONValue(content.Value, 1, new(int)); err != nil {
			return err
		}
	case "reference":
		if content.ReferenceURI == "" || content.Text != "" || content.Value != nil {
			return errors.New("reference content requires reference_uri")
		}
		if utf8.RuneCountInString(content.ReferenceURI) > MaxReferenceURIRunes {
			return errors.New("content.reference_uri exceeds the maximum length")
		}
		if content.DeclaredHash != "" && !validDeclaredHash(content.DeclaredHash) {
			return errors.New("declared_hash must be a hex digest")
		}
	default:
		return errors.New("unsupported content kind")
	}
	return nil
}

// validateJSONValue bounds embedded JSON content by depth and total node count.
// It accepts exactly the values a JSON decoder produces for a decoded body.
func validateJSONValue(value any, depth int, nodes *int) error {
	if depth > MaxJSONValueDepth {
		return errors.New("json content exceeds maximum depth")
	}
	*nodes++
	if *nodes > MaxJSONValueNodes {
		return errors.New("json content has too many values")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if utf8.RuneCountInString(key) > 128 {
				return errors.New("json content key is too long")
			}
			if err := validateJSONValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateJSONValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case string, bool, nil, json.Number:
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return errors.New("json content number must be finite")
		}
	default:
		return errors.New("json content contains an unsupported value")
	}
	return nil
}

// validDeclaredHash accepts an optional declared content digest. Only the shape
// is validated: dMem records the reference and never fetches or verifies it.
func validDeclaredHash(value string) bool {
	if len(value) < 32 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9', char >= 'a' && char <= 'f', char >= 'A' && char <= 'F':
		default:
			return false
		}
	}
	return true
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
