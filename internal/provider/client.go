package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
)

const (
	KindOpenAICompatible  = "openai-compatible"
	maxEmbeddingBatch     = 128
	maxEmbeddingDimension = 65536
	maxIdentityChars      = 512
)

const processSystemPrompt = `You are a memory extraction processor for an agent system. Inspect the target event, optionally together with context events and context memories, and decide what durable memories should be recorded.

Respond with STRICT JSON ONLY (no markdown, no surrounding prose) matching this shape exactly:
{"category":"no_memory|candidates_extracted","candidates":[...]}

If nothing is worth remembering, return {"category":"no_memory","candidates":[]}.
Otherwise return "candidates_extracted" with 1 to max_candidates candidates.

Every candidate object MUST contain EXACTLY these fields and no others:
- "content": string. A self-contained declarative memory statement.
- "memory_type": string. One of the allowed_memory_types given in the input.
- "space_id": string. Exactly equal to the target event's space_id.
- "confidence": number between 0 and 1 inclusive.
- "sensitivity": "normal" or "sensitive".
- "suggestion_kind": one of "new", "duplicate", "supplement", "replacement", "conflict".
- "evidence": non-empty array. Each element has EXACTLY {"event_id","evidence_type","start_offset","end_offset"}. "event_id" must be the target event_id or a context event_id present in the input. "evidence_type" is "direct_statement", "tool_result", or "manual_reference". Set BOTH "start_offset" and "end_offset" to null to cite the whole event, or set both to 0-based rune offsets into that event's content text with start_offset < end_offset and end_offset not exceeding the text length. Never set only one of the two.
- "related_memory_ids": array of context memory ids this candidate relates to; use [] when there are none.

Optionally include "valid_from" and/or "valid_until" as RFC3339 strings only when an explicit time bound is present; "valid_until" must be after "valid_from".

Do not invent event_id or memory id values that were not provided in the input. Do not add any field beyond those listed. Do not request state changes or deletion.`

type ErrorClass string

const (
	ErrorDisabled      ErrorClass = "disabled"
	ErrorPolicy        ErrorClass = "policy_rejected"
	ErrorInvalidInput  ErrorClass = "invalid_input"
	ErrorTransient     ErrorClass = "transient_failure"
	ErrorPermanent     ErrorClass = "permanent_failure"
	ErrorInvalidOutput ErrorClass = "invalid_output"
)

type Error struct {
	class ErrorClass
	code  string
}

func (e *Error) Error() string     { return "provider request failed: " + e.code }
func (e *Error) Class() ErrorClass { return e.class }
func (e *Error) Code() string      { return e.code }

type Config struct {
	Enabled                    bool
	Kind                       string
	Endpoint                   string
	Model                      string
	SecretEnv                  string
	AllowedSpaces              []string
	AllowedContentTypes        []string
	AllowedSourceTypes         []string
	AllowedSensitivities       []string
	TelemetryDisabled          bool
	EgressPolicyVersion        string
	MaxContentChars            int
	MaxContextItems            int
	MaxResponseSize            int64
	RequestTimeout             time.Duration
	MaxConcurrent              int
	RequestsPerMinute          int
	EmbeddingDim               int
	EmbeddingQueryInstructions string
}

type Client struct {
	enabled           bool
	endpoint          string
	model             string
	secretEnv         string
	policyVersion     string
	spaces            map[string]struct{}
	contentTypes      map[string]struct{}
	sourceTypes       map[string]struct{}
	sensitivities     map[string]struct{}
	maxChars          int
	maxContext        int
	maxResponse       int64
	timeout           time.Duration
	http              *http.Client
	concurrency       chan struct{}
	rateMu            sync.Mutex
	nextRequest       time.Time
	requestGap        time.Duration
	embeddingDim      int
	queryInstructions string
	multimodalEmbed   bool
}

var _ EmbeddingProvider = (*Client)(nil)
var _ domain.AggregateProvider = (*Client)(nil)

