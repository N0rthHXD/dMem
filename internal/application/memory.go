package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
	"github.com/N0rthHXD/dMem/internal/storage"
)

var ErrInvalidInput = errors.New("invalid application input")

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

// MemoryService is the temporary application surface retained for the Retrieval
// slice until it moves into the Retrieval gateway module.
type MemoryService interface {
	Recall(context.Context, RecallQuery) (domain.SearchResponse, error)
}

type memoryRepository interface {
	SearchMemories(context.Context, storage.SearchParams) (domain.SearchResponse, error)
}

type Service struct {
	repository memoryRepository
}

func NewService(repository memoryRepository) *Service {
	return &Service{repository: repository}
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
