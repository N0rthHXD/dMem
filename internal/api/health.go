package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/N0rthHXD/dMem/internal/aggregate"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type Health struct {
	ProcessAlive             bool                    `json:"process_alive"`
	Mode                     string                  `json:"mode"`
	OverallStatus            string                  `json:"overall_status"`
	Ready                    bool                    `json:"ready"`
	AcceptingWrites          bool                    `json:"accepting_writes"`
	QueryAvailable           bool                    `json:"query_available"`
	StorageState             string                  `json:"storage_state"`
	StorageHealthy           bool                    `json:"storage_healthy"`
	StorageWritable          bool                    `json:"storage_writable"`
	StorageIntegrityState    string                  `json:"storage_integrity_state"`
	SchemaCompatibility      string                  `json:"schema_compatibility"`
	QueueState               string                  `json:"queue_state"`
	FTSState                 string                  `json:"fts_state"`
	FTSIndexState            string                  `json:"fts_index_state"`
	FTSFreshness             string                  `json:"fts_freshness"`
	ProcessorState           string                  `json:"processor_state"`
	ProcessorIdentity        string                  `json:"processor_identity"`
	SemanticState            string                  `json:"semantic_state"`
	SemanticIndexState       string                  `json:"semantic_index_state"`
	SemanticIndexVersion     int                     `json:"semantic_index_version"`
	EntityIndexState         string                  `json:"entity_index_state"`
	EntityIndexVersion       int64                   `json:"entity_index_version"`
	RelationIndexState       string                  `json:"relation_index_state"`
	RelationIndexVersion     int64                   `json:"relation_index_version"`
	GraphIndexState          string                  `json:"graph_index_state"`
	GraphIndexVersion        int64                   `json:"graph_index_version"`
	FeedbackAggregateState   string                  `json:"feedback_aggregate_state"`
	FeedbackAggregateVersion int64                   `json:"feedback_aggregate_version"`
	AggregateState           string                  `json:"aggregate_state"`
	AggregateFreshCount      int                     `json:"aggregate_fresh_count"`
	AggregateStaleCount      int                     `json:"aggregate_stale_count"`
	AggregatePendingRebuilds int                     `json:"aggregate_pending_rebuilds"`
	BackupState              string                  `json:"backup_state"`
	JobQueueDepth            int                     `json:"job_queue_depth"`
	OldestJobAgeSeconds      int                     `json:"oldest_job_age_seconds"`
	DiskPressure             string                  `json:"disk_pressure"`
	ConfigurationIdentity    string                  `json:"configuration_identity,omitempty"`
	Decision                 *storage.DecisionHealth `json:"decision,omitempty"`
}

type Handler struct {
	ready            atomic.Bool
	ftsState         string
	repository       *storage.Repository
	retrievalGateway gwretrieval.Service
	memoryGateway    memory.Service
	feedbackGateway  feedback.Service
	aggregates       *aggregate.Service
	processorHealth  interface{ ProcessorHealth() (string, string) }
	maxRequestSize   int64
	id               func() (string, error)
	configuration    ConfigurationController
	reloadRequests   map[string]reloadRequest
	reloadMu         sync.Mutex
}

type reloadRequest struct {
	expectedIdentity string
	response         reloadConfigurationResponse
}

type ConfigurationController interface {
	ConfigurationStatus() (*config.Snapshot, config.ActivationResult)
	ReloadConfiguration(context.Context) (config.ActivationResult, error)
}

type HandlerDependencies struct {
	Repository       *storage.Repository
	RetrievalGateway gwretrieval.Service
	MemoryGateway    memory.Service
	FeedbackGateway  feedback.Service
	AggregateService *aggregate.Service
	ProcessorHealth  interface{ ProcessorHealth() (string, string) }
	MaxRequestSize   int64
	ID               func() (string, error)
	Configuration    ConfigurationController
}

