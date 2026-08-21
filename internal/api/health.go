package api

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/N0rthHXD/dMem/internal/storage"
)

type Health struct {
	ProcessAlive        bool   `json:"process_alive"`
	Mode                string `json:"mode"`
	Ready               bool   `json:"ready"`
	AcceptingWrites     bool   `json:"accepting_writes"`
	QueryAvailable      bool   `json:"query_available"`
	StorageHealthy      bool   `json:"storage_healthy"`
	StorageWritable     bool   `json:"storage_writable"`
	SchemaCompatibility string `json:"schema_compatibility"`
	FTSState            string `json:"fts_state"`
	FTSFreshness        string `json:"fts_freshness"`
	ProcessorState      string `json:"processor_state"`
	JobQueueDepth       int    `json:"job_queue_depth"`
	OldestJobAgeSeconds int    `json:"oldest_job_age_seconds"`
	DiskPressure        string `json:"disk_pressure"`
}

type Handler struct {
	ready          atomic.Bool
	ftsState       string
	repository     *storage.Repository
	maxRequestSize int64
	id             func() (string, error)
}

func NewHandler(ftsState string) *Handler {
	h := &Handler{ftsState: ftsState}
	h.ready.Store(true)
	return h
}

func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", h.health)
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
	mux.HandleFunc("GET /v1/jobs/{job_id}", h.job)
	mux.HandleFunc("POST /v1/jobs/{job_id}/{action}", h.job)
	mux.HandleFunc("POST /v1/search", h.search)
	mux.HandleFunc("POST /v1/indexes/fts/rebuild", h.rebuildFTS)
	mux.HandleFunc("/", h.notFound)
	return mux
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ready := h.ready.Load()
	mode := "healthy"
	if !ready {
		mode = "unavailable"
	}
	queueDepth, oldestAge := 0, 0
	ftsState := h.ftsState
	storageHealthy := ready
	diskPressure := "normal"
	if h.repository != nil {
		if depth, age, err := h.repository.QueueStats(r.Context()); err == nil {
			queueDepth, oldestAge = depth, age
		} else {
			storageHealthy = false
		}
		if state, err := h.repository.IndexState(r.Context()); err == nil {
			ftsState = state
		} else {
			storageHealthy = false
		}
		if pressure, err := h.repository.DiskPressure(); err == nil {
			diskPressure = pressure
		} else {
			storageHealthy = false
		}
	}
	if !storageHealthy {
		mode = "unavailable"
	} else if diskPressure == "hard" {
		mode = "read_only"
	} else if ftsState != "ready" {
		mode = "degraded"
	} else if diskPressure == "soft" {
		mode = "degraded"
	}
	acceptingWrites := ready && storageHealthy && diskPressure != "hard"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Health{
		ProcessAlive:        true,
		Mode:                mode,
		Ready:               ready,
		AcceptingWrites:     acceptingWrites,
		QueryAvailable:      ready && storageHealthy,
		StorageHealthy:      storageHealthy,
		StorageWritable:     acceptingWrites,
		SchemaCompatibility: "compatible",
		FTSState:            ftsState,
		FTSFreshness:        map[bool]string{true: "current", false: "stale"}[ftsState == "ready"],
		ProcessorState:      "not_configured",
		JobQueueDepth:       queueDepth,
		OldestJobAgeSeconds: oldestAge,
		DiskPressure:        diskPressure,
	})
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	h.writeError(w, http.StatusNotFound, "not_found", "route not found", h.requestID(), "", false, nil)
}
