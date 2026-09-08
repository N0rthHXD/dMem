package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
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
- "evidence": non-empty array with no duplicate references. Each element has EXACTLY {"event_id","evidence_type","start_offset","end_offset"}. "event_id" must be the target event_id or a context event_id present in the input. "evidence_type" is "direct_statement", "tool_result", or "manual_reference". For "new", set BOTH "start_offset" and "end_offset" to null to cite the whole event, or set both to 0-based rune offsets into that event's content text with start_offset < end_offset and end_offset not exceeding the text length. For every other suggestion_kind, offsets must be a non-null range. Never set only one of the two.
- "related_memory_ids": array of unique context memory ids this candidate relates to. "new" uses []; "supplement" and "replacement" require exactly one id; "duplicate" requires at least one id; "conflict" requires exactly two ids. Non-"new" candidates may only reference context memories marked proposal_capable=true.

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
	Enabled           bool
	Kind              string
	Endpoint          string
	Model             string
	TelemetryDisabled bool
	Secret            SecretReference
	Options           EmbeddingOptions
	Limits            Limits
	Policy            Policy
}

type Client struct {
	enabled           bool
	endpoint          string
	model             string
	secret            SecretReference
	contentTypes      map[string]struct{}
	sourceTypes       map[string]struct{}
	sensitivities     map[string]struct{}
	queryAllowed      bool
	maxEgressInput    int
	maxAggregateInput int
	maxContext        int
	maxCandidate      int
	maxTotalCandidate int
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
	contentTypes := makeMIMETypeSet(cfg.Policy.ContentTypes)
	sourceTypes := makeSet(cfg.Policy.SourceTypes)
	sensitivities := makeSet(cfg.Policy.Sensitivities)
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	httpCopy := *httpClient
	httpCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		enabled: true, endpoint: endpoint.String(), model: cfg.Model,
		secret:       cfg.Secret,
		contentTypes: contentTypes, sourceTypes: sourceTypes, sensitivities: sensitivities, queryAllowed: cfg.Policy.Query != "deny",
		maxEgressInput: cfg.Limits.MaxEgressInputChars, maxAggregateInput: cfg.Limits.MaxAggregateInputChars, maxContext: cfg.Limits.MaxContextItems,
		maxCandidate: cfg.Limits.MaxCandidateChars, maxTotalCandidate: cfg.Limits.MaxTotalCandidateChars,
		maxResponse: cfg.Limits.MaxResponseBytes, timeout: cfg.Limits.RequestTimeout,
		http: &httpCopy, concurrency: make(chan struct{}, cfg.Limits.MaxConcurrent),
		requestGap:   time.Minute / time.Duration(cfg.Limits.RequestsPerMinute),
		embeddingDim: cfg.Options.Dimensions, queryInstructions: cfg.Options.QueryInstructions,
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
	if !boundedString(cfg.Model) {
		return errors.New("provider: incomplete model identity")
	}
	if cfg.Secret.Source != "environment" || !validEnvName(cfg.Secret.Name) {
		return errors.New("provider: invalid secret reference")
	}
	if cfg.Limits.MaxEgressInputChars < 1 || cfg.Limits.MaxEgressInputChars > 100000 || cfg.Limits.MaxContextItems < 0 || cfg.Limits.MaxContextItems > 100 || cfg.Limits.MaxResponseBytes < 1024 || cfg.Limits.MaxResponseBytes > 16*1024*1024 {
		return errors.New("provider: invalid content or response bounds")
	}
	if cfg.Limits.RequestTimeout <= 0 || cfg.Limits.RequestTimeout > 5*time.Minute || cfg.Limits.MaxConcurrent < 1 || cfg.Limits.MaxConcurrent > 64 || cfg.Limits.RequestsPerMinute < 1 || cfg.Limits.RequestsPerMinute > 60000 {
		return errors.New("provider: invalid request limits")
	}
	if cfg.Options.Dimensions < 0 || cfg.Options.Dimensions > maxEmbeddingDimension || utf8.RuneCountInString(cfg.Options.QueryInstructions) > cfg.Limits.MaxEgressInputChars {
		return errors.New("provider: invalid embedding options")
	}
	if cfg.Limits.MaxCandidateChars != 0 && (cfg.Limits.MaxAggregateInputChars < 1 || cfg.Limits.MaxAggregateInputChars > 100000 || cfg.Limits.MaxCandidateChars > 100000 || cfg.Limits.MaxTotalCandidateChars < cfg.Limits.MaxCandidateChars || cfg.Limits.MaxTotalCandidateChars > 1000000) {
		return errors.New("provider: invalid candidate bounds")
	}
	contentTypes := makeMIMETypeSet(cfg.Policy.ContentTypes)
	sourceTypes := makeSet(cfg.Policy.SourceTypes)
	sensitivities := makeSet(cfg.Policy.Sensitivities)
	if len(contentTypes) == 0 || len(sourceTypes) == 0 || len(sensitivities) == 0 {
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
	if cfg.Policy.Default != "deny" {
		return errors.New("provider: policy default must be deny")
	}
	if len(cfg.Policy.Assignments) == 0 {
		return errors.New("provider: at least one policy assignment is required")
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
	return "generated"
}

func (c *Client) Process(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
	if err := c.PreflightProcess(request); err != nil {
		return ProcessResult{}, err
	}
	remaining := c.maxEgressInput
	if request.target.content.kind != "reference" {
		remaining -= utf8.RuneCountInString(request.target.content.text)
	}
	contextEvents, omissions := c.minimizeContext(request.target.spaceID, request.context, remaining)
	for _, event := range contextEvents {
		if event.Content.Kind != "reference" {
			remaining -= utf8.RuneCountInString(event.Content.Text)
		}
	}
	contextMemories, memoryOmissions := c.minimizeMemories(request.contextMemories, remaining, max(0, c.maxContext-len(contextEvents)))
	mergeOmissions(omissions, memoryOmissions)
	request.context = acceptedEvents(request.context, contextEvents)
	request.contextMemories = acceptedMemories(request.contextMemories, contextMemories)
	payload := chatRequest{Model: c.model, ResponseFormat: responseFormat{Type: "json_object"}}
	prompt := processPrompt{
		ProcessorIdentity: request.processorIdentity, ProcessingVersion: request.processingVersion,
		JobID: request.jobID, Attempt: request.attempt, Event: wireEvent(request.target),
		ContextEvents: contextEvents, ContextMemories: contextMemories, AllowedMemoryTypes: append([]string(nil), request.allowedMemoryTypes...), MaxCandidates: request.maxCandidates, MaxContentChars: min(request.maxCandidateChars, c.maxCandidate),
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
	request.maxCandidateChars = min(request.maxCandidateChars, c.maxCandidate)
	request.maxTotalChars = min(request.maxTotalChars, c.maxTotalCandidate)
	result, err := validateProcessOutput(output, request, response.Usage, requestID)
	if err != nil {
		return ProcessResult{}, err
	}
	result.omissionSummary = omissions
	for _, count := range omissions {
		result.omittedCount += count
	}
	return result, nil
}

func (c *Client) PreflightProcess(request ProcessRequest) error {
	if !c.Enabled() {
		return failure(ErrorDisabled, "provider_disabled")
	}
	if err := c.validateEvent(request.target); err != nil {
		return err
	}
	if !boundedString(request.processorIdentity) || !boundedString(request.processingVersion) || !boundedString(request.jobID) || request.attempt < 1 || request.maxCandidates < 1 || request.maxCandidates > 100 || request.maxCandidateChars < 1 || request.maxTotalChars < request.maxCandidateChars || len(request.allowedMemoryTypes) == 0 {
		return failure(ErrorInvalidInput, "invalid_process_request")
	}
	for _, memoryType := range request.allowedMemoryTypes {
		if !boundedString(memoryType) {
			return failure(ErrorInvalidInput, "invalid_process_request")
		}
	}
	if request.target.content.kind != "reference" {
		if utf8.RuneCountInString(request.target.content.text) > c.maxEgressInput {
			return failure(ErrorInvalidInput, "content_out_of_bounds")
		}
	}
	return nil
}

const graphExtractSystemPrompt = `You are a knowledge-graph extraction processor. Given a confirmed memory revision (source_memory_id, source_revision_id, content) and, optionally, a list of context memories, extract structured knowledge.

Respond with STRICT JSON ONLY (no markdown, no surrounding prose) matching this shape exactly:
{"entities":[...],"mentions":[...],"facts":[...],"relations":[...]}

Every array may be empty. Do not invent memory ids or revision ids that were not provided.

- "entities": array of entity objects. Each has EXACTLY:
  - "local_id": string. A short local identifier (e.g. "e0") unique within this response.
  - "canonical_name": string. A non-empty canonical display name.
  - "entity_type": one of "person","organization","place","project","product","event","concept","other".
  - "aliases": array of strings (may be empty). Alternative names or surface forms for this entity.
  - "confidence": number between 0 and 1 inclusive.
- "mentions": array of mention objects. Each has EXACTLY:
  - "entity_local_id": string. Must reference an "entities" local_id in this response.
  - "text": string. The exact surface text as it appears in content.
  - "start_rune": integer. 0-based rune offset into content.
  - "end_rune": integer. Exclusive 0-based rune offset into content. start_rune < end_rune and end_rune must not exceed the rune length of content, and content[start_rune:end_rune] (by runes) must equal "text".
  - "confidence": number between 0 and 1 inclusive.
- "facts": array of entity-fact objects. Each has EXACTLY:
  - "subject_local_id": string. Must reference an "entities" local_id.
  - "predicate": one of "participates_in","uses_database","owned_by","located_in","depends_on".
  - "object_local_id": string. Required when the object is another entity; must reference a DIFFERENT "entities" local_id. Omit or set to "" when using object_literal.
  - "object_literal": string. Required when the object is a literal value; omit otherwise.
  - "object_literal_type": "string","number", or "boolean". Required only when object_literal is present.
  - "confidence": number between 0 and 1 inclusive.
  - "valid_from", "valid_until": optional RFC3339 strings; valid_until must be after valid_from.
  Exactly one of "object_local_id" (non-empty) and "object_literal" (non-empty) must be present.
- "relations": array of memory-relation objects between the source revision and a context memory. Each has EXACTLY:
  - "relation_type": one of "supports","contradicts","supersedes","derived_from","related_to".
  - "target_memory_id": string. Must reference a context memory id provided in the input.
  - "target_revision_id": string. The current revision id of that context memory.
  - "confidence": number between 0 and 1 inclusive.
  - "evidence": array (may be empty) of {"start_rune","end_rune"} rune ranges into content. Each start_rune < end_rune and within content bounds.

Do not add any field beyond those listed.`

var validGraphPredicates = map[string]struct{}{
	"participates_in": {}, "uses_database": {}, "owned_by": {}, "located_in": {}, "depends_on": {},
}

func validGraphPredicate(value string) bool {
	_, ok := validGraphPredicates[value]
	return ok
}

func (c *Client) PreflightGraphExtract(request GraphExtractRequest) error {
	if !c.Enabled() {
		return failure(ErrorDisabled, "provider_disabled")
	}
	if !boundedString(request.processorIdentity) || !boundedString(request.processingVersion) || !boundedString(request.jobID) || request.attempt < 1 ||
		!boundedString(request.memoryID) || !boundedString(request.revisionID) || !boundedString(request.spaceID) ||
		request.content == "" || utf8.RuneCountInString(request.content) > c.maxEgressInput ||
		request.maxEntities < 1 || request.maxFacts < 1 || request.maxRelations < 1 || request.maxContentChars < 1 {
		return failure(ErrorInvalidInput, "invalid_graph_extract_request")
	}
	if _, ok := c.sensitivities[request.sensitivity]; !ok {
		return failure(ErrorPolicy, "sensitivity_not_allowed")
	}
	return nil
}

func (c *Client) GraphExtract(ctx context.Context, request GraphExtractRequest) (GraphExtractResult, error) {
	if err := c.PreflightGraphExtract(request); err != nil {
		return GraphExtractResult{}, err
	}
	remaining := c.maxEgressInput - utf8.RuneCountInString(request.content)
	contextMemories, _ := c.minimizeMemories(request.contextMemories, remaining, max(0, c.maxContext))
	request.contextMemories = acceptedMemories(request.contextMemories, contextMemories)
	payload := chatRequest{Model: c.model, ResponseFormat: responseFormat{Type: "json_object"}}
	prompt := graphExtractPrompt{
		ProcessorIdentity: request.processorIdentity, ProcessingVersion: request.processingVersion,
		JobID: request.jobID, Attempt: request.attempt, SourceMemoryID: request.memoryID, SourceRevisionID: request.revisionID,
		SpaceID: request.spaceID, Content: request.content, ContextMemories: contextMemories,
		MaxEntities: request.maxEntities, MaxFacts: request.maxFacts, MaxRelations: request.maxRelations,
	}
	promptJSON, err := json.Marshal(prompt)
	if err != nil {
		return GraphExtractResult{}, failure(ErrorInvalidInput, "invalid_graph_extract_request")
	}
	payload.Messages = []chatMessage{{Role: "system", Content: graphExtractSystemPrompt}, {Role: "user", Content: string(promptJSON)}}
	var response chatResponse
	requestID, err := c.post(ctx, payload, &response)
	if err != nil {
		return GraphExtractResult{}, err
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content == "" {
		return GraphExtractResult{}, failure(ErrorInvalidOutput, "malformed_chat_response")
	}
	var output graphExtractOutput
	if err := decodeSingleStrict([]byte(response.Choices[0].Message.Content), &output); err != nil {
		return GraphExtractResult{}, failure(ErrorInvalidOutput, "malformed_graph_extract_output")
	}
	result, err := validateGraphExtractOutput(output, request, response.Usage, requestID)
	if err != nil {
		return GraphExtractResult{}, err
	}
	return result, nil
}

func validateGraphExtractOutput(output graphExtractOutput, request GraphExtractRequest, usage usageWire, requestID string) (GraphExtractResult, error) {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
		return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_usage")
	}
	contentRunes := []rune(request.content)
	entityIDs := make(map[string]struct{}, len(output.Entities))
	entities := make([]GraphEntity, 0, len(output.Entities))
	for _, item := range output.Entities {
		if item.LocalID == "" || !boundedString(item.LocalID) {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_graph_entity")
		}
		if _, duplicate := entityIDs[item.LocalID]; duplicate {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "duplicate_graph_entity")
		}
		entityIDs[item.LocalID] = struct{}{}
		if strings.TrimSpace(item.CanonicalName) == "" || !boundedString(item.CanonicalName) || !validEntityTypeGraph(item.EntityType) {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_graph_entity")
		}
		if math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) || item.Confidence < 0 || item.Confidence > 1 {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_graph_entity")
		}
		aliases := make([]string, 0, len(item.Aliases))
		for _, alias := range item.Aliases {
			if strings.TrimSpace(alias) == "" || !boundedString(alias) {
				return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_graph_entity")
			}
			aliases = append(aliases, alias)
		}
		entities = append(entities, GraphEntity{localID: item.LocalID, canonicalName: strings.TrimSpace(item.CanonicalName), entityType: item.EntityType, aliases: aliases, confidence: item.Confidence})
	}
	mentions := make([]GraphMention, 0, len(output.Mentions))
	for _, item := range output.Mentions {
		if _, ok := entityIDs[item.EntityLocalID]; !ok {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "unknown_mention_entity")
		}
		if item.Text == "" || item.StartRune < 0 || item.EndRune <= item.StartRune || item.EndRune > len(contentRunes) {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_graph_mention")
		}
		if string(contentRunes[item.StartRune:item.EndRune]) != item.Text {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_graph_mention")
		}
		if math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) || item.Confidence < 0 || item.Confidence > 1 {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_graph_mention")
		}
		mentions = append(mentions, GraphMention{entityLocalID: item.EntityLocalID, text: item.Text, startRune: item.StartRune, endRune: item.EndRune, confidence: item.Confidence})
	}
	facts := make([]GraphFact, 0, len(output.Facts))
	for _, item := range output.Facts {
		if _, ok := entityIDs[item.SubjectLocalID]; !ok {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "unknown_fact_subject")
		}
		if !validGraphPredicate(item.Predicate) {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_fact_predicate")
		}
		hasObject := item.ObjectLocalID != ""
		hasLiteral := item.ObjectLiteral != ""
		if hasObject == hasLiteral {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_fact_object")
		}
		if hasObject {
			if _, ok := entityIDs[item.ObjectLocalID]; !ok {
				return GraphExtractResult{}, failure(ErrorInvalidOutput, "unknown_fact_object")
			}
			if item.ObjectLocalID == item.SubjectLocalID {
				return GraphExtractResult{}, failure(ErrorInvalidOutput, "self_fact_relation")
			}
		}
		if hasLiteral {
			if item.ObjectLiteralType != "string" && item.ObjectLiteralType != "number" && item.ObjectLiteralType != "boolean" {
				return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_fact_literal_type")
			}
			if !boundedString(item.ObjectLiteral) {
				return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_fact_literal")
			}
		}
		if math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) || item.Confidence < 0 || item.Confidence > 1 {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_fact_confidence")
		}
		if item.ValidFrom != nil && item.ValidUntil != nil && !item.ValidFrom.Before(*item.ValidUntil) {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_fact_validity")
		}
		facts = append(facts, GraphFact{subjectLocalID: item.SubjectLocalID, predicate: item.Predicate, objectLocalID: item.ObjectLocalID, objectLiteral: item.ObjectLiteral, objectLiteralType: item.ObjectLiteralType, confidence: item.Confidence, validFrom: cloneTime(item.ValidFrom), validUntil: cloneTime(item.ValidUntil)})
	}
	contextMemories := make(map[string]Memory, len(request.contextMemories))
	for _, memory := range request.contextMemories {
		contextMemories[memory.memoryID] = memory
	}
	relations := make([]GraphMemoryRelation, 0, len(output.Relations))
	for _, item := range output.Relations {
		if !validMemoryRelationType(item.RelationType) {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_relation_type")
		}
		memory, ok := contextMemories[item.TargetMemoryID]
		if !ok || memory.revisionID != item.TargetRevisionID {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "unknown_relation_target")
		}
		if item.TargetMemoryID == request.memoryID && item.TargetRevisionID == request.revisionID {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "self_memory_relation")
		}
		if math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) || item.Confidence < 0 || item.Confidence > 1 {
			return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_relation_confidence")
		}
		evidence := make([]GraphRelationEvidence, 0, len(item.Evidence))
		for _, ref := range item.Evidence {
			if ref.StartRune < 0 || ref.EndRune <= ref.StartRune || ref.EndRune > len(contentRunes) {
				return GraphExtractResult{}, failure(ErrorInvalidOutput, "invalid_relation_evidence")
			}
			evidence = append(evidence, GraphRelationEvidence{startRune: ref.StartRune, endRune: ref.EndRune})
		}
		relations = append(relations, GraphMemoryRelation{relationType: item.RelationType, targetMemoryID: item.TargetMemoryID, targetRevisionID: item.TargetRevisionID, confidence: item.Confidence, evidence: evidence})
	}
	if len(entities) > request.maxEntities || len(facts) > request.maxFacts || len(relations) > request.maxRelations {
		return GraphExtractResult{}, failure(ErrorInvalidOutput, "graph_output_out_of_bounds")
	}
	return GraphExtractResult{entities: entities, mentions: mentions, facts: facts, relations: relations, usage: usageFromWire(usage), requestID: requestID}, nil
}