func New(cfg Config, httpClient *http.Client) (*Client, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return &Client{enabled: false}, nil
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	spaces := makeSet(cfg.AllowedSpaces)
	contentTypes := makeSet(cfg.AllowedContentTypes)
	sourceTypes := makeSet(cfg.AllowedSourceTypes)
	sensitivities := makeSet(cfg.AllowedSensitivities)
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	httpCopy := *httpClient
	httpCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		enabled: true, endpoint: endpoint.String(), model: cfg.Model,
		secretEnv: cfg.SecretEnv, policyVersion: cfg.EgressPolicyVersion,
		spaces: spaces, contentTypes: contentTypes, sourceTypes: sourceTypes, sensitivities: sensitivities, maxChars: cfg.MaxContentChars,
		maxContext: cfg.MaxContextItems, maxResponse: cfg.MaxResponseSize, timeout: cfg.RequestTimeout,
		http: &httpCopy, concurrency: make(chan struct{}, cfg.MaxConcurrent),
		requestGap:   time.Minute / time.Duration(cfg.RequestsPerMinute),
		embeddingDim: cfg.EmbeddingDim, queryInstructions: cfg.EmbeddingQueryInstructions,
		multimodalEmbed: strings.TrimSuffix(endpoint.Path, "/") == "/api/v3/embeddings/multimodal",
	}, nil
}

func ValidateConfig(cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Kind != KindOpenAICompatible {
		return errors.New("provider: unsupported kind")
	}
	if !cfg.TelemetryDisabled {
		return errors.New("provider: telemetry must be disabled")
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("provider: endpoint must be a full HTTPS request URL")
	}
	if !boundedString(cfg.Model) || !validEnvName(cfg.SecretEnv) || !boundedString(cfg.EgressPolicyVersion) {
		return errors.New("provider: incomplete identity, secret reference, or policy version")
	}
	if cfg.MaxContentChars < 1 || cfg.MaxContentChars > 100000 || cfg.MaxContextItems < 0 || cfg.MaxContextItems > 100 || cfg.MaxResponseSize < 1024 || cfg.MaxResponseSize > 16*1024*1024 {
		return errors.New("provider: invalid content or response bounds")
	}
	if cfg.RequestTimeout <= 0 || cfg.RequestTimeout > 5*time.Minute || cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 64 || cfg.RequestsPerMinute < 1 || cfg.RequestsPerMinute > 60000 {
		return errors.New("provider: invalid request limits")
	}
	if cfg.EmbeddingDim < 0 || cfg.EmbeddingDim > maxEmbeddingDimension || utf8.RuneCountInString(cfg.EmbeddingQueryInstructions) > cfg.MaxContentChars {
		return errors.New("provider: invalid embedding options")
	}
	spaces := makeSet(cfg.AllowedSpaces)
	contentTypes := makeSet(cfg.AllowedContentTypes)
	sourceTypes := makeSet(cfg.AllowedSourceTypes)
	sensitivities := makeSet(cfg.AllowedSensitivities)
	if len(spaces) == 0 || len(contentTypes) == 0 || len(sourceTypes) == 0 || len(sensitivities) == 0 {
		return errors.New("provider: egress allowlists are required")
	}
	for sourceType := range sourceTypes {
		if !validSourceType(sourceType) {
			return errors.New("provider: invalid source type allowlist")
		}
	}
	for sensitivity := range sensitivities {
		if sensitivity != "normal" && sensitivity != "sensitive" {
			return errors.New("provider: invalid sensitivity allowlist")
		}
	}
	return nil
}

func (c *Client) Enabled() bool { return c != nil && c.enabled }
func (c *Client) Identity() string {
	if !c.Enabled() {
		return "none"
	}
	return KindOpenAICompatible + "/" + c.model
}
func (c *Client) ModelIdentity() string {
	if !c.Enabled() {
		return ""
	}
	return c.model
}
func (c *Client) EgressPolicyVersion() string {
	if !c.Enabled() {
		return ""
	}
	return c.policyVersion
}

