package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/storage"
)

var ErrInvalidInput = errors.New("invalid application input")

type RequestIdentity struct {
	CallerID       string
	IdempotencyKey string
	RequestID      string
	Method         string
	Route          string
	RequestHash    string
}

type RememberCommand struct {
	SpaceID       string
	Content       string
	MemoryType    string
	Sensitivity   string
	ValidFrom     *string
	ValidUntil    *string
	SourceName    string
	SourceEventID *string
	OccurredAt    string
	Metadata      map[string]any
	Reason        string
}

type RememberResult struct {
	Memory  domain.Memory `json:"memory"`
	EventID string        `json:"event_id"`
	Replay  bool          `json:"-"`
}

type RecallQuery struct {
	Query                  string
	SpaceIDs               []string
	IncludeAncestors       bool
	MemoryTypes            []string
	MemoryTypesSet         bool
	States                 []string
	ReferenceTime          string
	ReferenceTimeSet       bool
	Mode                   string
	Limit                  int
	MaxChars               int
	IncludeEvidence        bool
	ExcludeStaleAggregates bool
	Cursor                 string
}

type FeedbackCommand struct {
	SpaceID         string
	MemoryID        string
	FeedbackType    string
	ReasonCode      string
	Note            *string
	Search          *domain.FeedbackSearchContext
	CreateProposal  bool
	SourceEventID   string
	ProposedContent string
}

type FeedbackResult struct {
	Response domain.FeedbackAppendResponse `json:"response"`
	Replay   bool                          `json:"-"`
}

type ForgetCommand struct {
	MemoryID string
	Reason   string
}

type ForgetResult struct {
	Memory domain.Memory `json:"memory"`
	Replay bool          `json:"-"`
}

type MemoryService interface {
	Remember(context.Context, RequestIdentity, RememberCommand) (RememberResult, error)
	Recall(context.Context, RecallQuery) (domain.SearchResponse, error)
	Feedback(context.Context, RequestIdentity, FeedbackCommand) (FeedbackResult, error)
	Forget(context.Context, RequestIdentity, ForgetCommand) (ForgetResult, error)
}

type memoryRepository interface {
	Remember(context.Context, storage.WriteIdentity, storage.RememberParams) (storage.RememberResult, bool, error)
	SearchMemories(context.Context, storage.SearchParams) (domain.SearchResponse, error)
	AppendCurrentFeedback(context.Context, storage.WriteIdentity, storage.AppendFeedbackParams) (domain.FeedbackAppendResponse, bool, error)
	ForgetMemory(context.Context, storage.WriteIdentity, string, string) (domain.Memory, bool, error)
}

type Service struct {
	repository memoryRepository
}

