package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

type contractClient struct {
	t      *testing.T
	client *http.Client
	router routers.Router
	seen   map[string]map[int]bool
}

type fakeAggregateProvider struct {
	result domain.AggregateProviderResult
	err    error
	calls  []domain.AggregateProviderInput
}

func (f *fakeAggregateProvider) Build(_ context.Context, input domain.AggregateProviderInput) (domain.AggregateProviderResult, error) {
	f.calls = append(f.calls, input)
	return f.result, f.err
}

func TestOpenAPIContract(t *testing.T) {
	doc := loadOpenAPI(t)
	assertIdempotentResponseHeaders(t, doc)
	assertRecallResponseContract(t, doc)
	cfg := testConfig(t)
	cfg.ProcessingVersion = "contract-v3"
	cfg.ProcessingProvider = config.ProviderConfig{
		Enabled: true,
		Policy: provider.Policy{
			Default:       "deny",
			ContentTypes:  []string{"text/plain"},
			SourceTypes:   []string{"manual"},
			Sensitivities: []string{"normal"},
			Assignments: []provider.PolicyAssignment{
				{Selector: "space", SpaceID: "11111111-1111-4111-8111-111111111111", Effect: "allow"},
			},
		},
	}
	release := make(chan struct{})
	aggregateProvider := &fakeAggregateProvider{result: domain.AggregateProviderResult{Content: "contract aggregate", Sensitivity: "normal", ProviderIdentity: "contract/local", ModelIdentity: "contract-v1"}}
	d, err := startService(context.Background(), cfg, &heldProcessor{release: release}, aggregateProvider, true)
	if err != nil {
		t.Fatal(err)
	}
	client := unixClient(cfg.Endpoint)
	defer func() { close(release); _ = d.Close(context.Background()) }()
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	c := &contractClient{t: t, client: client, router: router, seen: map[string]map[int]bool{}}

	spaceBody := map[string]any{"space_type": "user", "external_id": "contract", "display_name": "Contract"}
	c.close(c.do(http.MethodGet, "/v1/health", "", nil))
	c.close(c.expect(http.StatusServiceUnavailable, http.MethodGet, "/v1/config", "", nil))
	c.close(c.expect(http.StatusServiceUnavailable, http.MethodPost, "/v1/config/reload", "config-unmanaged", map[string]any{}))
	c.invalidCaller(http.MethodGet, "/v1/config")
	c.invalid(http.StatusUnprocessableEntity, http.MethodPost, "/v1/config/reload", "config-invalid", `{"unknown":true}`)
	exerciseConfigurationContract(t, router, c.seen)
	response := c.do(http.MethodPost, "/v1/spaces", "space", spaceBody)
	var space domain.Space
	decodeResponse(t, response, &space)
	c.replay(http.MethodPost, "/v1/spaces", "space", spaceBody)
	c.close(c.do(http.MethodGet, "/v1/spaces?limit=1", "", nil))
	c.close(c.do(http.MethodGet, "/v1/spaces/"+space.SpaceID, "", nil))
	agentMemoryBody := map[string]any{"space_id": space.SpaceID, "content": "agent contract memory", "kind": "fact"}
	response = c.do(http.MethodPost, "/v1/remember", "agent-memory", agentMemoryBody)
	var agentMemory agentRememberResponse
	decodeResponse(t, response, &agentMemory)
	c.replay(http.MethodPost, "/v1/remember", "agent-memory", agentMemoryBody)
	c.close(c.expect(http.StatusOK, http.MethodPost, "/v1/recall", "", map[string]any{"query": "agent contract", "space_ids": []string{space.SpaceID}, "debug": map[string]any{"capabilities": []string{"keyword"}}}))
	agentFeedback := map[string]any{"space_id": space.SpaceID, "feedback_purpose": "memory_quality_feedback", "type": "confirm", "reason_code": "other"}
	c.close(c.do(http.MethodPost, "/v1/memories/"+agentMemory.Memory.MemoryID+"/feedback", "agent-feedback", agentFeedback))
	c.close(c.do(http.MethodPost, "/v1/memories/"+agentMemory.Memory.MemoryID+"/forget", "agent-forget", map[string]any{"reason": "contract forget"}))

	eventBody := map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "contract", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "contract searchable evidence"}, "metadata": map[string]any{},
	}
	response = c.do(http.MethodPost, "/v1/events", "event", eventBody)
	var event domain.IngestResponse
	decodeResponse(t, response, &event)
	c.replay(http.MethodPost, "/v1/events", "event", eventBody)
	for _, path := range []string{"/v1/events?space_id=" + space.SpaceID, "/v1/events/" + event.EventID, "/v1/jobs/" + event.JobIDs[0]} {
		c.close(c.do(http.MethodGet, path, "", nil))
	}
	c.close(c.do(http.MethodPost, "/v1/jobs/"+event.JobIDs[0]+"/cancel", "cancel-event-job", nil))
	waitForJobStatus(t, client, event.JobIDs[0], "cancelled")
	c.replay(http.MethodPost, "/v1/jobs/"+event.JobIDs[0]+"/cancel", "cancel-event-job", nil)

	db, err := sql.Open("sqlite3", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE jobs SET status='failed', last_error_code='contract', last_error_message='contract', finished_at=CURRENT_TIMESTAMP WHERE job_id=?`, event.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	c.close(c.do(http.MethodPost, "/v1/jobs/"+event.JobIDs[0]+"/retry", "retry-event-job", nil))
	c.replay(http.MethodPost, "/v1/jobs/"+event.JobIDs[0]+"/retry", "retry-event-job", nil)
	c.close(c.do(http.MethodPost, "/v1/jobs/"+event.JobIDs[0]+"/cancel", "cancel-retried-job", nil))
	waitForJobStatus(t, client, event.JobIDs[0], "cancelled")
	response = c.doWithReplay(http.MethodPost, "/v1/events/"+event.EventID+"/replay", "replay-event", nil, "")
	response.Body.Close()
	c.replay(http.MethodPost, "/v1/events/"+event.EventID+"/replay", "replay-event", nil)

	memoryBody := memoryCreateBody(space.SpaceID, event.EventID, "contract searchable memory")
	response = c.do(http.MethodPost, "/v1/memories", "memory", memoryBody)
	var memory domain.Memory
	decodeResponse(t, response, &memory)
	c.replay(http.MethodPost, "/v1/memories", "memory", memoryBody)
	for _, path := range []string{"/v1/memories?space_id=" + space.SpaceID, "/v1/memories/" + memory.MemoryID, "/v1/memories/" + memory.MemoryID + "/revisions", "/v1/memories/" + memory.MemoryID + "/state-events"} {
		c.close(c.do(http.MethodGet, path, "", nil))
	}
	reviseBody := memoryReviseBody(memory.CurrentRevision.RevisionID, event.EventID, "revised contract searchable memory")
	response = c.do(http.MethodPatch, "/v1/memories/"+memory.MemoryID, "revise", reviseBody)
	decodeResponse(t, response, &memory)
	c.replay(http.MethodPatch, "/v1/memories/"+memory.MemoryID, "revise", reviseBody)
	stateBody := map[string]any{"expected_revision_id": memory.CurrentRevision.RevisionID, "action": "contest", "reason": "contract test"}
	c.close(c.do(http.MethodPost, "/v1/memories/"+memory.MemoryID+"/state", "state", stateBody))
	c.replay(http.MethodPost, "/v1/memories/"+memory.MemoryID+"/state", "state", stateBody)

	confirmID, rejectID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	seedCandidateAPI(t, db, confirmID, "33333333-3333-4333-8333-333333333333", event.EventID, space.SpaceID, "confirm candidate", "contract-confirm")
	seedCandidateAPI(t, db, rejectID, "44444444-4444-4444-8444-444444444444", event.EventID, space.SpaceID, "reject candidate", "contract-reject")
	c.close(c.do(http.MethodGet, "/v1/candidates?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/candidates/"+confirmID, "", nil))
	reviewBody := map[string]any{"reason_code": "contract", "note": "validated"}
	c.close(c.do(http.MethodPost, "/v1/candidates/"+confirmID+"/confirm", "confirm", reviewBody))
	c.replay(http.MethodPost, "/v1/candidates/"+confirmID+"/confirm", "confirm", reviewBody)
	c.close(c.do(http.MethodPost, "/v1/candidates/"+rejectID+"/reject", "reject", reviewBody))
	c.replay(http.MethodPost, "/v1/candidates/"+rejectID+"/reject", "reject", reviewBody)

	targetMemoryBody := memoryCreateBody(space.SpaceID, event.EventID, "contract target")
	response = c.do(http.MethodPost, "/v1/memories", "target-memory", targetMemoryBody)
	var targetMemory domain.Memory
	decodeResponse(t, response, &targetMemory)
	response = c.do(http.MethodPost, "/v1/memories", "aggregate-memory", memoryCreateBody(space.SpaceID, event.EventID, "aggregate input"))
	var aggregateInput domain.Memory
	decodeResponse(t, response, &aggregateInput)
	response = c.do(http.MethodPost, "/v1/memories", "aggregate-memory-two", memoryCreateBody(space.SpaceID, event.EventID, "aggregate input two"))
	var aggregateInputTwo domain.Memory
	decodeResponse(t, response, &aggregateInputTwo)

	entityBody := map[string]any{"event_id": event.EventID, "space_id": space.SpaceID, "proposal_kind": "entity", "entity_type": "person", "canonical_name": "Contract Person", "sensitivity": "normal", "processor_identity": "contract/processor", "processing_version": "contract-v3"}
	response = c.do(http.MethodPost, "/v1/entity-proposals", "entity-create", entityBody)
	var entityProposal domain.EntityProposal
	decodeResponse(t, response, &entityProposal)
	c.replay(http.MethodPost, "/v1/entity-proposals", "entity-create", entityBody)
	c.close(c.do(http.MethodGet, "/v1/entity-proposals?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/entity-proposals/"+entityProposal.ProposalID, "", nil))
	entityReview := map[string]any{"reason_code": "contract", "note": "reviewed"}
	response = c.do(http.MethodPost, "/v1/entity-proposals/"+entityProposal.ProposalID+"/confirm", "entity-confirm", entityReview)
	decodeResponse(t, response, &entityProposal)
	c.replay(http.MethodPost, "/v1/entity-proposals/"+entityProposal.ProposalID+"/confirm", "entity-confirm", entityReview)
	entityRejectBody := withField(entityBody, "canonical_name", "Rejected Person")
	response = c.do(http.MethodPost, "/v1/entity-proposals", "entity-reject-create", entityRejectBody)
	var entityReject domain.EntityProposal
	decodeResponse(t, response, &entityReject)
	c.close(c.do(http.MethodPost, "/v1/entity-proposals/"+entityReject.ProposalID+"/reject", "entity-reject", entityReview))
	c.replay(http.MethodPost, "/v1/entity-proposals/"+entityReject.ProposalID+"/reject", "entity-reject", entityReview)
	c.close(c.do(http.MethodGet, "/v1/entities?space_id="+space.SpaceID+"&include_mentions=true", "", nil))
	c.close(c.do(http.MethodGet, "/v1/entities/"+*entityProposal.ResultEntityID+"?space_id="+space.SpaceID, "", nil))
	c.close(c.expect(http.StatusNotImplemented, http.MethodPost, "/v1/entities/"+*entityProposal.ResultEntityID+"/merge", "entity-merge", nil))

	relationBody := map[string]any{"event_id": event.EventID, "space_id": space.SpaceID, "relation_type": "supports", "source_memory_id": memory.MemoryID, "source_revision_id": memory.CurrentRevision.RevisionID, "target_memory_id": targetMemory.MemoryID, "target_revision_id": targetMemory.CurrentRevision.RevisionID, "evidence": []map[string]any{{"evidence_type": "human_confirmation"}}, "processor_identity": "contract/processor", "processing_version": "contract-v3"}
	response = c.do(http.MethodPost, "/v1/relation-proposals", "relation-create", relationBody)
	var relationProposal domain.RelationProposal
	decodeResponse(t, response, &relationProposal)
	c.replay(http.MethodPost, "/v1/relation-proposals", "relation-create", relationBody)
	c.close(c.do(http.MethodGet, "/v1/relation-proposals?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/relation-proposals/"+relationProposal.ProposalID, "", nil))
	response = c.do(http.MethodPost, "/v1/relation-proposals/"+relationProposal.ProposalID+"/confirm", "relation-confirm", entityReview)
	decodeResponse(t, response, &relationProposal)
	c.replay(http.MethodPost, "/v1/relation-proposals/"+relationProposal.ProposalID+"/confirm", "relation-confirm", entityReview)
	relationRejectBody := withField(relationBody, "processing_version", "contract-v3-reject")
	response = c.do(http.MethodPost, "/v1/relation-proposals", "relation-reject-create", relationRejectBody)
	var relationReject domain.RelationProposal
	decodeResponse(t, response, &relationReject)
	c.close(c.do(http.MethodPost, "/v1/relation-proposals/"+relationReject.ProposalID+"/reject", "relation-reject", entityReview))
	c.replay(http.MethodPost, "/v1/relation-proposals/"+relationReject.ProposalID+"/reject", "relation-reject", entityReview)
	c.close(c.do(http.MethodGet, "/v1/relations?space_id="+space.SpaceID+"&memory_id="+memory.MemoryID+"&direction=outgoing", "", nil))
	c.close(c.do(http.MethodGet, "/v1/relations/"+*relationProposal.ResultRelationID+"?space_id="+space.SpaceID, "", nil))

	factBody := map[string]any{"event_id": event.EventID, "space_id": space.SpaceID, "subject_entity_id": *entityProposal.ResultEntityID, "predicate": "has_status", "object_literal": "active", "object_literal_type": "string", "source_memory_id": memory.MemoryID, "source_revision_id": memory.CurrentRevision.RevisionID, "confidence": 0.9, "evidence": []map[string]any{{"evidence_type": "human_confirmation"}}, "processor_identity": "contract/processor", "processing_version": "contract-v3"}
	response = c.do(http.MethodPost, "/v1/fact-proposals", "fact-create", factBody)
	var factProposal domain.FactProposal
	decodeResponse(t, response, &factProposal)
	c.replay(http.MethodPost, "/v1/fact-proposals", "fact-create", factBody)
	c.close(c.do(http.MethodGet, "/v1/fact-proposals?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/fact-proposals/"+factProposal.ProposalID, "", nil))
	response = c.do(http.MethodPost, "/v1/fact-proposals/"+factProposal.ProposalID+"/confirm", "fact-confirm", entityReview)
	decodeResponse(t, response, &factProposal)
	c.replay(http.MethodPost, "/v1/fact-proposals/"+factProposal.ProposalID+"/confirm", "fact-confirm", entityReview)
	factRejectBody := withField(factBody, "object_literal", "paused")
	response = c.do(http.MethodPost, "/v1/fact-proposals", "fact-reject-create", factRejectBody)
	var factReject domain.FactProposal
	decodeResponse(t, response, &factReject)
	c.close(c.do(http.MethodPost, "/v1/fact-proposals/"+factReject.ProposalID+"/reject", "fact-reject", entityReview))
	c.replay(http.MethodPost, "/v1/fact-proposals/"+factReject.ProposalID+"/reject", "fact-reject", entityReview)
	c.close(c.do(http.MethodGet, "/v1/entity-facts?space_id="+space.SpaceID+"&predicate=has_status", "", nil))
	c.close(c.do(http.MethodGet, "/v1/entity-facts/"+*factProposal.ResultFactID+"?space_id="+space.SpaceID, "", nil))

	targets := []map[string]any{{"memory_id": memory.MemoryID, "expected_revision_id": memory.CurrentRevision.RevisionID}, {"memory_id": targetMemory.MemoryID, "expected_revision_id": targetMemory.CurrentRevision.RevisionID}}
	evolutionBody := map[string]any{"event_id": event.EventID, "space_id": space.SpaceID, "action": "conflict", "proposed_content": "contract conflict", "sensitivity": "normal", "processor_identity": "contract/processor", "processing_version": "contract-v3", "targets": targets, "evidence": []map[string]any{{"evidence_type": "raw_event_range", "event_id": event.EventID, "start_offset": 0, "end_offset": 8}}}
	response = c.do(http.MethodPost, "/v1/evolution-proposals", "evolution-create", evolutionBody)
	var evolution domain.EvolutionProposal
	decodeResponse(t, response, &evolution)
	c.replay(http.MethodPost, "/v1/evolution-proposals", "evolution-create", evolutionBody)
	c.close(c.do(http.MethodGet, "/v1/evolution-proposals?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/evolution-proposals/"+evolution.ProposalID, "", nil))
	evolutionReview := map[string]any{"expected_targets": targets, "reason_code": "contract"}
	response = c.do(http.MethodPost, "/v1/evolution-proposals/"+evolution.ProposalID+"/confirm", "evolution-confirm", evolutionReview)
	decodeResponse(t, response, &evolution)
	c.replay(http.MethodPost, "/v1/evolution-proposals/"+evolution.ProposalID+"/confirm", "evolution-confirm", evolutionReview)
	caseID := ""
	for _, result := range evolution.Reviews[0].Results {
		if result.ResultType == "conflict_case" {
			caseID = result.ResultID
		}
	}
	if caseID == "" {
		t.Fatal("confirmed evolution proposal has no conflict case")
	}
	evolutionRejectBody := withField(evolutionBody, "proposed_content", "rejected contract conflict")
	response = c.do(http.MethodPost, "/v1/evolution-proposals", "evolution-reject-create", evolutionRejectBody)
	var evolutionReject domain.EvolutionProposal
	decodeResponse(t, response, &evolutionReject)
	c.close(c.do(http.MethodPost, "/v1/evolution-proposals/"+evolutionReject.ProposalID+"/reject", "evolution-reject", map[string]any{"reason_code": "contract"}))
	c.replay(http.MethodPost, "/v1/evolution-proposals/"+evolutionReject.ProposalID+"/reject", "evolution-reject", map[string]any{"reason_code": "contract"})
	c.close(c.do(http.MethodGet, "/v1/conflict-cases?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/conflict-cases/"+caseID, "", nil))
	resolveBody := map[string]any{"resolution_type": "keep_both", "expected_targets": targets, "result_states": map[string]string{memory.MemoryID: "contested", targetMemory.MemoryID: "active"}, "reason": "contract resolution"}
	c.close(c.expect(http.StatusOK, http.MethodPost, "/v1/conflict-cases/"+caseID+"/resolve", "conflict-resolve", resolveBody))
	c.replay(http.MethodPost, "/v1/conflict-cases/"+caseID+"/resolve", "conflict-resolve", resolveBody)

	feedbackBody := map[string]any{"space_id": space.SpaceID, "memory_id": targetMemory.MemoryID, "revision_id": targetMemory.CurrentRevision.RevisionID, "feedback_purpose": "memory_quality_feedback", "feedback_type": "confirm", "reason_code": "other", "note": "contract feedback"}
	c.close(c.do(http.MethodPost, "/v1/feedback", "feedback-create", feedbackBody))
	c.replay(http.MethodPost, "/v1/feedback", "feedback-create", feedbackBody)
	c.close(c.do(http.MethodGet, "/v1/feedback?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/feedback/aggregates/health", "", nil))

	aggregateBody := map[string]any{"space_id": space.SpaceID, "aggregate_kind": "scene", "input_revision_ids": []string{aggregateInput.CurrentRevision.RevisionID, aggregateInputTwo.CurrentRevision.RevisionID}}
	response = c.expect(http.StatusCreated, http.MethodPost, "/v1/aggregate-proposals", "aggregate-create", aggregateBody)
	var aggregateProposal domain.AggregateProposal
	decodeResponse(t, response, &aggregateProposal)
	c.replay(http.MethodPost, "/v1/aggregate-proposals", "aggregate-create", aggregateBody)
	c.close(c.do(http.MethodGet, "/v1/aggregate-proposals?space_id="+space.SpaceID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/aggregate-proposals/"+aggregateProposal.ProposalID, "", nil))
	response = c.do(http.MethodPost, "/v1/aggregate-proposals/"+aggregateProposal.ProposalID+"/confirm", "aggregate-confirm", entityReview)
	decodeResponse(t, response, &aggregateProposal)
	c.replay(http.MethodPost, "/v1/aggregate-proposals/"+aggregateProposal.ProposalID+"/confirm", "aggregate-confirm", entityReview)
	aggregateRejectBody := withField(aggregateBody, "aggregate_kind", "topic")
	response = c.do(http.MethodPost, "/v1/aggregate-proposals", "aggregate-reject-create", aggregateRejectBody)
	var aggregateReject domain.AggregateProposal
	decodeResponse(t, response, &aggregateReject)
	c.close(c.do(http.MethodPost, "/v1/aggregate-proposals/"+aggregateReject.ProposalID+"/reject", "aggregate-reject", entityReview))
	c.replay(http.MethodPost, "/v1/aggregate-proposals/"+aggregateReject.ProposalID+"/reject", "aggregate-reject", entityReview)
	response = c.do(http.MethodGet, "/v1/aggregates?space_id="+space.SpaceID, "", nil)
	var aggregatePage domain.AggregatePage
	decodeResponse(t, response, &aggregatePage)
	aggregateID := aggregatePage.Items[0].AggregateID
	c.close(c.do(http.MethodGet, "/v1/aggregates/"+aggregateID, "", nil))
	c.close(c.do(http.MethodGet, "/v1/aggregates/"+aggregateID+"/provenance", "", nil))

	response = c.do(http.MethodPost, "/v1/recall", "", map[string]any{"query": "contract", "space_ids": []string{space.SpaceID}, "states": []string{"active", "contested"}, "debug": map[string]any{"capabilities": []string{"keyword"}}})
	var keyword domain.RecallResponse
	decodeResponse(t, response, &keyword)
	if !reflect.DeepEqual(keyword.Execution.RequestedCapabilities, []string{"keyword"}) || !reflect.DeepEqual(keyword.Execution.ExecutedCapabilities, []string{"keyword"}) || keyword.Execution.QueryPlanIdentity == "" {
		t.Fatalf("keyword ready response = %+v", keyword)
	}
	response = c.do(http.MethodPost, "/v1/recall", "", map[string]any{"query": "contract", "space_ids": []string{space.SpaceID}, "states": []string{"active", "contested"}, "debug": map[string]any{"capabilities": []string{"keyword", "semantic"}}})
	var hybrid domain.RecallResponse
	decodeResponse(t, response, &hybrid)
	if !reflect.DeepEqual(hybrid.Execution.RequestedCapabilities, []string{"keyword", "semantic"}) || !reflect.DeepEqual(hybrid.Execution.ExecutedCapabilities, []string{"keyword"}) || !hybrid.Execution.Degraded || len(hybrid.Execution.DegradationReasons) == 0 {
		t.Fatalf("hybrid degraded response = %+v", hybrid)
	}
	response = c.do(http.MethodPost, "/v1/indexes/fts/rebuild", "rebuild", nil)
	var rebuild domain.JobReference
	decodeResponse(t, response, &rebuild)
	c.replay(http.MethodPost, "/v1/indexes/fts/rebuild", "rebuild", nil)
	c.close(c.expect(http.StatusConflict, http.MethodPost, "/v1/indexes/fts/rebuild", "rebuild-active", nil))
	c.close(c.do(http.MethodPost, "/v1/jobs/"+rebuild.JobID+"/cancel", "cancel", nil))
	c.replay(http.MethodPost, "/v1/jobs/"+rebuild.JobID+"/cancel", "cancel", nil)
	response = c.do(http.MethodPost, "/v1/indexes/semantic/rebuild", "semantic-rebuild", nil)
	var semanticRebuild domain.JobReference
	decodeResponse(t, response, &semanticRebuild)
	c.replay(http.MethodPost, "/v1/indexes/semantic/rebuild", "semantic-rebuild", nil)
	c.close(c.expect(http.StatusConflict, http.MethodPost, "/v1/indexes/semantic/rebuild", "semantic-rebuild-active", nil))
	for _, jobType := range []string{"rebuild_entity_index", "rebuild_relation_index", "rebuild_graph_index", "rebuild_feedback_aggregates"} {
		if _, err := db.Exec(`UPDATE jobs SET status='succeeded', finished_at=CURRENT_TIMESTAMP WHERE job_type=? AND status IN ('pending','waiting','running','retrying')`, jobType); err != nil {
			t.Fatal(err)
		}
	}
	for _, rebuildRoute := range []struct{ path, key string }{
		{"/v1/indexes/graph/rebuild", "graph-index-rebuild"},
		{"/v1/feedback/aggregates/rebuild", "feedback-rebuild"},
	} {
		c.close(c.expect(http.StatusAccepted, http.MethodPost, rebuildRoute.path, rebuildRoute.key, nil))
		c.replay(http.MethodPost, rebuildRoute.path, rebuildRoute.key, nil)
		c.close(c.expect(http.StatusConflict, http.MethodPost, rebuildRoute.path, rebuildRoute.key+"-active", nil))
	}

	c.invalid(http.StatusUnprocessableEntity, http.MethodGet, "/v1/spaces/not-a-uuid", "", "")
	c.invalid(http.StatusBadRequest, http.MethodPost, "/v1/recall", "", "{")

	unknownID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := db.Exec(`UPDATE jobs SET status='succeeded', result_code='contract', finished_at=CURRENT_TIMESTAMP WHERE job_id=?`, rebuild.JobID); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, key string
		status            int
		body              any
	}{
		{http.MethodPost, "/v1/spaces", "space", http.StatusConflict, map[string]any{"space_type": "user", "external_id": "different", "display_name": "Contract"}},
		{http.MethodGet, "/v1/spaces/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/events", "event", http.StatusConflict, withField(eventBody, "source_name", "different")},
		{http.MethodGet, "/v1/events/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/events/" + unknownID + "/replay", "missing-event", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/events/" + unknownID + "/replay", "replay-event", http.StatusConflict, nil},
		{http.MethodPost, "/v1/memories", "memory", http.StatusConflict, withField(memoryBody, "content", "different")},
		{http.MethodGet, "/v1/memories/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPatch, "/v1/memories/" + memory.MemoryID, "stale-revision", http.StatusConflict, memoryReviseBody(reviseBody["expected_revision_id"].(string), event.EventID, "stale")},
		{http.MethodPatch, "/v1/memories/" + unknownID, "missing-memory", http.StatusNotFound, memoryReviseBody(memory.CurrentRevision.RevisionID, event.EventID, "missing")},
		{http.MethodGet, "/v1/memories/" + unknownID + "/revisions", "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/memories/" + memory.MemoryID + "/state", "stale-state", http.StatusConflict, map[string]any{"expected_revision_id": reviseBody["expected_revision_id"], "action": "archive", "reason": "stale"}},
		{http.MethodPost, "/v1/memories/" + unknownID + "/state", "missing-state", http.StatusNotFound, map[string]any{"expected_revision_id": memory.CurrentRevision.RevisionID, "action": "contest", "reason": "missing"}},
		{http.MethodGet, "/v1/memories/" + unknownID + "/state-events", "", http.StatusNotFound, nil},
		{http.MethodGet, "/v1/candidates/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/candidates/" + confirmID + "/confirm", "reviewed-confirm", http.StatusConflict, reviewBody},
		{http.MethodPost, "/v1/candidates/" + rejectID + "/reject", "reviewed-reject", http.StatusConflict, reviewBody},
		{http.MethodPost, "/v1/candidates/" + unknownID + "/confirm", "missing-confirm", http.StatusNotFound, reviewBody},
		{http.MethodPost, "/v1/candidates/" + unknownID + "/reject", "missing-reject", http.StatusNotFound, reviewBody},
		{http.MethodPost, "/v1/recall", "", http.StatusServiceUnavailable, map[string]any{"query": "contract", "space_ids": []string{space.SpaceID}, "debug": map[string]any{"capabilities": []string{"semantic"}}}},
		{http.MethodGet, "/v1/jobs/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/jobs/" + unknownID + "/cancel", "missing-cancel", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/jobs/" + unknownID + "/retry", "missing-retry", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/jobs/" + rebuild.JobID + "/retry", "retry-active", http.StatusConflict, nil},
		{http.MethodPost, "/v1/jobs/" + rebuild.JobID + "/cancel", "cancel-terminal", http.StatusConflict, nil},
		{http.MethodPost, "/v1/entity-proposals", "entity-create", http.StatusConflict, withField(entityBody, "canonical_name", "different")},
		{http.MethodGet, "/v1/entity-proposals/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/entity-proposals/" + unknownID + "/confirm", "entity-confirm-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/entity-proposals/" + unknownID + "/reject", "entity-reject-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/entity-proposals/" + entityProposal.ProposalID + "/confirm", "entity-confirm-terminal", http.StatusConflict, entityReview},
		{http.MethodPost, "/v1/entity-proposals/" + entityReject.ProposalID + "/reject", "entity-reject-terminal", http.StatusConflict, entityReview},
		{http.MethodGet, "/v1/entities/" + unknownID + "?space_id=" + space.SpaceID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/relation-proposals", "relation-create", http.StatusConflict, withField(relationBody, "relation_type", "contradicts")},
		{http.MethodGet, "/v1/relation-proposals/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/relation-proposals/" + unknownID + "/confirm", "relation-confirm-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/relation-proposals/" + unknownID + "/reject", "relation-reject-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/relation-proposals/" + relationProposal.ProposalID + "/confirm", "relation-confirm-terminal", http.StatusConflict, entityReview},
		{http.MethodPost, "/v1/relation-proposals/" + relationReject.ProposalID + "/reject", "relation-reject-terminal", http.StatusConflict, entityReview},
		{http.MethodGet, "/v1/relations/" + unknownID + "?space_id=" + space.SpaceID, "", http.StatusNotFound, nil},
		{http.MethodGet, "/v1/fact-proposals/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/fact-proposals/" + unknownID + "/confirm", "fact-confirm-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/fact-proposals/" + unknownID + "/reject", "fact-reject-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/fact-proposals/" + factProposal.ProposalID + "/confirm", "fact-confirm-terminal", http.StatusConflict, entityReview},
		{http.MethodPost, "/v1/fact-proposals/" + factReject.ProposalID + "/reject", "fact-reject-terminal", http.StatusConflict, entityReview},
		{http.MethodGet, "/v1/entity-facts/" + unknownID + "?space_id=" + space.SpaceID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/fact-proposals", "fact-create", http.StatusConflict, withField(factBody, "object_literal", "different")},
		{http.MethodPost, "/v1/evolution-proposals", "evolution-create", http.StatusConflict, withField(evolutionBody, "proposed_content", "different")},
		{http.MethodGet, "/v1/evolution-proposals/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/evolution-proposals/" + unknownID + "/confirm", "evolution-confirm-missing", http.StatusNotFound, evolutionReview},
		{http.MethodPost, "/v1/evolution-proposals/" + unknownID + "/reject", "evolution-reject-missing", http.StatusNotFound, map[string]any{"reason_code": "contract"}},
		{http.MethodPost, "/v1/evolution-proposals/" + evolution.ProposalID + "/confirm", "evolution-confirm-terminal", http.StatusConflict, evolutionReview},
		{http.MethodPost, "/v1/evolution-proposals/" + evolutionReject.ProposalID + "/reject", "evolution-reject-terminal", http.StatusConflict, map[string]any{"reason_code": "contract"}},
		{http.MethodGet, "/v1/conflict-cases/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/conflict-cases/" + unknownID + "/resolve", "conflict-missing", http.StatusNotFound, resolveBody},
		{http.MethodPost, "/v1/conflict-cases/" + caseID + "/resolve", "conflict-terminal", http.StatusConflict, resolveBody},
		{http.MethodPost, "/v1/feedback", "feedback-create", http.StatusConflict, withField(feedbackBody, "note", "different")},
		{http.MethodPost, "/v1/feedback", "feedback-missing", http.StatusNotFound, withField(feedbackBody, "memory_id", unknownID)},
		{http.MethodPost, "/v1/aggregate-proposals", "aggregate-create", http.StatusConflict, withField(aggregateBody, "aggregate_kind", "topic")},
		{http.MethodGet, "/v1/aggregate-proposals/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodPost, "/v1/aggregate-proposals/" + unknownID + "/confirm", "aggregate-confirm-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/aggregate-proposals/" + unknownID + "/reject", "aggregate-reject-missing", http.StatusNotFound, entityReview},
		{http.MethodPost, "/v1/aggregate-proposals/" + aggregateProposal.ProposalID + "/confirm", "aggregate-confirm-terminal", http.StatusConflict, entityReview},
		{http.MethodPost, "/v1/aggregate-proposals/" + aggregateReject.ProposalID + "/reject", "aggregate-reject-terminal", http.StatusConflict, entityReview},
		{http.MethodGet, "/v1/aggregates/" + unknownID, "", http.StatusNotFound, nil},
		{http.MethodGet, "/v1/aggregates/" + unknownID + "/provenance", "", http.StatusNotFound, nil},
	} {
		c.close(c.expect(test.status, test.method, test.path, test.key, test.body))
	}

	for _, test := range []struct {
		method, path, key, raw string
		status                 int
	}{
		{http.MethodPost, "/v1/spaces", "bad-space-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/spaces", "unknown-space", `{"space_type":"user","external_id":"x","display_name":"x","unknown":true}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/spaces?limit=0", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/events", "bad-event-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/events", "unknown-event", `{"unknown":true}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/events?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/events/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/events/not-a-uuid/replay", "bad-replay-id", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/memories", "bad-memory-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/memories", "unknown-memory", `{"unknown":true}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/memories?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/memories/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPatch, "/v1/memories/" + memory.MemoryID, "bad-revise-json", "{", http.StatusBadRequest},
		{http.MethodPatch, "/v1/memories/not-a-uuid", "bad-revise-id", "{}", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/memories/not-a-uuid/revisions", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/memories/" + memory.MemoryID + "/state", "bad-state-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/memories/not-a-uuid/state", "bad-state-id", "{}", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/memories/not-a-uuid/state-events", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/candidates/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/candidates?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/candidates/" + confirmID + "/confirm", "bad-confirm-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/candidates/" + confirmID + "/confirm", "bad-confirm", `{"reason_code":""}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/candidates/" + rejectID + "/reject", "bad-reject-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/candidates/" + rejectID + "/reject", "bad-reject", `{"reason_code":""}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/recall", "", `{"query":"","space_ids":["` + space.SpaceID + `"]}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/recall", "", `{"query":"contract","space_ids":[],"unknown":true}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/jobs/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/jobs/not-a-uuid/cancel", "bad-cancel-id", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/jobs/not-a-uuid/retry", "bad-retry-id", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/indexes/fts/rebuild", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/indexes/semantic/rebuild", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/entity-proposals", "entity-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/entity-proposals", "entity-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/entity-proposals?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/entity-proposals/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/entity-proposals/" + entityProposal.ProposalID + "/confirm", "entity-confirm-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/entity-proposals/" + entityProposal.ProposalID + "/confirm", "entity-confirm-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/entity-proposals/" + entityReject.ProposalID + "/reject", "entity-reject-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/entity-proposals/" + entityReject.ProposalID + "/reject", "entity-reject-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/entities?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/entities/not-a-uuid?space_id=" + space.SpaceID, "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/entities/not-a-uuid/merge", "entity-merge-invalid", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/relation-proposals", "relation-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/relation-proposals", "relation-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/relation-proposals?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/relation-proposals/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/relation-proposals/" + relationProposal.ProposalID + "/confirm", "relation-confirm-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/relation-proposals/" + relationProposal.ProposalID + "/confirm", "relation-confirm-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/relation-proposals/" + relationReject.ProposalID + "/reject", "relation-reject-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/relation-proposals/" + relationReject.ProposalID + "/reject", "relation-reject-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/relations?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/relations/not-a-uuid?space_id=" + space.SpaceID, "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/fact-proposals", "fact-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/fact-proposals", "fact-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/fact-proposals?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/fact-proposals/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/fact-proposals/" + factProposal.ProposalID + "/confirm", "fact-confirm-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/fact-proposals/" + factProposal.ProposalID + "/confirm", "fact-confirm-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/fact-proposals/" + factReject.ProposalID + "/reject", "fact-reject-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/fact-proposals/" + factReject.ProposalID + "/reject", "fact-reject-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/entity-facts?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/entity-facts/not-a-uuid?space_id=" + space.SpaceID, "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/indexes/graph/rebuild", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/evolution-proposals", "evolution-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/evolution-proposals", "evolution-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/evolution-proposals?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/evolution-proposals/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/evolution-proposals/" + evolution.ProposalID + "/confirm", "evolution-confirm-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/evolution-proposals/" + evolution.ProposalID + "/confirm", "evolution-confirm-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/evolution-proposals/" + evolutionReject.ProposalID + "/reject", "evolution-reject-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/evolution-proposals/" + evolutionReject.ProposalID + "/reject", "evolution-reject-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/conflict-cases?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/conflict-cases/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/conflict-cases/" + caseID + "/resolve", "conflict-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/conflict-cases/" + caseID + "/resolve", "conflict-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/feedback", "feedback-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/feedback", "feedback-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/feedback?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/feedback/aggregates/rebuild", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/aggregate-proposals", "aggregate-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/aggregate-proposals", "aggregate-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/aggregate-proposals?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/aggregate-proposals/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/aggregate-proposals/" + aggregateProposal.ProposalID + "/confirm", "aggregate-confirm-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/aggregate-proposals/" + aggregateProposal.ProposalID + "/confirm", "aggregate-confirm-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodPost, "/v1/aggregate-proposals/" + aggregateReject.ProposalID + "/reject", "aggregate-reject-json", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/aggregate-proposals/" + aggregateReject.ProposalID + "/reject", "aggregate-reject-invalid", `{}`, http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/aggregates?space_id=bad", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/aggregates/not-a-uuid", "", "", http.StatusUnprocessableEntity},
		{http.MethodGet, "/v1/aggregates/not-a-uuid/provenance", "", "", http.StatusUnprocessableEntity},
	} {
		c.invalid(test.status, test.method, test.path, test.key, test.raw)
	}
	c.invalidCaller(http.MethodGet, "/v1/feedback/aggregates/health")

	if _, err := db.Exec(`UPDATE index_metadata SET state='degraded' WHERE index_name='memory_fts'; UPDATE entity_index_metadata SET state='ready',source_entity_count=(SELECT count(*) FROM entities),source_mention_count=(SELECT count(*) FROM entity_mentions) WHERE singleton=1; UPDATE relation_index_metadata SET state='ready',source_relation_count=(SELECT count(*) FROM memory_relations WHERE state='active') WHERE singleton=1; UPDATE graph_index_metadata SET state='ready',source_fact_count=(SELECT count(*) FROM entity_facts WHERE state='active'),source_entity_count=(SELECT count(*) FROM entities WHERE state='active'),source_relation_count=(SELECT count(*) FROM memory_relations WHERE state='active') WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	response = c.expect(http.StatusOK, http.MethodPost, "/v1/recall", "", map[string]any{"query": "contract", "space_ids": []string{space.SpaceID}, "states": []string{"active", "contested"}})
	var degraded domain.RecallResponse
	decodeResponse(t, response, &degraded)
	if !degraded.Execution.Degraded || !reflect.DeepEqual(degraded.Execution.ExecutedCapabilities, []string{"graph"}) || !containsString(degraded.Execution.DegradationReasons, "keyword_unavailable") {
		t.Fatalf("keyword degraded response = %+v", degraded)
	}
	exerciseOverloadContract(t, router, c.seen)
	exerciseResourceExhaustedContract(t, router, c.seen)

	assertOperationStatusMatrix(t, doc, c.seen)
}

