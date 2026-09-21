package toolcontract

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

var examples = map[string]string{
	SchemaCapabilities:       `{"contract_version":"dmem-agent-tool-v1","tools":["dmem_recall","dmem_remember","dmem_feedback","dmem_forget"],"predicate_registry_version":"predicate-registry-v2","evidence_disclosure":"references_only"}`,
	SchemaError:              `{"error":{"code":"invalid_input","message":"invalid command","request_id":"req-1","retryable":false}}`,
	SchemaRecallInput:        `{"query":"database choice","scope_ids":["project-1"],"intent":"lookup","limit":10}`,
	SchemaRecallOutput:       `{"operation_id":"recall-1","memories":[{"memory_id":"mem-1","revision_id":"rev-1","kind":"preference","content":"PostgreSQL is preferred.","confidence":0.9,"evidence":[]}],"degraded_capabilities":["semantic"],"next_cursor":null}`,
	SchemaMemoryCommitInput:  `{"scope_id":"project-1","source":{"type":"agent_tool","name":"assistant","conversation_id":"conv-1","turn_id":"turn-1","occurred_at":"2026-09-20T10:00:00Z"},"memories":[{"local_id":"memory-1","kind":"preference","content":"PostgreSQL is preferred.","sensitivity":"normal","confidence":0.9,"search_hints":["database choice"]}]}`,
	SchemaMemoryCommitOutput: `{"operation_id":"op-1","replayed":false,"source_event_id":"event-1","memories":[{"local_id":"memory-1","status":"authority","memory_id":"mem-1","revision_id":"rev-1"}],"entities":[],"facts":[],"relations":[]}`,
	SchemaFeedbackInput:      `{"scope_id":"project-1","memory_id":"mem-1","revision_id":"rev-1","kind":"used"}`,
	SchemaFeedbackOutput:     `{"operation_id":"op-2","replayed":false,"feedback_id":"feedback-1","memory_id":"mem-1","revision_id":"rev-1","kind":"used"}`,
	SchemaForgetInput:        `{"scope_id":"project-1","memory_id":"mem-1","expected_revision_id":"rev-1","reason":"user requested deletion"}`,
	SchemaForgetOutput:       `{"operation_id":"op-3","replayed":false,"memory_id":"mem-1","revision_id":"rev-1","state":"deleted"}`,
	SchemaScopeEnsureInput:   `{"parent_scope_id":null,"scope_type":"project","external_id":"ext","display_name":"Project"}`,
	SchemaScopeEnsureOutput:  `{"operation_id":"op-4","replayed":false,"scope":{"scope_id":"scope-1","parent_scope_id":null,"scope_type":"project","external_id":"ext","display_name":"Project","created_at":"2026-09-20T10:00:00Z"}}`,
	SchemaScopeOutput:        `{"scope":{"scope_id":"scope-1","parent_scope_id":null,"scope_type":"project","external_id":"ext","display_name":"Project","created_at":"2026-09-20T10:00:00Z"}}`,
}

func TestSchemasCompileAndValidateGoldenExamples(t *testing.T) {
	for _, name := range SchemaNames() {
		document, ok := examples[name]
		if !ok {
			t.Fatalf("missing golden example for %s", name)
		}
		if err := Validate(name, []byte(document)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestSchemasRejectUnknownFieldsAndBoundaries(t *testing.T) {
	tests := []struct{ name, schema, document string }{
		{"unknown", SchemaRecallInput, `{"query":"x","scope_ids":["s"],"channels":["semantic"]}`},
		{"limit", SchemaRecallInput, `{"query":"x","scope_ids":["s"],"limit":101}`},
		{"feedback enum", SchemaFeedbackInput, `{"scope_id":"s","memory_id":"m","revision_id":"r","kind":"irrelevant"}`},
		{"forbidden authority", SchemaMemoryCommitInput, `{"scope_id":"s","source":{"type":"agent_tool","name":"a","conversation_id":"c","turn_id":"t","occurred_at":"2026-09-20T10:00:00Z"},"memories":[{"local_id":"m","kind":"fact","content":"x","sensitivity":"normal","confidence":1,"confirmed":true}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.schema, []byte(test.document)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestRememberOutputAcceptsEveryClosedOutcome(t *testing.T) {
	for _, status := range []string{"authority", "deduplicated", "proposal", "rejected"} {
		document := `{"operation_id":"op-1","replayed":false,"source_event_id":"event-1","memories":[{"local_id":"memory-1","status":"` + status + `"}],"entities":[],"facts":[],"relations":[]}`
		if err := Validate(SchemaMemoryCommitOutput, []byte(document)); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
}

func TestStrictDecodeRejectsTrailingJSON(t *testing.T) {
	var input RecallInput
	if err := DecodeStrict(SchemaRecallInput, []byte(examples[SchemaRecallInput]+` {}`), &input); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
	if err := DecodeStrict(SchemaRecallInput, []byte(examples[SchemaRecallInput]), &input); err != nil {
		t.Fatal(err)
	}
}

func TestStrictDecodeEnforcesDocumentLimit(t *testing.T) {
	var input RecallInput
	oversized := append([]byte(`{"query":"x","scope_ids":["s"],"cursor":"`), make([]byte, MaxRecallDocument)...)
	oversized = append(oversized, []byte(`"}`)...)
	if err := DecodeStrict(SchemaRecallInput, oversized, &input); err == nil {
		t.Fatal("expected document size rejection")
	}
}

func TestRememberPredicateEnumMatchesRegistry(t *testing.T) {
	data, err := Schema(SchemaMemoryCommitInput)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	defs := document["$defs"].(map[string]any)
	fact := defs["fact"].(map[string]any)
	properties := fact["properties"].(map[string]any)
	values := properties["predicate"].(map[string]any)["enum"].([]any)
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = value.(string)
	}
	want := domain.PredicateNames()
	sort.Strings(names)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("schema predicate enum drifted from registry:\n got %v\nwant %v", names, want)
	}
}

func TestAllObjectSchemasAreStrict(t *testing.T) {
	for _, name := range SchemaNames() {
		data, err := Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		walkObjects(t, name, value)
	}
}

func walkObjects(t *testing.T, name string, value any) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if object["type"] == "object" && object["x-dmem-open-map"] != true {
		if strict, exists := object["additionalProperties"]; !exists || strict != false {
			t.Errorf("%s has a non-strict object schema", name)
		}
	}
	for key, child := range object {
		if strings.HasPrefix(key, "$comment") {
			continue
		}
		switch typed := child.(type) {
		case map[string]any:
			walkObjects(t, name, typed)
		case []any:
			for _, item := range typed {
				walkObjects(t, name, item)
			}
		}
	}
}
