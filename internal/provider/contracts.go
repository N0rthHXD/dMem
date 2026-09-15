package provider

import (
	"context"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type EmbeddingProvider interface {
	Embed(context.Context, EmbeddingRequest) (EmbeddingResult, error)
}

type SecretReference struct {
	Source string
	Name   string
}

type EmbeddingOptions struct {
	Dimensions        int
	QueryInstructions string
}

type Limits struct {
	MaxEgressInputChars    int
	MaxAggregateInputChars int
	MaxContextItems        int
	MaxCandidateChars      int
	MaxTotalCandidateChars int
	MaxResponseBytes       int64
	RequestTimeout         time.Duration
	MaxConcurrent          int
	RequestsPerMinute      int
}

type PolicyAssignment struct {
	Selector           string
	SpaceID            string
	SpaceType          string
	ExternalID         string
	Effect             string
	IncludeDescendants bool
	Priority           int
}

type Policy struct {
	Default       string
	Query         string
	ContentTypes  []string
	SourceTypes   []string
	Sensitivities []string
	Assignments   []PolicyAssignment
}

type ProcessCategory string

const (
	ProcessNoMemory            ProcessCategory = "no_memory"
	ProcessCandidatesExtracted ProcessCategory = "candidates_extracted"
)

type Content struct {
	kind         string
	mediaType    string
	text         string
	declaredSize int64
	declaredHash string
}

func NewTextContent(mediaType, text string) Content {
	return Content{kind: "text", mediaType: mediaType, text: text}
}

func NewJSONContent(text string) Content {
	return Content{kind: "json", mediaType: "application/json", text: text}
}

func NewReferenceContent(mediaType string, declaredSize int64, declaredHash string) Content {
	return Content{kind: "reference", mediaType: mediaType, declaredSize: declaredSize, declaredHash: declaredHash}
}

func (c Content) Kind() string         { return c.kind }
func (c Content) MediaType() string    { return c.mediaType }
func (c Content) Text() string         { return c.text }
func (c Content) DeclaredSize() int64  { return c.declaredSize }
func (c Content) DeclaredHash() string { return c.declaredHash }

type Event struct {
	eventID     string
	spaceID     string
	sourceType  string
	sourceName  string
	sensitivity string
	occurredAt  time.Time
	content     Content
}

func NewEvent(eventID, spaceID, sourceType, sourceName, sensitivity string, occurredAt time.Time, content Content) Event {
	return Event{eventID: eventID, spaceID: spaceID, sourceType: sourceType, sourceName: sourceName, sensitivity: sensitivity, occurredAt: occurredAt, content: content}
}

func (e Event) EventID() string       { return e.eventID }
func (e Event) SpaceID() string       { return e.spaceID }
func (e Event) SourceType() string    { return e.sourceType }
func (e Event) SourceName() string    { return e.sourceName }
func (e Event) Sensitivity() string   { return e.sensitivity }
func (e Event) OccurredAt() time.Time { return e.occurredAt }
func (e Event) Content() Content      { return e.content }

type ProcessRequest struct {
	processorIdentity string
	processingVersion string
	jobID             string
	attempt           int
	target            Event
	context           []Event
	contextMemories   []Memory
	allowedKinds      []domain.MemoryKind
	maxCandidates     int
	maxCandidateChars int
	maxTotalChars     int
}

func NewProcessRequest(processorIdentity, processingVersion, jobID string, attempt int, target Event, contextEvents []Event, contextMemories []Memory, allowedKinds []domain.MemoryKind, maxCandidates, maxCandidateChars int, maxTotalChars ...int) ProcessRequest {
	totalChars := maxCandidateChars
	if len(maxTotalChars) > 0 {
		totalChars = maxTotalChars[0]
	}
	return ProcessRequest{
		processorIdentity: processorIdentity,
		processingVersion: processingVersion,
		jobID:             jobID,
		attempt:           attempt,
		target:            target,
		context:           append([]Event(nil), contextEvents...),
		contextMemories:   append([]Memory(nil), contextMemories...),
		allowedKinds:      append([]domain.MemoryKind(nil), allowedKinds...),
		maxCandidates:     maxCandidates,
		maxCandidateChars: maxCandidateChars,
		maxTotalChars:     totalChars,
	}
}

type Memory struct {
	memoryID, revisionID, spaceID, content, state, sensitivity string
	validFrom, validUntil                                      *time.Time
	proposalCapable                                            bool
}

func NewMemory(memoryID, revisionID, spaceID, content, state, sensitivity string, validFrom, validUntil *time.Time) Memory {
	return NewMemoryWithProposalCapability(memoryID, revisionID, spaceID, content, state, sensitivity, true, validFrom, validUntil)
}
func NewMemoryWithProposalCapability(memoryID, revisionID, spaceID, content, state, sensitivity string, proposalCapable bool, validFrom, validUntil *time.Time) Memory {
	return Memory{memoryID: memoryID, revisionID: revisionID, spaceID: spaceID, content: content, state: state, sensitivity: sensitivity, proposalCapable: proposalCapable, validFrom: cloneTime(validFrom), validUntil: cloneTime(validUntil)}
}

func (m Memory) MemoryID() string       { return m.memoryID }
func (m Memory) RevisionID() string     { return m.revisionID }
func (m Memory) SpaceID() string        { return m.spaceID }
func (m Memory) Content() string        { return m.content }
func (m Memory) State() string          { return m.state }
func (m Memory) Sensitivity() string    { return m.sensitivity }
func (m Memory) ProposalCapable() bool  { return m.proposalCapable }
func (m Memory) ValidFrom() *time.Time  { return cloneTime(m.validFrom) }
func (m Memory) ValidUntil() *time.Time { return cloneTime(m.validUntil) }

type Usage struct {
	inputTokens  int
	outputTokens int
}

func (u Usage) InputTokens() int  { return u.inputTokens }
func (u Usage) OutputTokens() int { return u.outputTokens }

type ProcessResult struct {
	category        ProcessCategory
	revisions       []domain.MemoryRevision
	usage           Usage
	requestID       string
	omittedCount    int
	omissionSummary map[string]int
}

func (r ProcessResult) Category() ProcessCategory { return r.category }
func (r ProcessResult) Revisions() []domain.MemoryRevision {
	return append([]domain.MemoryRevision(nil), r.revisions...)
}
func (r ProcessResult) Usage() Usage              { return r.usage }
func (r ProcessResult) ProviderRequestID() string { return r.requestID }
func (r ProcessResult) OmittedContextCount() int  { return r.omittedCount }
func (r ProcessResult) OmissionSummary() map[string]int {
	result := make(map[string]int, len(r.omissionSummary))
	for reason, count := range r.omissionSummary {
		result[reason] = count
	}
	return result
}

type GraphExtractRequest struct {
	processorIdentity string
	processingVersion string
	jobID             string
	attempt           int
	spaceID           string
	memoryID          string
	revisionID        string
	content           string
	sensitivity       string
	contextMemories   []Memory
	maxEntities       int
	maxFacts          int
	maxRelations      int
	maxContentChars   int
}

func NewGraphExtractRequest(processorIdentity, processingVersion, jobID string, attempt int, spaceID, memoryID, revisionID, content, sensitivity string, contextMemories []Memory, maxEntities, maxFacts, maxRelations, maxContentChars int) GraphExtractRequest {
	return GraphExtractRequest{
		processorIdentity: processorIdentity, processingVersion: processingVersion, jobID: jobID, attempt: attempt,
		spaceID: spaceID, memoryID: memoryID, revisionID: revisionID, content: content, sensitivity: sensitivity,
		contextMemories: append([]Memory(nil), contextMemories...),
		maxEntities:     maxEntities, maxFacts: maxFacts, maxRelations: maxRelations, maxContentChars: maxContentChars,
	}
}

func (r GraphExtractRequest) ProcessorIdentity() string { return r.processorIdentity }
func (r GraphExtractRequest) ProcessingVersion() string { return r.processingVersion }
func (r GraphExtractRequest) JobID() string             { return r.jobID }
func (r GraphExtractRequest) Attempt() int              { return r.attempt }
func (r GraphExtractRequest) SpaceID() string           { return r.spaceID }
func (r GraphExtractRequest) MemoryID() string          { return r.memoryID }
func (r GraphExtractRequest) RevisionID() string        { return r.revisionID }
func (r GraphExtractRequest) Content() string           { return r.content }
func (r GraphExtractRequest) Sensitivity() string       { return r.sensitivity }
func (r GraphExtractRequest) ContextMemories() []Memory {
	return append([]Memory(nil), r.contextMemories...)
}
func (r GraphExtractRequest) MaxEntities() int     { return r.maxEntities }
func (r GraphExtractRequest) MaxFacts() int        { return r.maxFacts }
func (r GraphExtractRequest) MaxRelations() int    { return r.maxRelations }
func (r GraphExtractRequest) MaxContentChars() int { return r.maxContentChars }

type GraphExtractResult struct {
	result    TypedGraphResult
	usage     Usage
	requestID string
}

func (r GraphExtractResult) Entities() []TypedGraphEntity {
	return append([]TypedGraphEntity(nil), r.result.Entities...)
}
func (r GraphExtractResult) Mentions() []TypedGraphMention {
	return append([]TypedGraphMention(nil), r.result.Mentions...)
}
func (r GraphExtractResult) Events() []TypedGraphEvent {
	return append([]TypedGraphEvent(nil), r.result.Events...)
}
func (r GraphExtractResult) Facts() []TypedGraphFact {
	return append([]TypedGraphFact(nil), r.result.Facts...)
}
func (r GraphExtractResult) Relations() []TypedGraphRelation {
	return append([]TypedGraphRelation(nil), r.result.Relations...)
}
func (r GraphExtractResult) Usage() Usage              { return r.usage }
func (r GraphExtractResult) ProviderRequestID() string { return r.requestID }

type EmbeddingRequest struct {
	spaceID     string
	contentType string
	sensitivity string
	sourceTypes []string
	inputs      []string
	query       bool
}

func NewEmbeddingQueryRequest(spaceID, contentType, sensitivity string, sourceTypes, inputs []string) EmbeddingRequest {
	request := NewEmbeddingRequest(spaceID, contentType, sensitivity, sourceTypes, inputs)
	request.query = true
	return request
}

func NewEmbeddingRequest(spaceID, contentType, sensitivity string, sourceTypes, inputs []string) EmbeddingRequest {
	return EmbeddingRequest{
		spaceID: spaceID, contentType: contentType, sensitivity: sensitivity,
		sourceTypes: append([]string(nil), sourceTypes...), inputs: append([]string(nil), inputs...),
	}
}

func (r EmbeddingRequest) SpaceID() string     { return r.spaceID }
func (r EmbeddingRequest) ContentType() string { return r.contentType }
func (r EmbeddingRequest) Sensitivity() string { return r.sensitivity }
func (r EmbeddingRequest) SourceTypes() []string {
	return append([]string(nil), r.sourceTypes...)
}
func (r EmbeddingRequest) Inputs() []string { return append([]string(nil), r.inputs...) }

type EmbeddingResult struct {
	vectors   [][]float32
	usage     Usage
	requestID string
}

func NewEmbeddingResult(vectors [][]float32, requestID string) EmbeddingResult {
	result := EmbeddingResult{vectors: make([][]float32, len(vectors)), requestID: requestID}
	for i := range vectors {
		result.vectors[i] = append([]float32(nil), vectors[i]...)
	}
	return result
}

func (r EmbeddingResult) Vectors() [][]float32 {
	result := make([][]float32, len(r.vectors))
	for i := range r.vectors {
		result[i] = append([]float32(nil), r.vectors[i]...)
	}
	return result
}
func (r EmbeddingResult) Usage() Usage              { return r.usage }
func (r EmbeddingResult) ProviderRequestID() string { return r.requestID }

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