func validEntityTypeGraph(value string) bool {
	switch value {
	case "person", "organization", "place", "project", "product", "event", "concept", "other":
		return true
	}
	return false
}

func validMemoryRelationType(value string) bool {
	switch value {
	case "supports", "contradicts", "supersedes", "derived_from", "related_to":
		return true
	}
	return false
}

type graphExtractPrompt struct {
	ProcessorIdentity string         `json:"processor_identity"`
	ProcessingVersion string         `json:"processing_version"`
	JobID             string         `json:"job_id"`
	Attempt           int            `json:"attempt"`
	SourceMemoryID    string         `json:"source_memory_id"`
	SourceRevisionID  string         `json:"source_revision_id"`
	SpaceID           string         `json:"space_id"`
	Content           string         `json:"content"`
	ContextMemories   []egressMemory `json:"context_memories,omitempty"`
	MaxEntities       int            `json:"max_entities"`
	MaxFacts          int            `json:"max_facts"`
	MaxRelations      int            `json:"max_relations"`
}

type graphEntityWire struct {
	LocalID       string   `json:"local_id"`
	CanonicalName string   `json:"canonical_name"`
	EntityType    string   `json:"entity_type"`
	Aliases       []string `json:"aliases"`
	Confidence    float64  `json:"confidence"`
}

