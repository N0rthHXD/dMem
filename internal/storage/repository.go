package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/consolidation"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/retrieval"
	"github.com/mattn/go-sqlite3"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrInvalidParent       = errors.New("invalid parent space")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrJobAlreadyActive    = errors.New("job already active")
	ErrQueueFull           = errors.New("job queue is full")
	ErrResourceExhausted   = errors.New("resource exhausted")
	ErrInvalidCursor       = errors.New("invalid cursor")
	ErrInvalidSearchQuery  = errors.New("invalid search query")
	ErrIndexDegraded       = errors.New("index degraded")
	ErrEmbeddingPolicy     = errors.New("embedding policy rejected")
	ErrPolicyDenied        = errors.New("policy denied")
)

type WriteIdentity struct {
	CallerID       string
	IdempotencyKey string
	RequestID      string
	Method         string
	Route          string
	RequestHash    string
}

type CreateSpaceParams struct {
	ParentSpaceID *string
	SpaceType     string
	ExternalID    string
	DisplayName   string
}

type IngestParams struct {
	SpaceID         string
	SourceType      string
	SourceName      string
	SourceEventID   *string
	SourceStreamKey string
	Sequence        *int64
	OccurredAt      string
	Content         domain.Content
	Metadata        map[string]any
}

type EventFilter struct {
	SpaceID       string
	SourceName    string
	OccurredFrom  string
	OccurredUntil string
	Limit         int
	Cursor        string
}

type Repository struct {
	store                              *Store
	runtime                            domain.Runtime
	queueSoftLimit                     int
	queueHardLimit                     int
	maxAttempts                        int
	processingVersion                  string
	processingGeneration               atomic.Int64
	processingPolicyGeneration         atomic.Int64
	processingCompatibilityIdentity    atomic.Value
	graphExtractionEnabled             atomic.Bool
	diskSoftFree                       uint64
	diskHardFree                       uint64
	embedding                          atomic.Pointer[embeddingRuntime]
	semanticBuildHook                  func()
	semanticActivateHook               func()
	semanticSearchErr                  error
	rankingWeights                     retrieval.Weights
	semanticCalibration                retrieval.Calibration
	graphPolicy                        retrieval.GraphPolicy
	minEntityConfidence                float64
	minFactConfidence                  float64
	graphBoostCap                      float64
	semanticWindowRecall               SemanticWindowRecall
	consolidationVersion               string
	consolidationMaxCandidates         int
	consolidationExactHashEnabled      bool
	consolidationSameSourceCosine      float64
	consolidationSameSubjectKindCosine float64
	consolidationClassifier            consolidation.Classifier
}

// SemanticWindowRecall is the optional approximate-nearest-neighbor extension
// point. Implementations return a bounded candidate window that the exact
// cosine path then reranks, so exact search remains the correctness baseline.
// It is nil by default and ANN is not implemented until measured scale requires
// it.
type SemanticWindowRecall func(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, policyGeneration int, compatibilityIdentity string, dimension int, queryBlob []byte, limit int) ([]semanticRecallItem, error)

// SetSemanticWindowRecall installs an optional ANN window recall hook.
func (r *Repository) SetSemanticWindowRecall(hook SemanticWindowRecall) {
	r.semanticWindowRecall = hook
}

type embeddingRuntime struct {
	Provider              provider.EmbeddingProvider
	ProviderIdentity      string
	ModelIdentity         string
	CompatibilityIdentity string
	ConfigurationIdentity string
	Generation            int
	PolicyGeneration      int
}

func (r *Repository) emb() *embeddingRuntime {
	return r.embedding.Load()
}

func (r *Repository) setEmbeddingProvider(p provider.EmbeddingProvider) {
	prev := *r.emb()
	prev.Provider = p
	r.embedding.Store(&prev)
}

func (r *Repository) ConfigureSemantic(embedding provider.EmbeddingProvider, providerIdentity, modelIdentity string) {
	base := providerIdentity + "/" + modelIdentity
	r.SetEmbedding(embedding, providerIdentity, modelIdentity, EmbeddingCompatibilityIdentity(base), base, 0, 0)
}