func TestRecallOpenAPIReplacementContract(t *testing.T) {
	doc := loadOpenAPI(t)
	assertRecallResponseContract(t, doc)
	request := doc.Components.Schemas["RecallRequest"].Value
	for _, property := range []string{"intent", "reference_time", "max_results", "max_context_tokens", "include_evidence", "explain"} {
		if request.Properties[property] == nil {
			t.Errorf("RecallRequest is missing property %q", property)
		}
	}
	debug := request.Properties["debug"]
	if debug == nil || debug.Value == nil || debug.Value.Properties["capabilities"] == nil || debug.Value.Properties["capabilities"].Value.Items == nil {
		t.Fatalf("RecallRequest is missing the evaluation/debug capability override contract")
	}
	debugCapabilities := debug.Value.Properties["capabilities"].Value.Items.Value
	for _, expected := range []string{"keyword", "semantic", "graph"} {
		found := false
		for _, candidate := range debugCapabilities.Enum {
			found = found || candidate == expected
		}
		if !found {
			t.Errorf("debug.capabilities enum is missing %q", expected)
		}
	}
}

func TestOpenAPIReplacementMemoryKindContract(t *testing.T) {
	doc := loadOpenAPI(t)
	kind := doc.Components.Schemas["MemoryKind"]
	if kind == nil || kind.Value == nil {
		t.Fatal("OpenAPI is missing the MemoryKind schema")
	}
	replacement := []string{"fact", "event", "preference", "plan", "procedure", "scene", "topic", "timeline"}
	seen := map[string]bool{}
	for _, value := range kind.Value.Enum {
		name, ok := value.(string)
		if !ok {
			t.Errorf("MemoryKind enum contains non-string value %v", value)
			continue
		}
		seen[name] = true
	}
	for _, want := range replacement {
		if !seen[want] {
			t.Errorf("MemoryKind enum is missing %q", want)
		}
	}
	if len(seen) != len(replacement) {
		t.Errorf("MemoryKind enum = %v, want exactly %v", kind.Value.Enum, replacement)
	}
	candidate := doc.Components.Schemas["Candidate"].Value
	revision := candidate.Properties["revision"]
	if revision == nil || !strings.HasSuffix(revision.Ref, "/MemoryRevision") {
		t.Errorf("Candidate.revision does not reference MemoryRevision: %+v", revision)
	}
	parameters := doc.Paths.Map()["/candidates"].Get.Parameters
	found := false
	for _, parameter := range parameters {
		if parameter.Value != nil && parameter.Value.Name == "memory_type" {
			found = true
			if parameter.Value.Schema == nil || !strings.HasSuffix(parameter.Value.Schema.Ref, "/MemoryKind") {
				t.Errorf("candidate memory_type query parameter does not reference MemoryKind")
			}
		}
	}
	if !found {
		t.Error("candidate memory_type query parameter is missing")
	}
	for _, schema := range []string{"CreateEvolutionProposalRequest", "EvolutionProposal"} {
		property := doc.Components.Schemas[schema].Value.Properties["memory_type"]
		if property == nil || property.Value == nil {
			t.Fatalf("%s.memory_type is missing", schema)
		}
		allowed := map[string]bool{}
		for _, value := range property.Value.Enum {
			if name, ok := value.(string); ok {
				allowed[name] = true
			}
		}
		for _, want := range replacement {
			if !allowed[want] {
				t.Errorf("%s.memory_type is missing replacement kind %q", schema, want)
			}
		}
	}
}