type graphMentionWire struct {
	EntityLocalID string  `json:"entity_local_id"`
	Text          string  `json:"text"`
	StartRune     int     `json:"start_rune"`
	EndRune       int     `json:"end_rune"`
	Confidence    float64 `json:"confidence"`
}

type graphFactWire struct {
	SubjectLocalID    string     `json:"subject_local_id"`
	Predicate         string     `json:"predicate"`
	ObjectLocalID     string     `json:"object_local_id"`
	ObjectLiteral     string     `json:"object_literal"`
	ObjectLiteralType string     `json:"object_literal_type"`
	Confidence        float64    `json:"confidence"`
	ValidFrom         *time.Time `json:"valid_from"`
	ValidUntil        *time.Time `json:"valid_until"`
}

type graphRelationWire struct {
	RelationType     string                  `json:"relation_type"`
	TargetMemoryID   string                  `json:"target_memory_id"`
	TargetRevisionID string                  `json:"target_revision_id"`
	Confidence       float64                 `json:"confidence"`
	Evidence         []graphRelationEvidence `json:"evidence"`
}

type graphRelationEvidence struct {
	StartRune int `json:"start_rune"`
	EndRune   int `json:"end_rune"`
}

type graphExtractOutput struct {
	Entities  []graphEntityWire   `json:"entities"`
	Mentions  []graphMentionWire  `json:"mentions"`
	Facts     []graphFactWire     `json:"facts"`
	Relations []graphRelationWire `json:"relations"`
}

