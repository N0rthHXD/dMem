package memory

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

// listStates is the closed set of lifecycle states a list query may filter on.
var listStates = map[string]bool{
	"active": true, "archived": true, "contested": true, "expired": true,
	"superseded": true, "retracted": true, "deleted": true,
}

// ServiceImpl is the default memory administration service. It owns input
// validation, protocol-neutral idempotent write identity, evidence inheritance,
// and stable error mapping; persistence, the revision fence, and the lifecycle
// state machine stay in storage.
type ServiceImpl struct {
	store Store
}

// NewService wires the memory administration service over a store port.
func NewService(store Store) *ServiceImpl {
	return &ServiceImpl{store: store}
}

// List returns one bounded page of memories for a scope.
func (s *ServiceImpl) List(ctx context.Context, _ gateway.CallContext, query ListQuery) (domain.MemoryPage, error) {
	if err := identifier("scope_id", query.SpaceID); err != nil {
		return domain.MemoryPage{}, invalidInput(err)
	}
	if query.State != "" && !listStates[query.State] {
		return domain.MemoryPage{}, invalidInput(errors.New("state is unsupported"))
	}
	if err := query.Page.Validate(); err != nil {
		return domain.MemoryPage{}, gateway.WrapError(gateway.ErrorInvalidInput, "page request is invalid", err)
	}
	page, err := s.store.ListMemories(ctx, ListFilter{
		SpaceID:          query.SpaceID,
		State:            query.State,
		IncludeAncestors: query.IncludeAncestors,
		Limit:            query.Page.EffectiveLimit(),
		Cursor:           query.Page.Cursor,
	})
	if err != nil {
		return domain.MemoryPage{}, normalizeError(err)
	}
	if page.Items == nil {
		page.Items = []domain.Memory{}
	}
	return page, nil
}

// Get reads one memory by identifier, including logically deleted memories.
func (s *ServiceImpl) Get(ctx context.Context, _ gateway.CallContext, query GetQuery) (domain.Memory, error) {
	if err := identifier("memory_id", query.MemoryID); err != nil {
		return domain.Memory{}, invalidInput(err)
	}
	value, err := s.store.GetMemory(ctx, query.MemoryID, true)
	if err != nil {
		return domain.Memory{}, normalizeError(err)
	}
	return value, nil
}

// ListRevisions returns the immutable revision history of one memory.
func (s *ServiceImpl) ListRevisions(ctx context.Context, _ gateway.CallContext, query MemoryQuery) ([]domain.MemoryRevision, error) {
	if err := identifier("memory_id", query.MemoryID); err != nil {
		return nil, invalidInput(err)
	}
	revisions, err := s.store.ListMemoryRevisions(ctx, query.MemoryID)
	if err != nil {
		return nil, normalizeError(err)
	}
	if revisions == nil {
		revisions = []domain.MemoryRevision{}
	}
	return revisions, nil
}

// ListStateEvents returns the append-only lifecycle events of one memory.
func (s *ServiceImpl) ListStateEvents(ctx context.Context, _ gateway.CallContext, query MemoryQuery) ([]domain.MemoryStateEvent, error) {
	if err := identifier("memory_id", query.MemoryID); err != nil {
		return nil, invalidInput(err)
	}
	events, err := s.store.ListMemoryStateEvents(ctx, query.MemoryID)
	if err != nil {
		return nil, normalizeError(err)
	}
	if events == nil {
		events = []domain.MemoryStateEvent{}
	}
	return events, nil
}

