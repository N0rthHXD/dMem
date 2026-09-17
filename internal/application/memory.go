package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
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
	SpaceID         string
	Content         string
	Kind            string
	Sensitivity     string
	ValidFrom       *string
	ValidUntil      *string
	SourceName      string
	SourceEventID   *string
	SourceStreamKey string
	Sequence        *int64
	OccurredAt      string
	Metadata        map[string]any
	Reason          string
}

type RememberResult struct {
	Memory  domain.Memory `json:"memory"`
	EventID string        `json:"event_id"`
	Replay  bool          `json:"-"`
}

type RecallQuery struct {
	OperationID            string
	Query                  string
	SpaceIDs               []string
	IncludeAncestors       bool
	MemoryTypes            []string
	MemoryTypesSet         bool
	States                 []string
	ReferenceTime          string
	ReferenceTimeSet       bool
	Intent                 domain.RecallIntent
	Channels               []string
	Limit                  int
	MaxChars               int
	MaxContextTokens       int
	IncludeEvidence        bool
	Explain                bool
	ExcludeStaleAggregates bool
	Cursor                 string
	GraphPolicy            string
	SkipDiversification    bool
}

type FeedbackCommand struct {
	SpaceID         string
	MemoryID        string
	Purpose         string
	FeedbackType    string
	ReasonCode      string
	Note            *string
	Decision        *domain.FeedbackDecisionContext
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
		SpaceID: command.SpaceID, Content: command.Content, Kind: command.Kind,
		Sensitivity: command.Sensitivity, ValidFrom: command.ValidFrom, ValidUntil: command.ValidUntil,
		SourceName: command.SourceName, SourceEventID: command.SourceEventID, SourceStreamKey: command.SourceStreamKey,
		Sequence: command.Sequence, OccurredAt: command.OccurredAt,
		Metadata: command.Metadata, Reason: command.Reason,
	})
	return RememberResult{Memory: result.Memory, EventID: result.EventID, Replay: replay}, err
}

func (s *Service) Recall(ctx context.Context, query RecallQuery) (domain.SearchResponse, error) {
	if err := validateRecall(query); err != nil {
		return domain.SearchResponse{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return s.repository.SearchMemories(ctx, storage.SearchParams{
		OperationID: query.OperationID, Query: query.Query, SpaceIDs: query.SpaceIDs, IncludeAncestors: query.IncludeAncestors,
		MemoryTypes: query.MemoryTypes, MemoryTypesSet: query.MemoryTypesSet, States: query.States,
		ReferenceTime: query.ReferenceTime, ReferenceTimeSet: query.ReferenceTimeSet, Channels: query.Channels,
		Limit: query.Limit, MaxChars: query.MaxChars, MaxContextTokens: query.MaxContextTokens,
		IncludeEvidence:        query.IncludeEvidence,
		ExcludeStaleAggregates: query.ExcludeStaleAggregates, Explain: query.Explain, Cursor: query.Cursor,
		Intent: string(query.Intent), SkipDiversification: query.SkipDiversification,
		GraphPolicy: retrieval.GraphPolicy(query.GraphPolicy),
	})
}

func (s *Service) Feedback(ctx context.Context, identity RequestIdentity, command FeedbackCommand) (FeedbackResult, error) {
	if err := validateFeedback(command); err != nil {
		return FeedbackResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	result, replay, err := s.repository.AppendCurrentFeedback(ctx, storageIdentity(identity), storage.AppendFeedbackParams{
		SpaceID: command.SpaceID, MemoryID: command.MemoryID,
		Purpose: command.Purpose, FeedbackType: command.FeedbackType, ReasonCode: command.ReasonCode, Note: command.Note, Decision: command.Decision,
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

func validateRecall(query RecallQuery) error {
	if strings.TrimSpace(query.Query) == "" || len(query.SpaceIDs) == 0 || query.Limit < 1 || query.Limit > 1000 || query.MaxContextTokens < 1 || query.MaxContextTokens > 1000000 || query.MaxChars < 1 || query.MaxChars > 4000000 {
		return errors.New("recall query, scope, result limit, or context budget is invalid")
	}
	if !query.Intent.Valid() {
		return errors.New("recall intent is invalid")
	}
	for _, channel := range query.Channels {
		if channel != "keyword" && channel != "semantic" && channel != "graph" {
			return errors.New("recall channels is invalid")
		}
	}
	for _, value := range query.MemoryTypes {
		if !domain.MemoryKind(value).Valid() {
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
	if query.GraphPolicy != "" && !retrieval.GraphPolicy(query.GraphPolicy).Valid() {
		return errors.New("recall graph_policy is invalid")
	}
	return nil
}

func validateFeedback(command FeedbackCommand) error {
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