func TestOpenAPICurrentPredicateSnapshot(t *testing.T) {
	doc := loadOpenAPI(t)
	predicate := doc.Components.Schemas["Predicate"]
	if predicate == nil || predicate.Value == nil {
		t.Fatal("OpenAPI is missing the Predicate schema")
	}
	got := make([]string, 0, len(predicate.Value.Enum))
	for _, value := range predicate.Value.Enum {
		name, ok := value.(string)
		if !ok {
			t.Fatalf("Predicate enum contains non-string value %v", value)
		}
		got = append(got, name)
	}
	want := domain.PredicateNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenAPI predicates = %v, want %v", got, want)
	}
}

func exerciseConfigurationContract(t *testing.T, router routers.Router, seen map[string]map[int]bool) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	writeReloadConfig(t, path, reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime")})
	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	c := &contractClient{t: t, client: unixClient(d.endpoint), router: router, seen: seen}
	c.close(c.expect(http.StatusOK, http.MethodGet, "/v1/config", "", nil))
	c.close(c.expect(http.StatusOK, http.MethodPost, "/v1/config/reload", "config-no-change", map[string]any{}))
	c.close(c.expect(http.StatusConflict, http.MethodPost, "/v1/config/reload", "config-conflict", map[string]any{"expected_effective_identity": "sha256:0000000000000000000000000000000000000000000000000000000000000000"}))
}