// Revise appends one immutable replacement revision under the expected-revision
// fence. When the command omits evidence, the current revision evidence is
// inherited so a pure content edit cannot silently drop provenance.
func (s *ServiceImpl) Revise(ctx context.Context, call gateway.CallContext, command ReviseCommand) (WriteResult, error) {
	if err := validateRevise(command); err != nil {
		return WriteResult{}, invalidInput(err)
	}
	evidence := command.Evidence
	if evidence == nil {
		current, err := s.store.GetMemory(ctx, command.MemoryID, false)
		if err != nil {
			return WriteResult{}, normalizeError(err)
		}
		evidence = evidenceFromRevision(current.CurrentRevision.Evidence)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationRevise, command)
	if err != nil {
		return WriteResult{}, gateway.WriteIdentityError(err)
	}
	stored, operationID, replay, err := s.store.ReviseMemory(ctx, identity, ReviseParams{
		MemoryID:           command.MemoryID,
		ExpectedRevisionID: command.ExpectedRevisionID,
		Content:            command.Content,
		ValidFrom:          command.ValidFrom,
		ValidUntil:         command.ValidUntil,
		Evidence:           evidenceParams(evidence),
		Reason:             command.Reason,
	})
	if err != nil {
		return WriteResult{}, normalizeError(err)
	}
	return WriteResult{WriteResult: gateway.WriteResult{OperationID: operationID, Replay: replay}, Memory: stored}, nil
}

// ChangeState applies one lifecycle transition under the expected-revision
// fence.
func (s *ServiceImpl) ChangeState(ctx context.Context, call gateway.CallContext, command StateChangeCommand) (WriteResult, error) {
	if err := validateStateChange(command); err != nil {
		return WriteResult{}, invalidInput(err)
	}
	identity, err := gateway.NewWriteIdentity(call, OperationStateChange, command)
	if err != nil {
		return WriteResult{}, gateway.WriteIdentityError(err)
	}
	stored, operationID, replay, err := s.store.ChangeMemoryState(ctx, identity, StateChangeParams{
		MemoryID:           command.MemoryID,
		ExpectedRevisionID: command.ExpectedRevisionID,
		Action:             string(command.Action),
		RestoreRevisionID:  command.RestoreRevisionID,
		Reason:             command.Reason,
	})
	if err != nil {
		return WriteResult{}, normalizeError(err)
	}
	return WriteResult{WriteResult: gateway.WriteResult{OperationID: operationID, Replay: replay}, Memory: stored}, nil
}

func validateRevise(command ReviseCommand) error {
	if err := identifiers(command.MemoryID, command.ExpectedRevisionID); err != nil {
		return err
	}
	if err := text("content", command.Content, MaxContentRunes); err != nil {
		return err
	}
	if err := text("reason", command.Reason, MaxReasonRunes); err != nil {
		return err
	}
	if err := window(command.ValidFrom, command.ValidUntil); err != nil {
		return err
	}
	if command.Evidence == nil {
		return nil
	}
	if len(command.Evidence) == 0 || len(command.Evidence) > MaxEvidence {
		return errors.New("evidence must contain between 1 and 64 items when supplied")
	}
	for _, item := range command.Evidence {
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

func validateStateChange(command StateChangeCommand) error {
	if err := identifiers(command.MemoryID, command.ExpectedRevisionID); err != nil {
		return err
	}
	if !command.Action.Valid() {
		return errors.New("action is unsupported")
	}
	if command.Action == StateRestore {
		if command.RestoreRevisionID == nil {
			return errors.New("restore_revision_id is required for restore")
		}
		if err := identifier("restore_revision_id", *command.RestoreRevisionID); err != nil {
			return err
		}
	} else if command.RestoreRevisionID != nil {
		return errors.New("restore_revision_id is only valid for restore")
	}
	return text("reason", command.Reason, MaxReasonRunes)
}

func evidenceFromRevision(evidence []domain.RevisionEvidence) []Evidence {
	result := make([]Evidence, len(evidence))
	for i, item := range evidence {
		result[i] = Evidence{EventID: item.EventID, Role: item.Role, StartOffset: item.StartOffset, EndOffset: item.EndOffset}
	}
	return result
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

// text requires non-empty valid UTF-8 content within a rune bound. Surrounding
// whitespace is preserved because it may be meaningful in revision text.
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
	return gateway.WrapError(gateway.ErrorInternal, "memory operation failed", err)
}
