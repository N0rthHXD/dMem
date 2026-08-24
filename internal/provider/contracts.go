package provider

import (
	"context"
	"time"
)

type EmbeddingProvider interface {
	Embed(context.Context, EmbeddingRequest) (EmbeddingResult, error)
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
	processorIdentity  string
	processingVersion  string
	jobID              string
	attempt            int
	target             Event
	context            []Event
	contextMemories    []Memory
	allowedMemoryTypes []string
	maxCandidates      int
	maxContentChars    int
}

func NewProcessRequest(processorIdentity, processingVersion, jobID string, attempt int, target Event, contextEvents []Event, contextMemories []Memory, allowedMemoryTypes []string, maxCandidates, maxContentChars int) ProcessRequest {
	return ProcessRequest{
		processorIdentity:  processorIdentity,
		processingVersion:  processingVersion,
		jobID:              jobID,
		attempt:            attempt,
		target:             target,
		context:            append([]Event(nil), contextEvents...),
		contextMemories:    append([]Memory(nil), contextMemories...),
		allowedMemoryTypes: append([]string(nil), allowedMemoryTypes...),
		maxCandidates:      maxCandidates,
		maxContentChars:    maxContentChars,
	}
}

type Memory struct {
	memoryID, revisionID, spaceID, content, state, sensitivity string
	validFrom, validUntil                                      *time.Time
}

func NewMemory(memoryID, revisionID, spaceID, content, state, sensitivity string, validFrom, validUntil *time.Time) Memory {
	return Memory{memoryID: memoryID, revisionID: revisionID, spaceID: spaceID, content: content, state: state, sensitivity: sensitivity, validFrom: cloneTime(validFrom), validUntil: cloneTime(validUntil)}
}

func (m Memory) MemoryID() string       { return m.memoryID }
func (m Memory) RevisionID() string     { return m.revisionID }
func (m Memory) SpaceID() string        { return m.spaceID }
func (m Memory) Content() string        { return m.content }
func (m Memory) State() string          { return m.state }
func (m Memory) Sensitivity() string    { return m.sensitivity }
func (m Memory) ValidFrom() *time.Time  { return cloneTime(m.validFrom) }
func (m Memory) ValidUntil() *time.Time { return cloneTime(m.validUntil) }

type Evidence struct {
	eventID, evidenceType  string
	startOffset, endOffset *int64
}

func (e Evidence) EventID() string      { return e.eventID }
func (e Evidence) EvidenceType() string { return e.evidenceType }
func (e Evidence) StartOffset() *int64  { return cloneInt64(e.startOffset) }
func (e Evidence) EndOffset() *int64    { return cloneInt64(e.endOffset) }

type Candidate struct {
	content     string
	memoryType  string
	spaceID     string
	validFrom   *time.Time
	validUntil  *time.Time
	confidence  float64
	sensitivity string
	evidence    []Evidence
	suggestion  string
	relatedIDs  []string
}

func (c Candidate) Content() string            { return c.content }
func (c Candidate) MemoryType() string         { return c.memoryType }
func (c Candidate) SpaceID() string            { return c.spaceID }
func (c Candidate) ValidFrom() *time.Time      { return cloneTime(c.validFrom) }
func (c Candidate) ValidUntil() *time.Time     { return cloneTime(c.validUntil) }
func (c Candidate) Confidence() float64        { return c.confidence }
func (c Candidate) Sensitivity() string        { return c.sensitivity }
func (c Candidate) Evidence() []Evidence       { return append([]Evidence(nil), c.evidence...) }
func (c Candidate) SuggestionKind() string     { return c.suggestion }
func (c Candidate) RelatedMemoryIDs() []string { return append([]string(nil), c.relatedIDs...) }

type Usage struct {
	inputTokens  int
	outputTokens int
}

func (u Usage) InputTokens() int  { return u.inputTokens }
func (u Usage) OutputTokens() int { return u.outputTokens }

type ProcessResult struct {
	category   ProcessCategory
	candidates []Candidate
	usage      Usage
	requestID  string
}

func (r ProcessResult) Category() ProcessCategory { return r.category }
func (r ProcessResult) Candidates() []Candidate   { return append([]Candidate(nil), r.candidates...) }
func (r ProcessResult) Usage() Usage              { return r.usage }
func (r ProcessResult) ProviderRequestID() string { return r.requestID }

type EmbeddingRequest struct {
	spaceID     string
	contentType string
	sensitivity string
	sourceTypes []string
	inputs      []string
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