func exerciseResourceExhaustedContract(t *testing.T, router routers.Router, seen map[string]map[int]bool) {
	t.Helper()
	normal := testConfig(t)
	d, release, client := startHeldDaemon(t, normal)
	c := &contractClient{t: t, client: client, router: router, seen: seen}
	response := c.do(http.MethodPost, "/v1/spaces", "pressure-space", map[string]any{"space_type": "user", "external_id": "pressure", "display_name": "Pressure"})
	var space domain.Space
	decodeResponse(t, response, &space)
	close(release)
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	pressure := testConfig(t)
	pressure.DataDir, pressure.DatabasePath = normal.DataDir, normal.DatabasePath
	pressure.DiskHardFree, pressure.DiskSoftFree = ^uint64(0), ^uint64(0)
	d, err := Start(context.Background(), pressure)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	c.client = unixClient(pressure.Endpoint)
	body := map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "pressure", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "pressure"},
	}
	c.close(c.expect(http.StatusInsufficientStorage, http.MethodPost, "/v1/events", "pressure-event", body))
	c.close(c.expect(http.StatusInsufficientStorage, http.MethodPost, "/v1/indexes/fts/rebuild", "pressure-fts", nil))
	c.close(c.expect(http.StatusInsufficientStorage, http.MethodPost, "/v1/indexes/semantic/rebuild", "pressure-semantic", nil))
	c.close(c.expect(http.StatusInsufficientStorage, http.MethodPost, "/v1/indexes/graph/rebuild", "pressure-graph", nil))
	c.close(c.expect(http.StatusInsufficientStorage, http.MethodPost, "/v1/feedback/aggregates/rebuild", "pressure-feedback", nil))
}