func (c *Client) Process(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
	if !c.Enabled() {
		return ProcessResult{}, failure(ErrorDisabled, "provider_disabled")
	}
	if err := c.validateEvent(request.target); err != nil {
		return ProcessResult{}, err
	}
	if !boundedString(request.processorIdentity) || !boundedString(request.processingVersion) || !boundedString(request.jobID) || request.attempt < 1 || request.maxCandidates < 1 || request.maxCandidates > 100 || request.maxContentChars < 1 || request.maxContentChars > c.maxChars || len(request.allowedMemoryTypes) == 0 {
		return ProcessResult{}, failure(ErrorInvalidInput, "invalid_process_request")
	}
	for _, memoryType := range request.allowedMemoryTypes {
		if !boundedString(memoryType) {
			return ProcessResult{}, failure(ErrorInvalidInput, "invalid_process_request")
		}
	}
	remaining := request.maxContentChars
	if request.target.content.kind != "reference" {
		remaining -= utf8.RuneCountInString(request.target.content.text)
		if remaining < 0 {
			return ProcessResult{}, failure(ErrorInvalidInput, "content_out_of_bounds")
		}
	}
	contextEvents, err := c.minimizeContext(request.target.spaceID, request.context, remaining)
	if err != nil {
		return ProcessResult{}, err
	}
	for _, event := range contextEvents {
		if event.Content.Kind != "reference" {
			remaining -= utf8.RuneCountInString(event.Content.Text)
		}
	}
	contextMemories, err := c.minimizeMemories(request.contextMemories, remaining, c.maxContext)
	if err != nil {
		return ProcessResult{}, err
	}
	payload := chatRequest{Model: c.model, ResponseFormat: responseFormat{Type: "json_object"}}
	prompt := processPrompt{
		ProcessorIdentity: request.processorIdentity, ProcessingVersion: request.processingVersion,
		JobID: request.jobID, Attempt: request.attempt, Event: wireEvent(request.target),
		ContextEvents: contextEvents, ContextMemories: contextMemories, AllowedMemoryTypes: append([]string(nil), request.allowedMemoryTypes...), MaxCandidates: request.maxCandidates, MaxContentChars: request.maxContentChars,
	}
	promptJSON, err := json.Marshal(prompt)
	if err != nil {
		return ProcessResult{}, failure(ErrorInvalidInput, "invalid_process_request")
	}
	payload.Messages = []chatMessage{{Role: "system", Content: processSystemPrompt}, {Role: "user", Content: string(promptJSON)}}
	var response chatResponse
	requestID, err := c.post(ctx, payload, &response)
	if err != nil {
		return ProcessResult{}, err
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content == "" {
		return ProcessResult{}, failure(ErrorInvalidOutput, "malformed_chat_response")
	}
	var output processOutput
	if err := decodeSingleStrict([]byte(response.Choices[0].Message.Content), &output); err != nil {
		return ProcessResult{}, failure(ErrorInvalidOutput, "malformed_processor_output")
	}
	result, err := validateProcessOutput(output, request, response.Usage, requestID, request.maxContentChars)
	if err != nil {
		return ProcessResult{}, err
	}
	return result, nil
}

func (c *Client) Build(ctx context.Context, request domain.AggregateProviderInput) (domain.AggregateProviderResult, error) {
	if !c.Enabled() {
		return domain.AggregateProviderResult{}, failure(ErrorDisabled, "provider_disabled")
	}
	if _, ok := c.spaces[request.SpaceID]; !ok {
		return domain.AggregateProviderResult{}, failure(ErrorPolicy, "space_not_allowed")
	}
	if request.Kind != "scene" && request.Kind != "topic" || request.PolicyVersion == "" || request.ProcessingVersion == "" || len(request.Inputs) == 0 || len(request.Inputs) > c.maxContext {
		return domain.AggregateProviderResult{}, failure(ErrorInvalidInput, "invalid_aggregate_request")
	}
	total := 0
	maxSensitivity := "normal"
	for _, input := range request.Inputs {
		if input.SpaceID != request.SpaceID || input.Content == "" {
			return domain.AggregateProviderResult{}, failure(ErrorPolicy, "aggregate_scope_not_allowed")
		}
		if _, ok := c.sensitivities[input.Sensitivity]; !ok {
			return domain.AggregateProviderResult{}, failure(ErrorPolicy, "aggregate_sensitivity_not_allowed")
		}
		total += utf8.RuneCountInString(input.Content)
		if total > c.maxChars {
			return domain.AggregateProviderResult{}, failure(ErrorInvalidInput, "content_out_of_bounds")
		}
		if input.Sensitivity == "sensitive" {
			maxSensitivity = "sensitive"
		}
	}
	payload := chatRequest{Model: c.model, ResponseFormat: responseFormat{Type: "json_object"}, Messages: []chatMessage{{Role: "system", Content: "Return JSON with content and sensitivity. Summarize only the supplied revisions and do not change source memories."}}}
	prompt, _ := json.Marshal(request)
	payload.Messages = append(payload.Messages, chatMessage{Role: "user", Content: string(prompt)})
	var response chatResponse
	if _, err := c.post(ctx, payload, &response); err != nil {
		return domain.AggregateProviderResult{}, err
	}
	if len(response.Choices) != 1 {
		return domain.AggregateProviderResult{}, failure(ErrorInvalidOutput, "malformed_aggregate_response")
	}
	var output struct {
		Content     string `json:"content"`
		Sensitivity string `json:"sensitivity"`
	}
	if err := decodeSingleStrict([]byte(response.Choices[0].Message.Content), &output); err != nil || output.Content == "" || output.Sensitivity != "normal" && output.Sensitivity != "sensitive" || maxSensitivity == "sensitive" && output.Sensitivity != "sensitive" {
		return domain.AggregateProviderResult{}, failure(ErrorInvalidOutput, "malformed_aggregate_output")
	}
	return domain.AggregateProviderResult{Content: output.Content, Sensitivity: output.Sensitivity, ProviderIdentity: c.Identity(), ModelIdentity: c.ModelIdentity()}, nil
}

func (c *Client) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	if !c.Enabled() {
		return EmbeddingResult{}, failure(ErrorDisabled, "provider_disabled")
	}
	if _, ok := c.spaces[request.spaceID]; !ok {
		return EmbeddingResult{}, failure(ErrorPolicy, "space_not_allowed")
	}
	if _, ok := c.contentTypes[request.contentType]; !ok {
		return EmbeddingResult{}, failure(ErrorPolicy, "content_type_not_allowed")
	}
	if _, ok := c.sensitivities[request.sensitivity]; !ok {
		return EmbeddingResult{}, failure(ErrorPolicy, "sensitivity_not_allowed")
	}
	if len(request.sourceTypes) == 0 {
		return EmbeddingResult{}, failure(ErrorInvalidInput, "missing_embedding_source_types")
	}
	for _, sourceType := range request.sourceTypes {
		if _, ok := c.sourceTypes[sourceType]; !ok {
			return EmbeddingResult{}, failure(ErrorPolicy, "source_type_not_allowed")
		}
	}
	if len(request.inputs) == 0 || len(request.inputs) > maxEmbeddingBatch {
		return EmbeddingResult{}, failure(ErrorInvalidInput, "invalid_embedding_batch")
	}
	inputs := append([]string(nil), request.inputs...)
	if request.query && c.queryInstructions != "" {
		for i := range inputs {
			inputs[i] = c.queryInstructions + inputs[i]
		}
	}
	for _, input := range inputs {
		if input == "" || utf8.RuneCountInString(input) > c.maxChars {
			return EmbeddingResult{}, failure(ErrorInvalidInput, "embedding_input_out_of_bounds")
		}
	}
	if c.multimodalEmbed && len(inputs) != 1 {
		return EmbeddingResult{}, failure(ErrorInvalidInput, "multimodal_embedding_requires_single_input")
	}
	var response embeddingResponse
	var requestID string
	var err error
	if c.multimodalEmbed {
		var wireResponse multimodalEmbeddingResponse
		requestID, err = c.post(ctx, multimodalEmbeddingWireRequest{
			Model: c.model, Input: []multimodalEmbeddingInput{{Type: "text", Text: inputs[0]}}, Dimensions: c.embeddingDim,
		}, &wireResponse)
		response = embeddingResponse{Data: []embeddingWireItem{wireResponse.Data}, Usage: wireResponse.Usage}
	} else {
		requestID, err = c.post(ctx, embeddingWireRequest{Model: c.model, Input: inputs, Dimensions: c.embeddingDim}, &response)
	}
	if err != nil {
		return EmbeddingResult{}, err
	}
	if len(response.Data) != len(request.inputs) {
		return EmbeddingResult{}, failure(ErrorInvalidOutput, "embedding_count_mismatch")
	}
	vectors := make([][]float32, len(response.Data))
	dimension := 0
	seen := make([]bool, len(response.Data))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(vectors) || seen[item.Index] || len(item.Embedding) == 0 || len(item.Embedding) > maxEmbeddingDimension {
			return EmbeddingResult{}, failure(ErrorInvalidOutput, "malformed_embedding_response")
		}
		if dimension == 0 {
			dimension = len(item.Embedding)
		}
		if c.embeddingDim > 0 && len(item.Embedding) != c.embeddingDim {
			return EmbeddingResult{}, failure(ErrorInvalidOutput, "embedding_dimension_mismatch")
		}
		if len(item.Embedding) != dimension {
			return EmbeddingResult{}, failure(ErrorInvalidOutput, "embedding_dimension_mismatch")
		}
		for _, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
				return EmbeddingResult{}, failure(ErrorInvalidOutput, "non_finite_embedding")
			}
		}
		seen[item.Index] = true
		vectors[item.Index] = make([]float32, dimension)
		for i, value := range item.Embedding {
			vectors[item.Index][i] = float32(value)
		}
	}
	return EmbeddingResult{vectors: vectors, usage: usageFromWire(response.Usage), requestID: requestID}, nil
}