func acceptedEvents(events []Event, accepted []egressEvent) []Event {
	ids := make(map[string]struct{}, len(accepted))
	for _, event := range accepted {
		ids[event.EventID] = struct{}{}
	}
	result := make([]Event, 0, len(accepted))
	for _, event := range events {
		if _, ok := ids[event.eventID]; ok {
			result = append(result, event)
		}
	}
	return result
}

func acceptedMemories(memories []Memory, accepted []egressMemory) []Memory {
	ids := make(map[string]struct{}, len(accepted))
	for _, memory := range accepted {
		ids[memory.MemoryID] = struct{}{}
	}
	result := make([]Memory, 0, len(accepted))
	for _, memory := range memories {
		if _, ok := ids[memory.memoryID]; ok {
			result = append(result, memory)
		}
	}
	return result
}

func (c *Client) Build(ctx context.Context, request domain.AggregateProviderInput) (domain.AggregateProviderResult, error) {
	if !c.Enabled() {
		return domain.AggregateProviderResult{}, failure(ErrorDisabled, "provider_disabled")
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
		if total > c.maxAggregateInput {
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
	if _, ok := c.contentTypes[normalizeMIMEType(request.contentType)]; !ok {
		return EmbeddingResult{}, failure(ErrorPolicy, "content_type_not_allowed")
	}
	if _, ok := c.sensitivities[request.sensitivity]; !ok {
		return EmbeddingResult{}, failure(ErrorPolicy, "sensitivity_not_allowed")
	}
	if request.query && !c.queryAllowed {
		return EmbeddingResult{}, failure(ErrorPolicy, "semantic_query_not_allowed")
	}
	if !request.query && len(request.sourceTypes) == 0 {
		return EmbeddingResult{}, failure(ErrorInvalidInput, "missing_embedding_source_types")
	}
	for _, sourceType := range request.sourceTypes {
		if request.query && sourceType == "semantic_query" {
			continue
		}
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
		if input == "" || utf8.RuneCountInString(input) > c.maxEgressInput {
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
	if _, ok := c.contentTypes[normalizeMIMEType(event.content.mediaType)]; !ok {
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
	if event.content.kind != "reference" && (event.content.text == "" || utf8.RuneCountInString(event.content.text) > c.maxEgressInput) {
		return failure(ErrorInvalidInput, "content_out_of_bounds")
	}
	return nil
}

func (c *Client) minimizeContext(spaceID string, events []Event, remaining int) ([]egressEvent, map[string]int) {
	result := make([]egressEvent, 0, len(events))
	omissions := make(map[string]int)
	for _, event := range events {
		if len(result) == c.maxContext {
			omissions["context_limit"]++
			continue
		}
		if event.spaceID != spaceID {
			omissions["context_space_not_allowed"]++
			continue
		}
		if _, ok := c.contentTypes[normalizeMIMEType(event.content.mediaType)]; !ok {
			omissions["context_content_type_not_allowed"]++
			continue
		}
		if _, ok := c.sourceTypes[event.sourceType]; !ok {
			omissions["context_source_type_not_allowed"]++
			continue
		}
		if _, ok := c.sensitivities[event.sensitivity]; !ok {
			omissions["context_sensitivity_not_allowed"]++
			continue
		}
		if !boundedString(event.eventID) || !boundedString(event.sourceType) || !boundedOptionalString(event.sourceName) || event.occurredAt.IsZero() {
			omissions["invalid_context_event"]++
			continue
		}
		if event.content.kind != "text" && event.content.kind != "json" && event.content.kind != "reference" {
			omissions["invalid_context_event"]++
			continue
		}
		if event.content.kind == "reference" && (event.content.declaredSize < 0 || !boundedOptionalString(event.content.declaredHash)) {
			omissions["invalid_context_event"]++
			continue
		}
		if event.content.kind != "reference" {
			length := utf8.RuneCountInString(event.content.text)
			if event.content.text == "" || length > remaining {
				omissions["context_input_limit"]++
				continue
			}
			remaining -= length
		}
		result = append(result, wireEvent(event))
	}
	return result, omissions
}

func (c *Client) minimizeMemories(memories []Memory, remaining, limit int) ([]egressMemory, map[string]int) {
	if limit < 0 {
		limit = 0
	}
	result := make([]egressMemory, 0, len(memories))
	omissions := make(map[string]int)
	for _, memory := range memories {
		if len(result) == limit {
			omissions["context_limit"]++
			continue
		}
		if _, ok := c.sensitivities[memory.sensitivity]; !ok {
			omissions["context_memory_sensitivity_not_allowed"]++
			continue
		}
		if !boundedString(memory.memoryID) || !boundedString(memory.revisionID) || !boundedString(memory.spaceID) || memory.content == "" || !validMemoryState(memory.state) || (memory.validFrom != nil && memory.validUntil != nil && !memory.validFrom.Before(*memory.validUntil)) {
			omissions["invalid_context_memory"]++
			continue
		}
		length := utf8.RuneCountInString(memory.content)
		if length > remaining {
			omissions["context_input_limit"]++
			continue
		}
		remaining -= length
		result = append(result, egressMemory{MemoryID: memory.memoryID, RevisionID: memory.revisionID, SpaceID: memory.spaceID, Content: memory.content, State: memory.state, Sensitivity: memory.sensitivity, ProposalCapable: memory.proposalCapable, ValidFrom: cloneTime(memory.validFrom), ValidUntil: cloneTime(memory.validUntil)})
	}
	return result, omissions
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
	secret := resolveSecret(c.secret)
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

func validateProcessOutput(output processOutput, request ProcessRequest, usage usageWire, requestID string) (ProcessResult, error) {
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
		if item.Content == "" || contentChars > request.maxCandidateChars || totalChars > request.maxTotalChars || item.SpaceID != request.target.spaceID {
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
		seenEvidence := make(map[string]struct{}, len(item.Evidence))
		for n, reference := range item.Evidence {
			event, ok := events[reference.EventID]
			if !ok || !validEvidenceType(reference.EvidenceType) || (reference.StartOffset == nil) != (reference.EndOffset == nil) {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_evidence_reference")
			}
			if reference.StartOffset != nil && (event.content.kind == "reference" || *reference.StartOffset < 0 || *reference.EndOffset <= *reference.StartOffset || *reference.EndOffset > int64(utf8.RuneCountInString(event.content.text))) {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_evidence_reference")
			}
			if item.SuggestionKind != "new" && reference.StartOffset == nil {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_evidence_reference")
			}
			key := evidenceKey(reference)
			if _, duplicate := seenEvidence[key]; duplicate {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_evidence_reference")
			}
			seenEvidence[key] = struct{}{}
			evidence[n] = Evidence{eventID: reference.EventID, evidenceType: reference.EvidenceType, startOffset: cloneInt64(reference.StartOffset), endOffset: cloneInt64(reference.EndOffset)}
		}
		memoryIDs := make(map[string]Memory, len(request.contextMemories))
		for _, memory := range request.contextMemories {
			memoryIDs[memory.memoryID] = memory
		}
		seenMemoryIDs := make(map[string]struct{}, len(item.RelatedMemoryIDs))
		for _, relatedID := range item.RelatedMemoryIDs {
			memory, ok := memoryIDs[relatedID]
			if !ok || !boundedString(relatedID) || item.SuggestionKind != "new" && !memory.proposalCapable {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_related_memory_reference")
			}
			if _, duplicate := seenMemoryIDs[relatedID]; duplicate {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_related_memory_reference")
			}
			seenMemoryIDs[relatedID] = struct{}{}
		}
		switch item.SuggestionKind {
		case "supplement", "replacement":
			if len(item.RelatedMemoryIDs) != 1 {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_related_memory_reference")
			}
		case "conflict":
			if len(item.RelatedMemoryIDs) != 2 {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_related_memory_reference")
			}
		case "duplicate":
			if len(item.RelatedMemoryIDs) == 0 {
				return ProcessResult{}, failure(ErrorInvalidOutput, "invalid_related_memory_reference")
			}
		}
		candidates[i] = Candidate{content: item.Content, memoryType: item.MemoryType, spaceID: item.SpaceID, validFrom: cloneTime(item.ValidFrom), validUntil: cloneTime(item.ValidUntil), confidence: item.Confidence, sensitivity: item.Sensitivity, evidence: evidence, suggestion: item.SuggestionKind, relatedIDs: append([]string(nil), item.RelatedMemoryIDs...)}
	}
	return ProcessResult{category: output.Category, candidates: candidates, usage: usageFromWire(usage), requestID: requestID}, nil
}

func evidenceKey(value evidenceWire) string {
	if value.StartOffset == nil {
		return value.EventID + "|-"
	}
	return value.EventID + "|" + strconv.FormatInt(*value.StartOffset, 10) + "|" + strconv.FormatInt(*value.EndOffset, 10)
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
func makeMIMETypeSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = normalizeMIMEType(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}
func normalizeMIMEType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}
func mergeOmissions(destination, source map[string]int) {
	for reason, count := range source {
		destination[reason] += count
	}
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
	case "message", "conversation", "tool_call", "tool_result", "file_reference", "collector_event", "manual", "semantic_query":
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
	MemoryID        string     `json:"memory_id"`
	RevisionID      string     `json:"revision_id"`
	SpaceID         string     `json:"space_id"`
	Content         string     `json:"content"`
	State           string     `json:"state"`
	Sensitivity     string     `json:"sensitivity"`
	ProposalCapable bool       `json:"proposal_capable"`
	ValidFrom       *time.Time `json:"valid_from,omitempty"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
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