func exerciseOverloadContract(t *testing.T, router routers.Router, seen map[string]map[int]bool) {
	t.Helper()
	cfg := testConfig(t)
	cfg.QueueSoftLimit, cfg.QueueHardLimit = 1, 2
	release := make(chan struct{})
	d, err := start(context.Background(), cfg, &heldProcessor{release: release})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(release)
		_ = d.Close(context.Background())
	}()
	c := &contractClient{t: t, client: unixClient(cfg.Endpoint), router: router, seen: seen}
	response := c.do(http.MethodPost, "/v1/spaces", "overload-space", map[string]any{"space_type": "user", "external_id": "overload", "display_name": "Overload"})
	var space domain.Space
	decodeResponse(t, response, &space)
	for i := 0; i < 2; i++ {
		body := map[string]any{
			"space_id": space.SpaceID, "source_type": "manual", "source_name": "overload", "occurred_at": "2026-08-21T00:00:00Z",
			"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": strconv.Itoa(i)},
		}
		c.close(c.expect(http.StatusAccepted, http.MethodPost, "/v1/events", "overload-"+strconv.Itoa(i), body))
	}
	body := map[string]any{
		"space_id": space.SpaceID, "source_type": "manual", "source_name": "overload", "occurred_at": "2026-08-21T00:00:00Z",
		"content": map[string]any{"kind": "text", "media_type": "text/plain", "text": "full"},
	}
	c.close(c.expect(http.StatusTooManyRequests, http.MethodPost, "/v1/events", "overload-full", body))
}

func loadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	openapi3.DefineStringFormat("uuid", openapi3.FormatOfStringForUUIDOfRFC4122)
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromFile(filepath.Join("..", "..", "docs", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load OpenAPI: %v", err)
	}
	for _, schema := range doc.Components.Schemas {
		adaptOpenAPI31Nulls(schema.Value, map[*openapi3.Schema]bool{})
	}
	if err := doc.Validate(validationContext()); err != nil {
		t.Fatalf("validate OpenAPI: %v", err)
	}
	return doc
}

func adaptOpenAPI31Nulls(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) {
	if schema == nil || seen[schema] {
		return
	}
	seen[schema] = true
	if schema.Type != nil && schema.Type.Includes("null") {
		types := schema.Type.Slice()
		withoutNull := make(openapi3.Types, 0, len(types)-1)
		for _, value := range types {
			if value != "null" {
				withoutNull = append(withoutNull, value)
			}
		}
		schema.Type = &withoutNull
		schema.Nullable = true
	}
	for _, property := range schema.Properties {
		adaptOpenAPI31Nulls(property.Value, seen)
	}
	if schema.Items != nil {
		adaptOpenAPI31Nulls(schema.Items.Value, seen)
	}
	for _, alternatives := range []openapi3.SchemaRefs{schema.OneOf, schema.AnyOf, schema.AllOf} {
		for _, alternative := range alternatives {
			adaptOpenAPI31Nulls(alternative.Value, seen)
		}
	}
}