func (c *Client) validateEvent(event Event) error {
	if _, ok := c.spaces[event.spaceID]; !ok {
		return failure(ErrorPolicy, "space_not_allowed")
	}
	if _, ok := c.contentTypes[event.content.mediaType]; !ok {
		return failure(ErrorPolicy, "content_type_not_allowed")
	}
	if _, ok := c.sourceTypes[event.sourceType]; !ok {
		return failure(ErrorPolicy, "source_type_not_allowed")
	}
	if _, ok := c.sensitivities[event.sensitivity]; !ok {
		return failure(ErrorPolicy, "sensitivity_not_allowed")
	}
	if !boundedString(event.eventID) || !boundedString(event.spaceID) || !boundedString(event.sourceType) || !boundedOptionalString(event.sourceName) || event.occurredAt.IsZero() {
		return failure(ErrorInvalidInput, "invalid_event")
	}
	if !boundedString(event.content.mediaType) || (event.content.kind != "text" && event.content.kind != "json" && event.content.kind != "reference") {
		return failure(ErrorInvalidInput, "invalid_content_kind")
	}
	if event.content.kind == "reference" && (event.content.declaredSize < 0 || !boundedOptionalString(event.content.declaredHash)) {
		return failure(ErrorInvalidInput, "invalid_reference_metadata")
	}
	if event.content.kind != "reference" && (event.content.text == "" || utf8.RuneCountInString(event.content.text) > c.maxChars) {
		return failure(ErrorInvalidInput, "content_out_of_bounds")
	}
	return nil
}