func NewHandler(ftsState string, dependencies HandlerDependencies) *Handler {
	h := &Handler{
		ftsState:         ftsState,
		repository:       dependencies.Repository,
		retrievalGateway: dependencies.RetrievalGateway,
		memoryGateway:    dependencies.MemoryGateway,
		feedbackGateway:  dependencies.FeedbackGateway,
		aggregates:       dependencies.AggregateService,
		processorHealth:  dependencies.ProcessorHealth,
		maxRequestSize:   dependencies.MaxRequestSize,
		id:               dependencies.ID,
		configuration:    dependencies.Configuration,
		reloadRequests:   make(map[string]reloadRequest),
	}
	h.ready.Store(true)
	return h
}

func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", h.health)
	mux.HandleFunc("GET /v1/config", h.configurationStatus)
	mux.HandleFunc("POST /v1/config/reload", h.reloadConfiguration)
	mux.HandleFunc("POST /v1/remember", h.remember)
	mux.HandleFunc("POST /v1/recall", h.recall)
	mux.HandleFunc("POST /v1/memories/{memory_id}/feedback", h.memoryFeedback)
	mux.HandleFunc("POST /v1/memories/{memory_id}/forget", h.forget)
	mux.HandleFunc("GET /v1/spaces", h.spaces)
	mux.HandleFunc("POST /v1/spaces", h.spaces)
	mux.HandleFunc("GET /v1/spaces/{space_id}", h.space)
	mux.HandleFunc("GET /v1/events", h.events)
	mux.HandleFunc("POST /v1/events", h.events)
	mux.HandleFunc("GET /v1/events/{event_id}", h.event)
	mux.HandleFunc("POST /v1/events/{event_id}/replay", h.replayEvent)
	mux.HandleFunc("GET /v1/memories", h.memories)
	mux.HandleFunc("POST /v1/memories", h.memories)
	mux.HandleFunc("GET /v1/memories/{memory_id}", h.memory)
	mux.HandleFunc("PATCH /v1/memories/{memory_id}", h.memory)
	mux.HandleFunc("GET /v1/memories/{memory_id}/revisions", h.memoryRevisions)
	mux.HandleFunc("POST /v1/memories/{memory_id}/state", h.memoryState)
	mux.HandleFunc("GET /v1/memories/{memory_id}/state-events", h.memoryStateEvents)
	mux.HandleFunc("GET /v1/candidates", h.candidates)
	mux.HandleFunc("GET /v1/candidates/{candidate_id}", h.candidate)
	mux.HandleFunc("POST /v1/candidates/{candidate_id}/{action}", h.candidate)
	mux.HandleFunc("GET /v1/entity-proposals", h.entityProposals)
	mux.HandleFunc("POST /v1/entity-proposals", h.entityProposals)
	mux.HandleFunc("GET /v1/entity-proposals/{proposal_id}", h.entityProposal)
	mux.HandleFunc("POST /v1/entity-proposals/{proposal_id}/{action}", h.entityProposal)
	mux.HandleFunc("GET /v1/entities", h.entities)
	mux.HandleFunc("GET /v1/entities/{entity_id}", h.entity)
	mux.HandleFunc("POST /v1/entities/{entity_id}/{action}", h.entity)
	mux.HandleFunc("GET /v1/relation-proposals", h.relationProposals)
	mux.HandleFunc("POST /v1/relation-proposals", h.relationProposals)
	mux.HandleFunc("GET /v1/relation-proposals/{proposal_id}", h.relationProposal)
	mux.HandleFunc("POST /v1/relation-proposals/{proposal_id}/{action}", h.relationProposal)
	mux.HandleFunc("GET /v1/evolution-proposals", h.evolutionProposals)
	mux.HandleFunc("POST /v1/evolution-proposals", h.evolutionProposals)
	mux.HandleFunc("GET /v1/evolution-proposals/{proposal_id}", h.evolutionProposal)
	mux.HandleFunc("POST /v1/evolution-proposals/{proposal_id}/{action}", h.evolutionProposal)
	mux.HandleFunc("GET /v1/conflict-cases", h.conflictCases)
	mux.HandleFunc("GET /v1/conflict-cases/{case_id}", h.conflictCase)
	mux.HandleFunc("POST /v1/conflict-cases/{case_id}/{action}", h.conflictCase)
	mux.HandleFunc("GET /v1/relations", h.relations)
	mux.HandleFunc("GET /v1/relations/{relation_id}", h.relation)
	mux.HandleFunc("GET /v1/entity-facts", h.entityFacts)
	mux.HandleFunc("GET /v1/entity-facts/{fact_id}", h.entityFact)
	mux.HandleFunc("GET /v1/fact-proposals", h.factProposals)
	mux.HandleFunc("POST /v1/fact-proposals", h.factProposals)
	mux.HandleFunc("GET /v1/fact-proposals/{proposal_id}", h.factProposal)
	mux.HandleFunc("POST /v1/fact-proposals/{proposal_id}/{action}", h.factProposal)
	mux.HandleFunc("POST /v1/indexes/graph/rebuild", h.rebuildGraphIndex)
	mux.HandleFunc("GET /v1/jobs/{job_id}", h.job)
	mux.HandleFunc("POST /v1/jobs/{job_id}/{action}", h.job)
	mux.HandleFunc("POST /v1/indexes/fts/rebuild", h.rebuildFTS)
	mux.HandleFunc("POST /v1/indexes/semantic/rebuild", h.rebuildSemantic)
	mux.HandleFunc("GET /v1/feedback", h.feedback)
	mux.HandleFunc("POST /v1/feedback", h.feedback)
	mux.HandleFunc("GET /v1/feedback/aggregates/health", h.feedbackAggregateHealth)
	mux.HandleFunc("POST /v1/feedback/aggregates/rebuild", h.rebuildFeedbackAggregates)
	mux.HandleFunc("GET /v1/aggregate-proposals", h.aggregateProposals)
	mux.HandleFunc("POST /v1/aggregate-proposals", h.aggregateProposals)
	mux.HandleFunc("GET /v1/aggregate-proposals/{proposal_id}", h.aggregateProposal)
	mux.HandleFunc("POST /v1/aggregate-proposals/{proposal_id}/{action}", h.aggregateProposal)
	mux.HandleFunc("GET /v1/aggregates", h.aggregatesList)
	mux.HandleFunc("GET /v1/aggregates/{aggregate_id}", h.aggregateGet)
	mux.HandleFunc("GET /v1/aggregates/{aggregate_id}/provenance", h.aggregateGet)
	mux.HandleFunc("/", h.notFound)
	return mux
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ready := h.ready.Load()
	storageState, schemaState := "unavailable", "unknown"
	storageIntegrityState := "unknown"
	queueState, queueDepth, oldestAge := "unavailable", 0, 0
	ftsState := h.ftsState
	diskPressure := "normal"
	semanticState, semanticVersion := "not_configured", 0
	entityState, entityVersion := "unavailable", int64(0)
	relationState, relationVersion := "unavailable", int64(0)
	graphState, graphVersion := "unavailable", int64(0)
	feedbackState, feedbackVersion := "unavailable", int64(0)
	aggregateState, aggregateFresh, aggregateStale, aggregateRebuilds := "unavailable", 0, 0, 0
	if h.repository != nil {
		storageState, schemaState = h.repository.StorageStatus(r.Context())
		if state, _ := h.repository.IntegrityStatus(); state != "" {
			storageIntegrityState = state
		}
		queueState, queueDepth, oldestAge, _ = h.repository.QueueStatus(r.Context())
		if state, err := h.repository.IndexState(r.Context()); err == nil {
			ftsState = state
		} else {
			ftsState = "unavailable"
		}
		if pressure, err := h.repository.DiskPressure(); err == nil {
			diskPressure = pressure
		} else {
			storageState = "unavailable"
		}
		semanticState, semanticVersion = h.repository.SemanticHealth(r.Context())
		entityState, entityVersion = h.repository.EntityIndexHealth(r.Context())
		relationState, relationVersion = h.repository.RelationIndexHealth(r.Context())
		graphState, graphVersion = h.repository.GraphIndexHealth(r.Context())
		if status, err := h.repository.FeedbackAggregateHealth(r.Context()); err == nil {
			feedbackState, feedbackVersion = status.State, status.Generation
		}
		if status, err := h.repository.AggregateHealth(r.Context()); err == nil {
			aggregateState, aggregateFresh, aggregateStale, aggregateRebuilds = status.State, status.FreshCount, status.StaleCount, status.PendingRebuilds
		}
	}
	processorState, processorIdentity := "not_configured", "none"
	if h.processorHealth != nil {
		processorState, processorIdentity = h.processorHealth.ProcessorHealth()
	}
	configurationIdentity := ""
	if h.configuration != nil {
		if snapshot, _ := h.configuration.ConfigurationStatus(); snapshot != nil {
			configurationIdentity = snapshot.Config.EffectiveIdentity
		}
	}
	storageHealthy := storageState == "ready"
	serviceReady := ready && storageHealthy && schemaState == "compatible"
	mode := "healthy"
	if !serviceReady {
		mode = "unavailable"
	} else if diskPressure == "hard" {
		mode = "read_only"
	} else if ftsState != "ready" || entityState != "ready" || relationState != "ready" || graphState != "ready" || feedbackState != "ready" || aggregateState != "ready" || queueState != "ready" || processorState == "degraded" || processorState == "unavailable" || semanticState == "degraded" || semanticState == "unavailable" || semanticState == "rebuilding" || semanticState == "rebuild_required" || diskPressure == "soft" {
		mode = "degraded"
	}
	acceptingWrites := serviceReady && diskPressure != "hard"
	backupState := "ready"
	if !storageHealthy {
		backupState = "unavailable"
	} else if diskPressure != "normal" {
		backupState = "degraded"
	}
	var decision *storage.DecisionHealth
	if h.repository != nil {
		value := h.repository.DecisionHealth(r.Context())
		decision = &value
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Health{
		ProcessAlive:             true,
		Mode:                     mode,
		OverallStatus:            mode,
		Ready:                    serviceReady,
		AcceptingWrites:          acceptingWrites,
		QueryAvailable:           serviceReady,
		StorageState:             storageState,
		StorageHealthy:           storageHealthy,
		StorageWritable:          acceptingWrites,
		StorageIntegrityState:    storageIntegrityState,
		SchemaCompatibility:      schemaState,
		QueueState:               queueState,
		FTSState:                 ftsState,
		FTSIndexState:            ftsState,
		FTSFreshness:             map[bool]string{true: "current", false: "stale"}[ftsState == "ready"],
		ProcessorState:           processorState,
		ProcessorIdentity:        processorIdentity,
		SemanticState:            semanticState,
		SemanticIndexState:       semanticState,
		SemanticIndexVersion:     semanticVersion,
		EntityIndexState:         entityState,
		EntityIndexVersion:       entityVersion,
		RelationIndexState:       relationState,
		RelationIndexVersion:     relationVersion,
		GraphIndexState:          graphState,
		GraphIndexVersion:        graphVersion,
		FeedbackAggregateState:   feedbackState,
		FeedbackAggregateVersion: feedbackVersion,
		AggregateState:           aggregateState,
		AggregateFreshCount:      aggregateFresh,
		AggregateStaleCount:      aggregateStale,
		AggregatePendingRebuilds: aggregateRebuilds,
		BackupState:              backupState,
		JobQueueDepth:            queueDepth,
		OldestJobAgeSeconds:      oldestAge,
		DiskPressure:             diskPressure,
		ConfigurationIdentity:    configurationIdentity,
		Decision:                 decision,
	})
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	h.writeError(w, http.StatusNotFound, "not_found", "route not found", h.requestID(), "", false, nil)
}