func assertIdempotentResponseHeaders(t *testing.T, doc *openapi3.T) {
	t.Helper()
	for path, item := range doc.Paths.Map() {
		for method, operation := range item.Operations() {
			idempotent := false
			for _, parameter := range operation.Parameters {
				if parameter.Value != nil && parameter.Value.Name == "Idempotency-Key" {
					idempotent = true
				}
			}
			if !idempotent {
				continue
			}
			for status, response := range operation.Responses.Map() {
				hasHeader := false
				if response.Value != nil {
					_, hasHeader = response.Value.Headers["Idempotent-Replay"]
				}
				if strings.HasPrefix(status, "2") && (response.Value == nil || !hasHeader) {
					t.Errorf("%s %s success response %s is missing Idempotent-Replay", method, path, status)
				}
			}
		}
	}
}

func assertOperationStatusMatrix(t *testing.T, doc *openapi3.T, seen map[string]map[int]bool) {
	t.Helper()
	for path, item := range doc.Paths.Map() {
		for method, operation := range item.Operations() {
			for statusText, response := range operation.Responses.Map() {
				status, err := strconv.Atoi(statusText)
				if err != nil {
					t.Errorf("%s %s has non-numeric response status %q", method, path, statusText)
					continue
				}
				if seen[operation.OperationID][status] {
					continue
				}
				_ = response
				t.Errorf("%s %s (%s) documented status %d was not exercised", method, path, operation.OperationID, status)
			}
		}
	}
}