func (r *Repository) SetEmbedding(embedding provider.EmbeddingProvider, providerIdentity, modelIdentity, compatibilityIdentity, configurationIdentity string, generation, policyGeneration int) {
	r.embedding.Store(&embeddingRuntime{
		Provider:              embedding,
		ProviderIdentity:      providerIdentity,
		ModelIdentity:         modelIdentity,
		CompatibilityIdentity: compatibilityIdentity,
		ConfigurationIdentity: configurationIdentity,
		Generation:            generation,
		PolicyGeneration:      policyGeneration,
	})
}

func (r *Repository) SetGraphExtractionEnabled(enabled bool) {
	r.graphExtractionEnabled.Store(enabled)
}

func (r *Repository) graphExtractionOn() bool {
	return r.graphExtractionEnabled.Load()
}

func (r *Repository) SetProcessingGeneration(generation, policyGeneration int, compatibilityIdentity string) {
	r.processingGeneration.Store(int64(generation))
	r.processingPolicyGeneration.Store(int64(policyGeneration))
	if compatibilityIdentity != "" {
		r.processingCompatibilityIdentity.Store(compatibilityIdentity)
	}
}

func (r *Repository) currentProcessingGeneration() int {
	return int(r.processingGeneration.Load())
}

func (r *Repository) currentProcessingPolicyGeneration() int {
	return int(r.processingPolicyGeneration.Load())
}

func (r *Repository) embeddingIdentity() string {
	e := r.emb()
	return e.ProviderIdentity + "/" + e.ModelIdentity
}

func NewRepository(store *Store, runtime domain.Runtime, softLimit, hardLimit, maxAttempts int, processingVersion string) *Repository {
	r := &Repository{
		store: store, runtime: runtime, queueSoftLimit: softLimit, queueHardLimit: hardLimit,
		maxAttempts: maxAttempts, processingVersion: processingVersion,
		rankingWeights:       retrieval.DefaultWeights(),
		semanticCalibration:  retrieval.DefaultCalibration(),
		graphPolicy:          retrieval.GraphFeaturesOnly,
		minEntityConfidence:  0.60,
		minFactConfidence:    0.70,
		graphBoostCap:        0.08,
		consolidationVersion: "consolidation-v1", consolidationMaxCandidates: 32,
		consolidationExactHashEnabled: true, consolidationSameSourceCosine: 0.90, consolidationSameSubjectKindCosine: 0.94,
	}
	r.embedding.Store(&embeddingRuntime{})
	return r
}

// SetRetrievalCalibration installs the configured ranking, semantic
// calibration, and graph gate contracts.
func (r *Repository) SetRetrievalCalibration(weights retrieval.Weights, calibration retrieval.Calibration, graphPolicy retrieval.GraphPolicy, minEntityConfidence, minFactConfidence, graphBoostCap float64) {
	if weights.Version != "" {
		r.rankingWeights = weights
	}
	if calibration.Version != "" {
		r.semanticCalibration = calibration
	}
	if graphPolicy.Valid() {
		r.graphPolicy = graphPolicy
	}
	if minEntityConfidence >= 0 && minEntityConfidence <= 1 {
		r.minEntityConfidence = minEntityConfidence
	}
	if minFactConfidence >= 0 && minFactConfidence <= 1 {
		r.minFactConfidence = minFactConfidence
	}
	if graphBoostCap > 0 {
		r.graphBoostCap = graphBoostCap
		r.rankingWeights.GraphBoostGraphCap = graphBoostCap
	}
}

// SetConsolidation configures bounded candidate consolidation. The versioned
// identity binds the thresholds and candidate bound into consolidation records.
func (r *Repository) SetConsolidation(version string, maxCandidates int, exactHashEnabled bool, sameSourceEventCosine, sameSubjectKindCosine float64) {
	if version != "" {
		r.consolidationVersion = version
	}
	if maxCandidates > 0 {
		r.consolidationMaxCandidates = maxCandidates
	}
	r.consolidationExactHashEnabled = exactHashEnabled
	if sameSourceEventCosine >= 0 && sameSourceEventCosine <= 1 {
		r.consolidationSameSourceCosine = sameSourceEventCosine
	}
	if sameSubjectKindCosine >= 0 && sameSubjectKindCosine <= 1 {
		r.consolidationSameSubjectKindCosine = sameSubjectKindCosine
	}
}