func (c *Client) minimizeContext(spaceID string, events []Event, remaining int) ([]egressEvent, error) {
	if len(events) > c.maxContext {
		events = events[:c.maxContext]
	}
	result := make([]egressEvent, 0, len(events))
	for _, event := range events {
		if event.spaceID != spaceID {
			return nil, failure(ErrorPolicy, "context_space_not_allowed")
		}
		if _, ok := c.contentTypes[event.content.mediaType]; !ok {
			return nil, failure(ErrorPolicy, "context_content_type_not_allowed")
		}
		if _, ok := c.sourceTypes[event.sourceType]; !ok {
			return nil, failure(ErrorPolicy, "context_source_type_not_allowed")
		}
		if _, ok := c.sensitivities[event.sensitivity]; !ok {
			return nil, failure(ErrorPolicy, "context_sensitivity_not_allowed")
		}
		if !boundedString(event.eventID) || !boundedString(event.sourceType) || !boundedOptionalString(event.sourceName) || event.occurredAt.IsZero() {
			return nil, failure(ErrorInvalidInput, "invalid_context_event")
		}
		if event.content.kind != "text" && event.content.kind != "json" && event.content.kind != "reference" {
			return nil, failure(ErrorInvalidInput, "invalid_context_event")
		}
		if event.content.kind == "reference" && (event.content.declaredSize < 0 || !boundedOptionalString(event.content.declaredHash)) {
			return nil, failure(ErrorInvalidInput, "invalid_context_event")
		}
		if event.content.kind != "reference" {
			length := utf8.RuneCountInString(event.content.text)
			if event.content.text == "" || length > remaining {
				break
			}
			remaining -= length
		}
		result = append(result, wireEvent(event))
	}
	return result, nil
}

