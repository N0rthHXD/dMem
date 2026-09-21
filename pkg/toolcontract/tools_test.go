package toolcontract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestV1ToolDefinitionsMatchCapabilities(t *testing.T) {
	definitions, err := V1ToolDefinitions(ToolDefinitionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	if !reflect.DeepEqual(names, V1Capabilities().Tools) {
		t.Fatalf("tool names = %v", names)
	}
	for _, definition := range definitions {
		if definition.Title == "" || definition.Description == "" {
			t.Errorf("%s is missing display metadata", definition.Name)
		}
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			t.Fatalf("%s input schema: %v", definition.Name, err)
		}
		if schema.Type != "object" {
			t.Errorf("%s input schema type = %q", definition.Name, schema.Type)
		}
		if len(definition.OutputSchema) == 0 {
			t.Errorf("%s has no output schema", definition.Name)
		}
	}
}

func TestHostDefaultsOnlyRelaxScopeAndSource(t *testing.T) {
	plain, err := V1ToolDefinitions(ToolDefinitionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hosted, err := V1ToolDefinitions(ToolDefinitionOptions{DefaultScope: true, HostSource: true})
	if err != nil {
		t.Fatal(err)
	}
	plainByName := indexDefinitions(t, plain)
	hostedByName := indexDefinitions(t, hosted)

	for _, name := range []string{ToolRecall, ToolRemember, ToolFeedback, ToolForget} {
		if got := requiredFieldsOf(t, plainByName[name].InputSchema, "scope_ids", "scope_id"); got == "" {
			t.Errorf("%s contract schema does not require a scope field", name)
		}
		if got := requiredFieldsOf(t, hostedByName[name].InputSchema, "scope_ids", "scope_id"); got != "" {
			t.Errorf("%s host schema still requires %s", name, got)
		}
	}
	if !rawRequired(t, plainByName[ToolRemember].InputSchema)["source"] {
		t.Fatal("remember contract schema does not require source")
	}
	properties := propertiesOf(t, hostedByName[ToolRemember].InputSchema)
	if _, present := properties["source"]; present {
		t.Fatal("host remember schema still exposes source")
	}
	if _, present := properties["memories"]; !present {
		t.Fatal("host remember schema lost its domain fields")
	}
	// The canonical contract schema is unchanged: the derivation never mutates
	// the embedded source document.
	canonical, err := Schema(SchemaMemoryCommitInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"source"`) {
		t.Fatal("canonical memory commit schema no longer mentions source")
	}
}

func TestV1ToolDefinitionsAreDeterministic(t *testing.T) {
	first, err := V1ToolDefinitions(ToolDefinitionOptions{DefaultScope: true, HostSource: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := V1ToolDefinitions(ToolDefinitionOptions{DefaultScope: true, HostSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("tool definitions are not deterministic")
	}
}

func TestHostRememberSchemaValidatesWithInjectedIdentity(t *testing.T) {
	// The host injects scope and source before the canonical contract decode, so
	// the strict canonical schema must accept the resolved document.
	resolved := []byte(`{"scope_id":"scope-1","source":{"type":"agent_tool","name":"assistant","conversation_id":"c","turn_id":"t","occurred_at":"2026-09-20T10:00:00Z"},"memories":[{"local_id":"m","kind":"fact","content":"c","sensitivity":"normal","confidence":0.5}]}`)
	var input MemoryCommitInput
	if err := DecodeStrict(SchemaMemoryCommitInput, resolved, &input); err != nil {
		t.Fatalf("resolved command rejected: %v", err)
	}
	if input.ScopeID != "scope-1" || input.Source.Name != "assistant" {
		t.Fatalf("decoded input = %+v", input)
	}
}

func TestMemoryPolicyPromptModesAndValidation(t *testing.T) {
	opportunistic := MemoryPolicyPrompt(IntegrationOpportunistic)
	checkpoint := MemoryPolicyPrompt(IntegrationCheckpoint)
	if opportunistic == checkpoint {
		t.Fatal("integration modes share one prompt")
	}
	for name, prompt := range map[string]string{"opportunistic": opportunistic, "checkpoint": checkpoint} {
		for _, tool := range []string{ToolRecall, ToolRemember, ToolFeedback, ToolForget} {
			if !strings.Contains(prompt, tool) {
				t.Errorf("%s prompt is missing guidance for %s", name, tool)
			}
		}
	}
	if !strings.Contains(checkpoint, "end of every turn") {
		t.Fatal("checkpoint prompt does not describe the explicit turn decision")
	}
	if strings.Contains(opportunistic, "end of every turn") {
		t.Fatal("opportunistic prompt leaks checkpoint behaviour")
	}
	for _, mode := range IntegrationModes() {
		if _, err := ParseIntegrationMode(string(mode)); err != nil {
			t.Fatalf("ParseIntegrationMode(%q) = %v", mode, err)
		}
	}
	if _, err := ParseIntegrationMode("other"); err == nil {
		t.Fatal("ParseIntegrationMode accepted an unknown mode")
	}
}

func indexDefinitions(t *testing.T, definitions []ToolDefinition) map[string]ToolDefinition {
	t.Helper()
	index := make(map[string]ToolDefinition, len(definitions))
	for _, definition := range definitions {
		index[definition.Name] = definition
	}
	return index
}

func requiredFieldsOf(t *testing.T, schema json.RawMessage, names ...string) string {
	t.Helper()
	required := rawRequired(t, schema)
	for _, name := range names {
		if required[name] {
			return name
		}
	}
	return ""
}

func rawRequired(t *testing.T, schema json.RawMessage) map[string]bool {
	t.Helper()
	var document struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &document); err != nil {
		t.Fatalf("schema: %v", err)
	}
	required := map[string]bool{}
	for _, name := range document.Required {
		required[name] = true
	}
	return required
}

func propertiesOf(t *testing.T, schema json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var document struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &document); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return document.Properties
}