func assertRecallResponseContract(t *testing.T, doc *openapi3.T) {
	t.Helper()
	schema := doc.Components.Schemas["RecallResponse"].Value
	if !slicesContains(schema.Required, "context") || !slicesContains(schema.Required, "execution") {
		t.Error("RecallResponse must require context and execution")
	}
	execution := doc.Components.Schemas["RecallExecutionMetadata"].Value
	capabilities := execution.Properties["requested_capabilities"].Value.Items.Value
	for _, expected := range []string{"keyword", "semantic", "graph"} {
		found := false
		for _, candidate := range capabilities.Enum {
			found = found || candidate == expected
		}
		if !found {
			t.Errorf("RecallExecutionMetadata.requested_capabilities enum is missing %q", expected)
		}
	}
	for _, property := range []string{"operation_id", "intent", "reference_time", "requested_capabilities", "executed_capabilities", "degraded", "degradation_reasons", "query_plan_identity", "ranking_identity", "context_selection_identity"} {
		if !slicesContains(execution.Required, property) {
			t.Errorf("RecallExecutionMetadata is missing required property %q", property)
		}
	}
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (c *contractClient) do(method, path, key string, body any) *http.Response {
	return c.doWithReplay(method, path, key, body, "false")
}

func (c *contractClient) doWithReplay(method, path, key string, body any, expectedReplay string) *http.Response {
	c.t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("X-Caller-ID", "contract-test")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	route, params, err := c.router.FindRoute(request)
	if err != nil {
		c.t.Fatalf("route %s %s: %v", method, path, err)
	}
	input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	if err := openapi3filter.ValidateRequest(validationContext(), input); err != nil {
		c.t.Fatalf("request %s %s violates OpenAPI: %v", method, path, err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	bodyBytes, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		c.t.Fatal(err)
	}
	response.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	validation := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: response.StatusCode, Header: response.Header}
	validation.SetBodyBytes(bodyBytes)
	if err := openapi3filter.ValidateResponse(validationContext(), validation); err != nil {
		c.t.Fatalf("response %s %s (%d) violates OpenAPI: %v\n%s", method, path, response.StatusCode, err, bodyBytes)
	}
	c.record(route.Operation.OperationID, response.StatusCode)
	if key != "" && response.StatusCode >= 200 && response.StatusCode < 300 {
		replay := response.Header.Get("Idempotent-Replay")
		if expectedReplay != "" && replay != expectedReplay || expectedReplay == "" && replay != "false" && replay != "true" {
			c.t.Fatalf("%s %s Idempotent-Replay = %q", method, path, replay)
		}
	}
	return response
}

func (c *contractClient) expect(status int, method, path, key string, body any) *http.Response {
	c.t.Helper()
	response := c.do(method, path, key, body)
	if response.StatusCode != status {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		c.t.Fatalf("%s %s status = %d, want %d: %s", method, path, response.StatusCode, status, data)
	}
	return response
}

func (c *contractClient) record(operationID string, status int) {
	if c.seen[operationID] == nil {
		c.seen[operationID] = map[int]bool{}
	}
	c.seen[operationID][status] = true
}

func (c *contractClient) replay(method, path, key string, body any) {
	c.t.Helper()
	response := c.doWithReplay(method, path, key, body, "true")
	defer response.Body.Close()
	if response.Header.Get("Idempotent-Replay") != "true" {
		c.t.Fatalf("%s %s replay header = %q", method, path, response.Header.Get("Idempotent-Replay"))
	}
}

func (c *contractClient) close(response *http.Response) { response.Body.Close() }

func (c *contractClient) invalid(status int, method, path, key, raw string) {
	c.t.Helper()
	request, err := http.NewRequest(method, "http://localhost"+path, strings.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("X-Caller-ID", "contract-test")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	request.Header.Set("Content-Type", "application/json")
	route, params, err := c.router.FindRoute(request)
	if err != nil {
		c.t.Fatal(err)
	}
	input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	if err := openapi3filter.ValidateRequest(validationContext(), input); err == nil {
		c.t.Fatalf("intentionally invalid request %s %s unexpectedly satisfies OpenAPI", method, path)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		c.t.Fatal(err)
	}
	if response.StatusCode != status {
		c.t.Fatalf("invalid request %s %s status = %d, want %d: %s", method, path, response.StatusCode, status, body)
	}
	validation := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: response.StatusCode, Header: response.Header}
	validation.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(validationContext(), validation); err != nil {
		c.t.Fatalf("invalid request response violates OpenAPI: %v\n%s", err, body)
	}
	c.record(route.Operation.OperationID, response.StatusCode)
}

func (c *contractClient) invalidCaller(method, path string) {
	c.t.Helper()
	request, err := http.NewRequest(method, "http://localhost"+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	route, params, err := c.router.FindRoute(request)
	if err != nil {
		c.t.Fatal(err)
	}
	input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	if err := openapi3filter.ValidateRequest(validationContext(), input); err == nil {
		c.t.Fatalf("request without X-Caller-ID unexpectedly satisfies OpenAPI")
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		c.t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnprocessableEntity {
		c.t.Fatalf("request without X-Caller-ID status = %d: %s", response.StatusCode, body)
	}
	validation := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: response.StatusCode, Header: response.Header}
	validation.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(validationContext(), validation); err != nil {
		c.t.Fatalf("request without X-Caller-ID response violates OpenAPI: %v\n%s", err, body)
	}
	c.record(route.Operation.OperationID, response.StatusCode)
}

func withField(body map[string]any, key string, value any) map[string]any {
	result := make(map[string]any, len(body)+1)
	for name, field := range body {
		result[name] = field
	}
	result[key] = value
	return result
}

func validationContext() context.Context {
	return openapi3.WithValidationOptions(context.Background(), openapi3.EnableSchemaFormatValidation())
}