func (c *Client) minimizeMemories(memories []Memory, remaining, limit int) ([]egressMemory, error) {
	if limit < 0 {
		limit = 0
	}
	if len(memories) > limit {
		memories = memories[:limit]
	}
	result := make([]egressMemory, 0, len(memories))
	for _, memory := range memories {
		if _, ok := c.spaces[memory.spaceID]; !ok {
			return nil, failure(ErrorPolicy, "context_memory_space_not_allowed")
		}
		if _, ok := c.sensitivities[memory.sensitivity]; !ok {
			return nil, failure(ErrorPolicy, "context_memory_sensitivity_not_allowed")
		}
		if !boundedString(memory.memoryID) || !boundedString(memory.revisionID) || !boundedString(memory.spaceID) || memory.content == "" || !validMemoryState(memory.state) || (memory.validFrom != nil && memory.validUntil != nil && !memory.validFrom.Before(*memory.validUntil)) {
			return nil, failure(ErrorInvalidInput, "invalid_context_memory")
		}
		length := utf8.RuneCountInString(memory.content)
		if length > remaining {
			break
		}
		remaining -= length
		result = append(result, egressMemory{MemoryID: memory.memoryID, RevisionID: memory.revisionID, SpaceID: memory.spaceID, Content: memory.content, State: memory.state, Sensitivity: memory.sensitivity, ValidFrom: cloneTime(memory.validFrom), ValidUntil: cloneTime(memory.validUntil)})
	}
	return result, nil
}

func wireEvent(event Event) egressEvent {
	content := egressContent{Kind: event.content.kind, MediaType: event.content.mediaType}
	if event.content.kind == "reference" {
		content.DeclaredSize, content.DeclaredHash = event.content.declaredSize, event.content.declaredHash
	} else {
		content.Text = event.content.text
	}
	return egressEvent{EventID: event.eventID, SpaceID: event.spaceID, SourceType: event.sourceType, SourceName: event.sourceName, Sensitivity: event.sensitivity, OccurredAt: event.occurredAt.UTC().Format(time.RFC3339Nano), Content: content}
}

func (c *Client) post(ctx context.Context, payload, output any) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	secret := os.Getenv(c.secretEnv)
	if secret == "" {
		return "", failure(ErrorPermanent, "secret_unavailable")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", failure(ErrorInvalidInput, "request_encoding_failed")
	}
	if err := c.acquire(requestCtx); err != nil {
		return "", err
	}
	defer func() { <-c.concurrency }()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", failure(ErrorPermanent, "request_creation_failed")
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return "", failure(ErrorTransient, "request_timeout_or_cancelled")
		}
		return "", failure(ErrorTransient, "network_failure")
	}
	defer response.Body.Close()
	requestID := boundedRequestID(response.Header.Get("X-Request-Id"))
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return requestID, failure(ErrorPermanent, "redirect_rejected")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return requestID, statusFailure(response.StatusCode)
	}
	limited := io.LimitReader(response.Body, c.maxResponse+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return requestID, failure(ErrorTransient, "response_read_failed")
	}
	if int64(len(responseBody)) > c.maxResponse {
		return requestID, failure(ErrorInvalidOutput, "response_too_large")
	}
	if err := decodeSingle(responseBody, output); err != nil {
		return requestID, failure(ErrorInvalidOutput, "malformed_json_response")
	}
	return requestID, nil
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.concurrency <- struct{}{}:
	case <-ctx.Done():
		return failure(ErrorTransient, "request_cancelled")
	}
	c.rateMu.Lock()
	wait := time.Until(c.nextRequest)
	if wait < 0 {
		wait = 0
	}
	c.nextRequest = time.Now().Add(wait).Add(c.requestGap)
	c.rateMu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		<-c.concurrency
		return failure(ErrorTransient, "request_cancelled")
	}
}