func NewService(repository memoryRepository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Remember(ctx context.Context, identity RequestIdentity, command RememberCommand) (RememberResult, error) {
	if err := validateRemember(command); err != nil {
		return RememberResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	result, replay, err := s.repository.Remember(ctx, storageIdentity(identity), storage.RememberParams{
		SpaceID: command.SpaceID, Content: command.Content, MemoryType: command.MemoryType,
		Sensitivity: command.Sensitivity, ValidFrom: command.ValidFrom, ValidUntil: command.ValidUntil,
		SourceName: command.SourceName, SourceEventID: command.SourceEventID, OccurredAt: command.OccurredAt,
		Metadata: command.Metadata, Reason: command.Reason,
	})
	return RememberResult{Memory: result.Memory, EventID: result.EventID, Replay: replay}, err
}

func (s *Service) Recall(ctx context.Context, query RecallQuery) (domain.SearchResponse, error) {
	if err := validateRecall(query); err != nil {
		return domain.SearchResponse{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return s.repository.SearchMemories(ctx, storage.SearchParams{
		Query: query.Query, SpaceIDs: query.SpaceIDs, IncludeAncestors: query.IncludeAncestors,
		MemoryTypes: query.MemoryTypes, MemoryTypesSet: query.MemoryTypesSet, States: query.States,
		ReferenceTime: query.ReferenceTime, ReferenceTimeSet: query.ReferenceTimeSet, Mode: query.Mode,
		Limit: query.Limit, MaxChars: query.MaxChars, IncludeEvidence: query.IncludeEvidence,
		ExcludeStaleAggregates: query.ExcludeStaleAggregates, Cursor: query.Cursor,
	})
}

func (s *Service) Feedback(ctx context.Context, identity RequestIdentity, command FeedbackCommand) (FeedbackResult, error) {
	if err := validateFeedback(command); err != nil {
		return FeedbackResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	result, replay, err := s.repository.AppendCurrentFeedback(ctx, storageIdentity(identity), storage.AppendFeedbackParams{
		SpaceID: command.SpaceID, MemoryID: command.MemoryID,
		FeedbackType: command.FeedbackType, ReasonCode: command.ReasonCode, Note: command.Note, Search: command.Search,
		CreateProposal: command.CreateProposal, SourceEventID: command.SourceEventID, ProposedContent: command.ProposedContent,
	})
	return FeedbackResult{Response: result, Replay: replay}, err
}

func (s *Service) Forget(ctx context.Context, identity RequestIdentity, command ForgetCommand) (ForgetResult, error) {
	if command.MemoryID == "" || utf8.RuneCountInString(command.Reason) < 1 || utf8.RuneCountInString(command.Reason) > 1024 {
		return ForgetResult{}, fmt.Errorf("%w: memory_id and a reason of at most 1024 characters are required", ErrInvalidInput)
	}
	memory, replay, err := s.repository.ForgetMemory(ctx, storageIdentity(identity), command.MemoryID, command.Reason)
	return ForgetResult{Memory: memory, Replay: replay}, err
}

func HashRequest(value any) string {
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func storageIdentity(identity RequestIdentity) storage.WriteIdentity {
	return storage.WriteIdentity{
		CallerID: identity.CallerID, IdempotencyKey: identity.IdempotencyKey, RequestID: identity.RequestID,
		Method: identity.Method, Route: identity.Route, RequestHash: identity.RequestHash,
	}
}

func validateRemember(command RememberCommand) error {
	if command.SpaceID == "" || strings.TrimSpace(command.Content) == "" {
		return errors.New("space_id and content are required")
	}
	validTypes := map[string]bool{"fact": true, "preference": true, "constraint": true, "decision": true, "task": true, "event": true, "project_context": true, "agent_experience": true}
	if !validTypes[command.MemoryType] || command.Sensitivity != "normal" && command.Sensitivity != "sensitive" {
		return errors.New("memory_type or sensitivity is invalid")
	}
	if command.SourceName == "" || utf8.RuneCountInString(command.SourceName) > 128 || utf8.RuneCountInString(command.Reason) < 1 || utf8.RuneCountInString(command.Reason) > 1024 {
		return errors.New("source_name or reason is invalid")
	}
	if _, err := time.Parse(time.RFC3339, command.OccurredAt); err != nil {
		return errors.New("occurred_at must be RFC3339")
	}
	return validateTimeRange(command.ValidFrom, command.ValidUntil)
}

func validateRecall(query RecallQuery) error {
	if strings.TrimSpace(query.Query) == "" || len(query.SpaceIDs) == 0 || query.Limit < 1 || query.Limit > 100 || query.MaxChars < 1 || query.MaxChars > 100000 {
		return errors.New("recall query, scope, limit, or character budget is invalid")
	}
	if query.Mode != "keyword" && query.Mode != "semantic" && query.Mode != "hybrid" {
		return errors.New("recall mode is invalid")
	}
	validTypes := map[string]bool{"fact": true, "preference": true, "constraint": true, "decision": true, "task": true, "event": true, "project_context": true, "agent_experience": true, "aggregate": true}
	for _, value := range query.MemoryTypes {
		if !validTypes[value] {
			return errors.New("recall memory_types is invalid")
		}
	}
	validStates := map[string]bool{"active": true, "contested": true}
	for _, value := range query.States {
		if !validStates[value] {
			return errors.New("recall states is invalid")
		}
	}
	if query.ReferenceTimeSet {
		if _, err := time.Parse(time.RFC3339, query.ReferenceTime); err != nil {
			return errors.New("reference_time must be RFC3339")
		}
	}
	return nil
}

func validateFeedback(command FeedbackCommand) error {
	types := map[string]bool{"useful": true, "irrelevant": true, "correct": true, "supplement": true, "contest": true}
	reasons := map[string]bool{"answered_query": true, "relevant_context": true, "not_relevant": true, "fact_incorrect": true, "fact_incomplete": true, "fact_outdated": true, "conflicts_with_evidence": true, "duplicate": true, "other": true}
	if command.SpaceID == "" || command.MemoryID == "" || !types[command.FeedbackType] || !reasons[command.ReasonCode] {
		return errors.New("feedback target, type, or reason is invalid")
	}
	if command.Note != nil && utf8.RuneCountInString(*command.Note) > 2000 {
		return errors.New("feedback note is too long")
	}
	canPropose := command.FeedbackType == "correct" || command.FeedbackType == "supplement" || command.FeedbackType == "contest"
	if command.CreateProposal && (!canPropose || command.SourceEventID == "" || strings.TrimSpace(command.ProposedContent) == "") {
		return errors.New("feedback proposal requires a corrective type, source_event_id, and proposed_content")
	}
	if command.Search != nil {
		if command.Search.SearchOperationID == "" || len(command.Search.QueryHash) != 64 || command.Search.ResultRank < 1 || command.Search.RankingVersion == "" || len(command.Search.MatchedChannels) == 0 {
			return errors.New("search context is invalid")
		}
		sort.Strings(command.Search.MatchedChannels)
	}
	return nil
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