func (r *Repository) consolidationRules() consolidationRules {
	version := r.consolidationVersion
	if version == "" {
		version = "consolidation-v1"
	}
	return consolidationRules{
		Version:               version,
		ExactHashEnabled:      r.consolidationExactHashEnabled,
		SameSourceEventCosine: r.consolidationSameSourceCosine,
		SameSubjectKindCosine: r.consolidationSameSubjectKindCosine,
	}
}

func (r *Repository) retrievalCalibration() (retrieval.Weights, retrieval.Calibration, retrieval.GraphPolicy) {
	weights := r.rankingWeights
	if weights.Version == "" {
		weights = retrieval.DefaultWeights()
	}
	calibration := r.semanticCalibration
	if calibration.Version == "" {
		calibration = retrieval.DefaultCalibration()
	}
	policy := r.graphPolicy
	if !policy.Valid() {
		policy = retrieval.GraphFeaturesOnly
	}
	return weights, calibration, policy
}

// BeginReadSnapshot starts a read-only transaction for consumers that must scan
// multiple authority tables from one SQLite snapshot.
func (r *Repository) BeginReadSnapshot(ctx context.Context) (*sql.Tx, error) {
	return r.store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

func (r *Repository) SetDiskFreeThresholds(soft, hard uint64) {
	r.diskSoftFree, r.diskHardFree = soft, hard
}

func (r *Repository) IndexState(ctx context.Context) (string, error) {
	return r.store.IndexState(ctx)
}

func (r *Repository) QueueStats(ctx context.Context) (int, int, error) {
	var depth int
	var oldest sql.NullString
	err := r.store.reader.QueryRowContext(ctx, `SELECT count(*), min(created_at) FROM jobs WHERE status IN ('pending','waiting','running','retrying') AND (job_type NOT IN ('embed_memory','rebuild_semantic') OR processing_version=?)`, r.embeddingIdentity()).Scan(&depth, &oldest)
	if err != nil || !oldest.Valid {
		return depth, 0, err
	}
	created, err := time.Parse(time.RFC3339Nano, oldest.String)
	if err != nil {
		return 0, 0, err
	}
	age := int(r.runtime.Now().UTC().Sub(created).Seconds())
	if age < 0 {
		age = 0
	}
	return depth, age, nil
}

func (r *Repository) QueueStatus(ctx context.Context) (string, int, int, error) {
	depth, age, err := r.QueueStats(ctx)
	if err != nil {
		return "unavailable", depth, age, err
	}
	state := "ready"
	if depth >= r.queueHardLimit {
		state = "unavailable"
	} else if depth >= r.queueSoftLimit {
		state = "degraded"
	}
	return state, depth, age, nil
}

func (r *Repository) StorageStatus(ctx context.Context) (string, string) {
	var version int
	if err := r.store.reader.QueryRowContext(ctx, `SELECT schema_version FROM schema_metadata WHERE singleton=1`).Scan(&version); err != nil {
		return "unavailable", "unknown"
	}
	if version != SchemaVersion {
		return "unavailable", "incompatible"
	}
	if state, _ := r.store.IntegrityStatus(); state == integrityFailed {
		return "unavailable", "compatible"
	}
	return "ready", "compatible"
}

// IntegrityStatus reports the background integrity check state.
func (r *Repository) IntegrityStatus() (string, error) {
	return r.store.IntegrityStatus()
}

func (r *Repository) CreateSpace(ctx context.Context, identity WriteIdentity, params CreateSpaceParams) (domain.Space, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.Space{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.Space{}, false, err
	} else if found {
		var result domain.Space
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return domain.Space{}, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.Space{}, false, err
	}

	if err := validateSpaceParent(ctx, tx, params.ParentSpaceID, params.SpaceType); err != nil {
		return domain.Space{}, false, err
	}
	spaceID, err := r.runtime.ID()
	if err != nil {
		return domain.Space{}, false, err
	}
	operationID, err := r.runtime.ID()
	if err != nil {
		return domain.Space{}, false, err
	}
	auditID, err := r.runtime.ID()
	if err != nil {
		return domain.Space{}, false, err
	}
	createdAt := formatTime(r.runtime.Now())
	result := domain.Space{SpaceID: spaceID, ParentSpaceID: params.ParentSpaceID, SpaceType: params.SpaceType, ExternalID: params.ExternalID, DisplayName: params.DisplayName, CreatedAt: createdAt}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES (?, ?, ?, ?, ?, ?)`, spaceID, params.ParentSpaceID, params.SpaceType, params.ExternalID, params.DisplayName, createdAt); err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
			return domain.Space{}, false, fmt.Errorf("%w: create space", ErrConflict)
		}
		return domain.Space{}, false, err
	}
	if err := r.persistCurrentSpacePolicies(ctx, tx, spaceID); err != nil {
		return domain.Space{}, false, err
	}
	body, _ := json.Marshal(result)
	if err := writeIdempotency(ctx, tx, identity, operationID, 201, body, createdAt); err != nil {
		return domain.Space{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?, ?, ?, ?, 'create_space', 'space', ?, 'succeeded', ?)`, auditID, operationID, identity.RequestID, identity.CallerID, spaceID, createdAt); err != nil {
		return domain.Space{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Space{}, false, err
	}
	return result, false, nil
}

func validateSpaceParent(ctx context.Context, tx *sql.Tx, parentID *string, childType string) error {
	allowed := map[string]map[string]bool{
		"agent": {"user": true}, "application": {"user": true}, "project": {"user": true},
		"task": {"project": true}, "session": {"project": true, "task": true},
	}
	if childType == "user" {
		if parentID != nil {
			return ErrInvalidParent
		}
		return nil
	}
	if parentID == nil {
		return ErrInvalidParent
	}
	var parentType string
	if err := tx.QueryRowContext(ctx, "SELECT space_type FROM memory_spaces WHERE space_id = ?", *parentID).Scan(&parentType); err != nil {
		return ErrInvalidParent
	}
	if !allowed[childType][parentType] {
		return ErrInvalidParent
	}
	return nil
}

func (r *Repository) GetSpace(ctx context.Context, id string) (domain.Space, error) {
	var result domain.Space
	err := r.store.reader.QueryRowContext(ctx, `SELECT space_id, parent_space_id, space_type, external_id, display_name, created_at FROM memory_spaces WHERE space_id = ?`, id).Scan(
		&result.SpaceID, &result.ParentSpaceID, &result.SpaceType, &result.ExternalID, &result.DisplayName, &result.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Space{}, ErrNotFound
	}
	return result, err
}

func (r *Repository) ListSpaces(ctx context.Context, parentID *string, limit int, cursor string) (domain.SpacePage, error) {
	query := `SELECT space_id, parent_space_id, space_type, external_id, display_name, created_at FROM memory_spaces WHERE 1=1`
	args := []any{}
	if parentID != nil {
		query += " AND parent_space_id = ?"
		args = append(args, *parentID)
	}
	if cursor != "" {
		query += " AND space_id > ?"
		args = append(args, cursor)
	}
	query += " ORDER BY space_id ASC LIMIT ?"
	args = append(args, limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.SpacePage{}, err
	}
	defer rows.Close()
	page := domain.SpacePage{Items: []domain.Space{}}
	for rows.Next() {
		var item domain.Space
		if err := rows.Scan(&item.SpaceID, &item.ParentSpaceID, &item.SpaceType, &item.ExternalID, &item.DisplayName, &item.CreatedAt); err != nil {
			return domain.SpacePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > limit {
		next := page.Items[limit-1].SpaceID
		page.NextCursor = &next
		page.Items = page.Items[:limit]
	}
	return page, rows.Err()
}

func (r *Repository) IngestEvent(ctx context.Context, identity WriteIdentity, params IngestParams) (domain.IngestResponse, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.IngestResponse{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.IngestResponse{}, false, err
	} else if found {
		var result domain.IngestResponse
		if status != 202 || json.Unmarshal([]byte(body), &result) != nil {
			return domain.IngestResponse{}, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.IngestResponse{}, false, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM memory_spaces WHERE space_id = ?", params.SpaceID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return domain.IngestResponse{}, false, ErrNotFound
	} else if err != nil {
		return domain.IngestResponse{}, false, err
	}
	var activeJobs, backlog int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(status IN ('pending','waiting','retrying')), 0) FROM jobs WHERE status IN ('pending','waiting','running','retrying') AND (job_type NOT IN ('embed_memory','rebuild_semantic') OR processing_version=?)`, r.embeddingIdentity()).Scan(&activeJobs, &backlog); err != nil {
		return domain.IngestResponse{}, false, err
	}
	if activeJobs >= r.queueHardLimit {
		return domain.IngestResponse{}, false, ErrQueueFull
	}
	jobStatus, responseStatus := "pending", "accepted"
	if backlog >= r.queueSoftLimit {
		jobStatus, responseStatus = "waiting", "deferred"
	}

	ids := make([]string, 5)
	for i := range ids {
		ids[i], err = r.runtime.ID()
		if err != nil {
			return domain.IngestResponse{}, false, err
		}
	}
	eventID, contentID, operationID, jobID, auditID := ids[0], ids[1], ids[2], ids[3], ids[4]
	now := formatTime(r.runtime.Now())
	contentText, referenceURI, contentHash, sizeBytes, err := storedContent(params.Content)
	if err != nil {
		return domain.IngestResponse{}, false, err
	}
	metadata, err := json.Marshal(params.Metadata)
	if err != nil {
		return domain.IngestResponse{}, false, err
	}
	streamID, err := resolveSourceStream(ctx, tx, now, params.SpaceID, params.SourceType, params.SourceName, params.SourceStreamKey)
	if err != nil {
		return domain.IngestResponse{}, false, err
	}
	sequence, err := eventSequence(ctx, tx, streamID, params.Sequence)
	if err != nil {
		return domain.IngestResponse{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO raw_contents(content_id, content_kind, content_text, reference_uri, media_type, content_hash, size_bytes, declared_size, declared_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, contentID, params.Content.Kind, contentText, referenceURI, params.Content.MediaType, contentHash, sizeBytes, params.Content.DeclaredSize, nullableString(params.Content.DeclaredHash), now); err != nil {
		return domain.IngestResponse{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO raw_events(event_id, operation_id, space_id, content_id, source_type, source_name, source_event_id, source_stream_id, sequence, caller_id, occurred_at, received_at, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, eventID, operationID, params.SpaceID, contentID, params.SourceType, params.SourceName, params.SourceEventID, streamID, sequence, identity.CallerID, params.OccurredAt, now, string(metadata), now); err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
			return domain.IngestResponse{}, false, fmt.Errorf("%w: duplicate source stream sequence", ErrConflict)
		}
		return domain.IngestResponse{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES (?, ?, 'extract_memory', 'raw_event', ?, ?, ?, ?, ?, ?)`, jobID, operationID, eventID, r.processingVersion, jobStatus, r.maxAttempts, now, now); err != nil {
		return domain.IngestResponse{}, false, err
	}
	result := domain.IngestResponse{EventID: eventID, OperationID: operationID, Status: responseStatus, JobIDs: []string{jobID}, AcceptedAt: now}
	body, _ := json.Marshal(result)
	if err := writeIdempotency(ctx, tx, identity, operationID, 202, body, now); err != nil {
		return domain.IngestResponse{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?, ?, ?, ?, 'ingest_event', 'raw_event', ?, 'succeeded', ?)`, auditID, operationID, identity.RequestID, identity.CallerID, eventID, now); err != nil {
		return domain.IngestResponse{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.IngestResponse{}, false, err
	}
	return result, false, nil
}

func storedContent(content domain.Content) (any, any, string, int, error) {
	var contentText any
	var referenceURI any
	var canonical []byte
	switch content.Kind {
	case "text":
		contentText = content.Text
		canonical = []byte(content.Text)
	case "json":
		encoded, err := json.Marshal(content.Value)
		if err != nil {
			return nil, nil, "", 0, err
		}
		contentText, canonical = string(encoded), encoded
	case "reference":
		referenceURI = content.ReferenceURI
		canonical, _ = json.Marshal(struct {
			ReferenceURI string  `json:"reference_uri"`
			MediaType    string  `json:"media_type"`
			DeclaredSize *int64  `json:"declared_size"`
			DeclaredHash *string `json:"declared_hash"`
		}{content.ReferenceURI, content.MediaType, content.DeclaredSize, stringPointer(content.DeclaredHash)})
	default:
		return nil, nil, "", 0, errors.New("unsupported content kind")
	}
	hash := sha256.Sum256(canonical)
	return contentText, referenceURI, hex.EncodeToString(hash[:]), len(canonical), nil
}

func (r *Repository) GetEvent(ctx context.Context, id string) (domain.RawEvent, error) {
	return scanEvent(r.store.reader.QueryRowContext(ctx, eventSelect+" WHERE e.event_id = ?", id))
}

const eventSelect = `SELECT e.event_id, e.operation_id, e.caller_id, e.space_id, e.source_type, e.source_name, e.source_event_id, e.source_stream_id, e.sequence, e.occurred_at, e.received_at, e.processing_status, c.content_kind, c.media_type, c.content_text, c.reference_uri, c.declared_size, c.declared_hash, e.metadata_json FROM raw_events e JOIN raw_contents c ON c.content_id = e.content_id`

type rowScanner interface{ Scan(...any) error }

func scanEvent(row rowScanner) (domain.RawEvent, error) {
	var event domain.RawEvent
	var text, reference, declaredHash sql.NullString
	var declaredSize sql.NullInt64
	var sourceStreamID sql.NullString
	var sequence sql.NullInt64
	var metadata string
	err := row.Scan(&event.EventID, &event.OperationID, &event.CallerID, &event.SpaceID, &event.SourceType, &event.SourceName, &event.SourceEventID, &sourceStreamID, &sequence, &event.OccurredAt, &event.ReceivedAt, &event.ProcessingStatus, &event.Content.Kind, &event.Content.MediaType, &text, &reference, &declaredSize, &declaredHash, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RawEvent{}, ErrNotFound
	}
	if err != nil {
		return domain.RawEvent{}, err
	}
	if sourceStreamID.Valid {
		event.SourceStreamID = &sourceStreamID.String
	}
	if sequence.Valid {
		event.Sequence = &sequence.Int64
	}
	event.Content.Text, event.Content.ReferenceURI, event.Content.DeclaredHash = text.String, reference.String, declaredHash.String
	if declaredSize.Valid {
		event.Content.DeclaredSize = &declaredSize.Int64
	}
	if event.Content.Kind == "json" {
		decoder := json.NewDecoder(strings.NewReader(text.String))
		decoder.UseNumber()
		_ = decoder.Decode(&event.Content.Value)
		event.Content.Text = ""
	}
	decoder := json.NewDecoder(strings.NewReader(metadata))
	decoder.UseNumber()
	if err := decoder.Decode(&event.Metadata); err != nil {
		return domain.RawEvent{}, err
	}
	return event, nil
}

func (r *Repository) ListEvents(ctx context.Context, filter EventFilter) (domain.EventPage, error) {
	query := eventSelect + " WHERE e.space_id = ?"
	args := []any{filter.SpaceID}
	if filter.SourceName != "" {
		query += " AND e.source_name = ?"
		args = append(args, filter.SourceName)
	}
	if filter.OccurredFrom != "" {
		query += " AND e.occurred_at >= ?"
		args = append(args, filter.OccurredFrom)
	}
	if filter.OccurredUntil != "" {
		query += " AND e.occurred_at < ?"
		args = append(args, filter.OccurredUntil)
	}
	if filter.Cursor != "" {
		var cursor struct{ Time, ID string }
		decoded, err := base64.RawURLEncoding.DecodeString(filter.Cursor)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Time == "" || cursor.ID == "" {
			return domain.EventPage{}, ErrInvalidCursor
		}
		query += " AND (e.occurred_at < ? OR (e.occurred_at = ? AND e.event_id < ?))"
		args = append(args, cursor.Time, cursor.Time, cursor.ID)
	}
	query += " ORDER BY e.occurred_at DESC, e.event_id DESC LIMIT ?"
	args = append(args, filter.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.EventPage{}, err
	}
	defer rows.Close()
	page := domain.EventPage{Items: []domain.RawEvent{}}
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return domain.EventPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		encoded, _ := json.Marshal(struct{ Time, ID string }{last.OccurredAt, last.EventID})
		next := base64.RawURLEncoding.EncodeToString(encoded)
		page.NextCursor, page.Items = &next, page.Items[:filter.Limit]
	}
	return page, rows.Err()
}

func (r *Repository) ReplayEvent(ctx context.Context, identity WriteIdentity, eventID string) (domain.JobReference, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.JobReference{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.JobReference{}, false, err
	} else if found {
		var result domain.JobReference
		if status != 202 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.JobReference{}, false, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM raw_events WHERE event_id = ?", eventID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return domain.JobReference{}, false, ErrNotFound
	} else if err != nil {
		return domain.JobReference{}, false, err
	}
	var existingID, existingOperationID, existingStatus string
	err = tx.QueryRowContext(ctx, `SELECT job_id,operation_id,status FROM jobs WHERE job_type='extract_memory' AND target_type='raw_event' AND target_id=? AND processing_version=? ORDER BY created_at DESC,job_id DESC LIMIT 1`, eventID, r.processingVersion).Scan(&existingID, &existingOperationID, &existingStatus)
	if err == nil {
		result := domain.JobReference{OperationID: existingOperationID, JobID: existingID}
		body, _ := json.Marshal(result)
		if err := writeIdempotency(ctx, tx, identity, existingOperationID, 202, body, formatTime(r.runtime.Now())); err != nil {
			return domain.JobReference{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.JobReference{}, false, err
		}
		return result, existingStatus == "succeeded" || existingStatus == "failed" || existingStatus == "cancelled" || existingStatus == "expired", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.JobReference{}, false, err
	}
	jobID, err := r.runtime.ID()
	if err != nil {
		return domain.JobReference{}, false, err
	}
	operationID, err := r.runtime.ID()
	if err != nil {
		return domain.JobReference{}, false, err
	}
	auditID, err := r.runtime.ID()
	if err != nil {
		return domain.JobReference{}, false, err
	}
	now := formatTime(r.runtime.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id, operation_id, job_type, target_type, target_id, processing_version, status, max_attempts, next_run_at, created_at) VALUES (?, ?, 'extract_memory', 'raw_event', ?, ?, 'pending', ?, ?, ?)`, jobID, operationID, eventID, r.processingVersion, r.maxAttempts, now, now); err != nil {
		return domain.JobReference{}, false, err
	}
	result := domain.JobReference{OperationID: operationID, JobID: jobID}
	body, _ := json.Marshal(result)
	if err := writeIdempotency(ctx, tx, identity, operationID, 202, body, now); err != nil {
		return domain.JobReference{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id, operation_id, request_id, caller_id, action, target_type, target_id, outcome, created_at) VALUES (?, ?, ?, ?, 'replay_event', 'raw_event', ?, 'succeeded', ?)`, auditID, operationID, identity.RequestID, identity.CallerID, eventID, now); err != nil {
		return domain.JobReference{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.JobReference{}, false, err
	}
	return result, false, nil
}

func readIdempotency(ctx context.Context, tx *sql.Tx, identity WriteIdentity) (int, string, bool, error) {
	var hash, body string
	var status int
	err := tx.QueryRowContext(ctx, `SELECT request_hash, response_status, response_body FROM idempotency_records WHERE caller_id=? AND http_method=? AND normalized_route=? AND idempotency_key=?`, identity.CallerID, identity.Method, identity.Route, identity.IdempotencyKey).Scan(&hash, &status, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if hash != identity.RequestHash {
		return 0, "", false, ErrIdempotencyConflict
	}
	return status, body, true, nil
}

func writeIdempotency(ctx context.Context, tx *sql.Tx, identity WriteIdentity, operationID string, status int, body []byte, now string) error {
	id := sha256.Sum256([]byte(identity.CallerID + "\x00" + identity.Method + "\x00" + identity.Route + "\x00" + identity.IdempotencyKey))
	_, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(idempotency_id, caller_id, http_method, normalized_route, idempotency_key, request_hash, operation_id, response_status, response_body, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, hex.EncodeToString(id[:]), identity.CallerID, identity.Method, identity.Route, identity.IdempotencyKey, identity.RequestHash, operationID, status, string(body), now)
	return err
}

func HashRequest(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