func validateProcessOutput(output processOutput, request ProcessRequest, usage usageWire, requestID string, maxChars int) (ProcessResult, error) {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
		return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_usage")
	}
	if output.Category == ProcessNoMemory {
		if len(output.Candidates) != 0 {
			return ProcessResult{}, failure(ErrorInvalidOutput, "unexpected_candidates")
		}
		return ProcessResult{category: output.Category, usage: usageFromWire(usage), requestID: requestID}, nil
	}
	if output.Category != ProcessCandidatesExtracted || len(output.Candidates) == 0 || len(output.Candidates) > request.maxCandidates {
		return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_result_category")
	}
	allowedTypes := makeSet(request.allowedMemoryTypes)
	candidates := make([]Candidate, len(output.Candidates))
	totalChars := 0
	for i, item := range output.Candidates {
		contentChars := utf8.RuneCountInString(item.Content)
		totalChars += contentChars
		if item.Content == "" || contentChars > maxChars || totalChars > maxChars || item.SpaceID != request.target.spaceID {
			return ProcessResult{}, failure(ErrorInvalidOutput, "candidate_out_of_bounds")
		}
		if _, ok := allowedTypes[item.MemoryType]; !ok || math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) || item.Confidence < 0 || item.Confidence > 1 {
			return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_candidate")
		}
		if (item.Sensitivity != "normal" && item.Sensitivity != "sensitive") || !validSuggestion(item.SuggestionKind) || len(item.Evidence) == 0 || len(item.Evidence) > 100 || len(item.RelatedMemoryIDs) > 100 || (item.ValidFrom != nil && item.ValidUntil != nil && !item.ValidFrom.Before(*item.ValidUntil)) {
			return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_candidate")
		}
		events := map[string]Event{request.target.eventID: request.target}
		for _, event := range request.context {
			events[event.eventID] = event
		}
		evidence := make([]Evidence, len(item.Evidence))
		for n, reference := range item.Evidence {
			event, ok := events[reference.EventID]
			if !ok || !validEvidenceType(reference.EvidenceType) || (reference.StartOffset == nil) != (reference.EndOffset == nil) {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_evidence_reference")
			}
			if reference.StartOffset != nil && (event.content.kind == "reference" || *reference.StartOffset < 0 || *reference.EndOffset <= *reference.StartOffset || *reference.EndOffset > int64(utf8.RuneCountInString(event.content.text))) {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_evidence_reference")
			}
			evidence[n] = Evidence{eventID: reference.EventID, evidenceType: reference.EvidenceType, startOffset: cloneInt64(reference.StartOffset), endOffset: cloneInt64(reference.EndOffset)}
		}
		memoryIDs := makeSet(nil)
		for _, memory := range request.contextMemories {
			memoryIDs[memory.memoryID] = struct{}{}
		}
		for _, relatedID := range item.RelatedMemoryIDs {
			if _, ok := memoryIDs[relatedID]; !ok || !boundedString(relatedID) {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_related_memory_reference")
			}
		}
		candidates[i] = Candidate{content: item.Content, memoryType: item.MemoryType, spaceID: item.SpaceID, validFrom: cloneTime(item.ValidFrom), validUntil: cloneTime(item.ValidUntil), confidence: item.Confidence, sensitivity: item.Sensitivity, evidence: evidence, suggestion: item.SuggestionKind, relatedIDs: append([]string(nil), item.RelatedMemoryIDs...)}
	}
	return ProcessResult{category: output.Category, candidates: candidates, usage: usageFromWire(usage), requestID: requestID}, nil
}

func decodeSingle(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("additional JSON value")
	}
	return nil
}

func decodeSingleStrict(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("additional JSON value")
	}
	return nil
}

