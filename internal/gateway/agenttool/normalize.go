package agenttool

import (
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

type canonicalEnvelope[T any] struct {
	ContractVersion string                `json:"contract_version"`
	Operation       gateway.OperationName `json:"operation"`
	Command         T                     `json:"command"`
}

func CanonicalRecallHash(command RecallCommand) (string, error) {
	normalized := command
	normalized.Query = normalizeText(command.Query)
	normalized.ScopeIDs = normalizedSet(command.ScopeIDs)
	normalized.EntityHints = normalizedSet(command.EntityHints)
	if normalized.Intent == "" {
		normalized.Intent = "auto"
	}
	if normalized.Limit == 0 {
		normalized.Limit = DefaultRecallLimit
	}
	normalized.ReferenceTime = utcTime(normalized.ReferenceTime)
	return gateway.CanonicalHash(canonicalEnvelope[RecallCommand]{ContractVersion, OperationRecall, normalized})
}

func CanonicalMemoryCommitHash(command MemoryCommitCommand) (string, error) {
	normalized := command
	normalized.Source.OccurredAt = command.Source.OccurredAt.UTC()
	normalized.Source.Metadata = normalizeMap(command.Source.Metadata)
	normalized.Memories = append([]MemorySuggestion(nil), command.Memories...)
	for i := range normalized.Memories {
		normalized.Memories[i].Content = normalizeText(normalized.Memories[i].Content)
		normalized.Memories[i].SearchHints = normalizedSet(normalized.Memories[i].SearchHints)
		normalized.Memories[i].ValidFrom = utcTime(normalized.Memories[i].ValidFrom)
		normalized.Memories[i].ValidUntil = utcTime(normalized.Memories[i].ValidUntil)
	}
	sort.Slice(normalized.Memories, func(i, j int) bool { return normalized.Memories[i].LocalID < normalized.Memories[j].LocalID })
	normalized.Entities = append([]EntitySuggestion(nil), command.Entities...)
	if len(normalized.Entities) == 0 {
		normalized.Entities = []EntitySuggestion{}
	}
	for i := range normalized.Entities {
		normalized.Entities[i].Aliases = normalizedSet(normalized.Entities[i].Aliases)
	}
	sort.Slice(normalized.Entities, func(i, j int) bool { return normalized.Entities[i].LocalID < normalized.Entities[j].LocalID })
	normalized.Facts = append([]FactSuggestion(nil), command.Facts...)
	if len(normalized.Facts) == 0 {
		normalized.Facts = []FactSuggestion{}
	}
	for i := range normalized.Facts {
		normalized.Facts[i].ValidFrom = utcTime(normalized.Facts[i].ValidFrom)
		normalized.Facts[i].ValidUntil = utcTime(normalized.Facts[i].ValidUntil)
	}
	sort.Slice(normalized.Facts, func(i, j int) bool { return normalized.Facts[i].LocalID < normalized.Facts[j].LocalID })
	normalized.Relations = append([]RelationSuggestion(nil), command.Relations...)
	if len(normalized.Relations) == 0 {
		normalized.Relations = []RelationSuggestion{}
	}
	sort.Slice(normalized.Relations, func(i, j int) bool { return normalized.Relations[i].LocalID < normalized.Relations[j].LocalID })
	return gateway.CanonicalHash(canonicalEnvelope[MemoryCommitCommand]{ContractVersion, OperationMemoryCommit, normalized})
}

func CanonicalFeedbackHash(command FeedbackCommand) (string, error) {
	command.Reason = normalizeText(command.Reason)
	return gateway.CanonicalHash(canonicalEnvelope[FeedbackCommand]{ContractVersion, OperationFeedback, command})
}

func CanonicalForgetHash(command ForgetCommand) (string, error) {
	command.Reason = normalizeText(command.Reason)
	return gateway.CanonicalHash(canonicalEnvelope[ForgetCommand]{ContractVersion, OperationForget, command})
}

func NewMemoryCommitWriteIdentity(call gateway.CallContext, command MemoryCommitCommand) (gateway.WriteIdentity, error) {
	if err := ValidateMemoryCommit(command); err != nil {
		return gateway.WriteIdentity{}, err
	}
	hash, err := CanonicalMemoryCommitHash(command)
	if err != nil {
		return gateway.WriteIdentity{}, gateway.WriteIdentityError(err)
	}
	return writeIdentity(call, OperationMemoryCommit, hash)
}

func NewFeedbackWriteIdentity(call gateway.CallContext, command FeedbackCommand) (gateway.WriteIdentity, error) {
	if err := ValidateFeedback(command); err != nil {
		return gateway.WriteIdentity{}, err
	}
	hash, err := CanonicalFeedbackHash(command)
	if err != nil {
		return gateway.WriteIdentity{}, gateway.WriteIdentityError(err)
	}
	return writeIdentity(call, OperationFeedback, hash)
}

func NewForgetWriteIdentity(call gateway.CallContext, command ForgetCommand) (gateway.WriteIdentity, error) {
	if err := ValidateForget(command); err != nil {
		return gateway.WriteIdentity{}, err
	}
	hash, err := CanonicalForgetHash(command)
	if err != nil {
		return gateway.WriteIdentity{}, gateway.WriteIdentityError(err)
	}
	return writeIdentity(call, OperationForget, hash)
}

func writeIdentity(call gateway.CallContext, operation gateway.OperationName, hash string) (gateway.WriteIdentity, error) {
	if !call.HasCaller() {
		return gateway.WriteIdentity{}, gateway.WriteIdentityError(gateway.ErrMissingCaller)
	}
	if call.IdempotencyKey() == "" {
		return gateway.WriteIdentity{}, gateway.WriteIdentityError(gateway.ErrMissingIdempotency)
	}
	return gateway.WriteIdentity{Caller: call.Caller, Operation: operation, IdempotencyKey: call.IdempotencyKey(), CommandHash: hash, RequestID: call.RequestID}, nil
}

func normalizeText(value string) string { return strings.Join(strings.Fields(value), " ") }

func normalizedSet(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizeText(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

func normalizeMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return map[string]any{}
	}
	result := make(map[string]any, len(value))
	for key, child := range value {
		switch typed := child.(type) {
		case map[string]any:
			result[key] = normalizeMap(typed)
		case []any:
			items := make([]any, len(typed))
			for i, item := range typed {
				if object, ok := item.(map[string]any); ok {
					items[i] = normalizeMap(object)
				} else {
					items[i] = item
				}
			}
			result[key] = items
		default:
			result[key] = child
		}
	}
	return result
}