func statusFailure(status int) error {
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
		return failure(ErrorTransient, "provider_temporarily_unavailable")
	}
	return failure(ErrorPermanent, "provider_request_rejected")
}
func failure(class ErrorClass, code string) error { return &Error{class: class, code: code} }
func boundedRequestID(value string) string {
	if len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return ""
		}
	}
	return value
}
func makeSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}
func validEnvName(name string) bool {
	for i, r := range name {
		if !((r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return name != ""
}
func validSuggestion(value string) bool {
	switch value {
	case "new", "duplicate", "supplement", "replacement", "conflict":
		return true
	}
	return false
}
func validSourceType(value string) bool {
	switch value {
	case "message", "conversation", "tool_call", "tool_result", "file_reference", "collector_event", "manual":
		return true
	}
	return false
}
func validMemoryState(value string) bool {
	switch value {
	case "active", "contested", "superseded", "archived", "expired", "retracted":
		return true
	}
	return false
}
func validEvidenceType(value string) bool {
	return value == "direct_statement" || value == "tool_result" || value == "manual_reference"
}
func boundedString(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= maxIdentityChars
}
func boundedOptionalString(value string) bool {
	return utf8.RuneCountInString(value) <= maxIdentityChars
}
func usageFromWire(value usageWire) Usage {
	return Usage{inputTokens: value.PromptTokens, outputTokens: value.CompletionTokens}
}

type egressContent struct {
	Kind         string `json:"kind"`
	MediaType    string `json:"media_type"`
	Text         string `json:"text,omitempty"`
	DeclaredSize int64  `json:"declared_size,omitempty"`
	DeclaredHash string `json:"declared_hash,omitempty"`
}
type egressEvent struct {
	EventID     string        `json:"event_id"`
	SpaceID     string        `json:"space_id"`
	SourceType  string        `json:"source_type"`
	SourceName  string        `json:"source_name,omitempty"`
	Sensitivity string        `json:"sensitivity"`
	OccurredAt  string        `json:"occurred_at"`
	Content     egressContent `json:"content"`
}
type processPrompt struct {
	ProcessorIdentity  string         `json:"processor_identity"`
	ProcessingVersion  string         `json:"processing_version"`
	JobID              string         `json:"job_id"`
	Attempt            int            `json:"attempt"`
	Event              egressEvent    `json:"event"`
	ContextEvents      []egressEvent  `json:"context_events,omitempty"`
	ContextMemories    []egressMemory `json:"context_memories,omitempty"`
	AllowedMemoryTypes []string       `json:"allowed_memory_types"`
	MaxCandidates      int            `json:"max_candidates"`
	MaxContentChars    int            `json:"max_content_chars"`
}
type egressMemory struct {
	MemoryID    string     `json:"memory_id"`
	RevisionID  string     `json:"revision_id"`
	SpaceID     string     `json:"space_id"`
	Content     string     `json:"content"`
	State       string     `json:"state"`
	Sensitivity string     `json:"sensitivity"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
}
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type responseFormat struct {
	Type string `json:"type"`
}
type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}
type usageWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}
type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage usageWire `json:"usage"`
}
type candidateWire struct {
	Content          string         `json:"content"`
	MemoryType       string         `json:"memory_type"`
	SpaceID          string         `json:"space_id"`
	ValidFrom        *time.Time     `json:"valid_from"`
	ValidUntil       *time.Time     `json:"valid_until"`
	Confidence       float64        `json:"confidence"`
	Sensitivity      string         `json:"sensitivity"`
	Evidence         []evidenceWire `json:"evidence"`
	SuggestionKind   string         `json:"suggestion_kind"`
	RelatedMemoryIDs []string       `json:"related_memory_ids"`
}
type evidenceWire struct {
	EventID      string `json:"event_id"`
	EvidenceType string `json:"evidence_type"`
	StartOffset  *int64 `json:"start_offset"`
	EndOffset    *int64 `json:"end_offset"`
}
type processOutput struct {
	Category   ProcessCategory `json:"category"`
	Candidates []candidateWire `json:"candidates"`
}
type embeddingWireRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}
type multimodalEmbeddingWireRequest struct {
	Model      string                     `json:"model"`
	Input      []multimodalEmbeddingInput `json:"input"`
	Dimensions int                        `json:"dimensions,omitempty"`
}
type multimodalEmbeddingInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type embeddingWireItem struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}
type embeddingResponse struct {
	Data  []embeddingWireItem `json:"data"`
	Usage usageWire           `json:"usage"`
}
type multimodalEmbeddingResponse struct {
	Data  embeddingWireItem `json:"data"`
	Usage usageWire         `json:"usage"`
}
