package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/config"
)

func TestConfigurationAPIStatusReloadAndConflict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime")}
	writeReloadConfig(t, path, initial)
	manager := config.NewManager(path, nil)
	d, err := StartManaged(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(d.endpoint)

	status := configAPIRequest(t, client, http.MethodGet, "/v1/config", nil, "")
	if status.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", status.StatusCode)
	}
	var active struct {
		ActiveEffectiveIdentity string `json:"active_effective_identity"`
		Capabilities            struct {
			Processing struct {
				State                   string `json:"state"`
				ConfigurationGeneration int    `json:"configuration_generation"`
				PolicyGeneration        int    `json:"policy_generation"`
				BlockedWorkCount        int    `json:"blocked_work_count"`
				ReconciliationState     string `json:"reconciliation_state"`
			} `json:"processing"`
		} `json:"capabilities"`
	}
	if err := json.NewDecoder(status.Body).Decode(&active); err != nil {
		t.Fatal(err)
	}
	status.Body.Close()
	if active.ActiveEffectiveIdentity == "" {
		t.Fatal("status omitted active identity")
	}
	processing := active.Capabilities.Processing
	if processing.State != "disabled" || processing.ConfigurationGeneration != 1 || processing.PolicyGeneration != 1 || processing.BlockedWorkCount != 0 || processing.ReconciliationState != "idle" {
		t.Fatalf("processing status = %+v", processing)
	}

	updated := initial
	updated.processing = true
	writeReloadConfig(t, path, updated)
	reload := configAPIRequest(t, client, http.MethodPost, "/v1/config/reload", map[string]any{}, "reload-1")
	if reload.StatusCode != http.StatusOK {
		t.Fatalf("reload code = %d", reload.StatusCode)
	}
	reload.Body.Close()
	replay := configAPIRequest(t, client, http.MethodPost, "/v1/config/reload", map[string]any{}, "reload-1")
	if replay.StatusCode != http.StatusOK || replay.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay = %d, header %q", replay.StatusCode, replay.Header.Get("Idempotent-Replay"))
	}
	replay.Body.Close()

	idempotencyConflict := configAPIRequest(t, client, http.MethodPost, "/v1/config/reload", map[string]any{"expected_effective_identity": active.ActiveEffectiveIdentity}, "reload-1")
	if idempotencyConflict.StatusCode != http.StatusConflict {
		t.Fatalf("idempotency conflict code = %d", idempotencyConflict.StatusCode)
	}
	idempotencyConflict.Body.Close()

	conflict := configAPIRequest(t, client, http.MethodPost, "/v1/config/reload", map[string]any{"expected_effective_identity": active.ActiveEffectiveIdentity}, "reload-2")
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict code = %d", conflict.StatusCode)
	}
	conflict.Body.Close()

	rejected := configAPIRequest(t, client, http.MethodPost, "/v1/config/reload", map[string]any{"unknown": true}, "reload-3")
	if rejected.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("strict request code = %d", rejected.StatusCode)
	}
	rejected.Body.Close()
}

func configAPIRequest(t *testing.T, client *http.Client, method, path string, body any, key string) *http.Response {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Caller-ID", "config-api-test")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRejectedReloadPersistsAuditableActivationRow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{dataDir: filepath.Join(root, "data"), runtimeDir: filepath.Join(root, "runtime")}
	writeReloadConfig(t, path, initial)
	t.Setenv("DMEM_RELOAD_PROCESSING_SECRET", "processing-secret")

	manager := config.NewManager(path, nil)
	d, err := StartManaged(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(d.endpoint)

	db := openDaemonDB(t, d)
	var initialCount int
	if err := db.QueryRow(`SELECT count(*) FROM configuration_activations`).Scan(&initialCount); err != nil {
		t.Fatal(err)
	}

	invalid := initial.TOML() + "\n[unknown_section]\nfoo = 1\n"
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	rejected := configAPIRequest(t, client, http.MethodPost, "/v1/config/reload", map[string]any{}, "reject-audit-key")
	if rejected.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("rejected reload code = %d; want 422", rejected.StatusCode)
	}
	rejected.Body.Close()

	var finalCount int
	if err := db.QueryRow(`SELECT count(*) FROM configuration_activations`).Scan(&finalCount); err != nil {
		t.Fatal(err)
	}
	if finalCount != initialCount+1 {
		t.Fatalf("activation count = %d -> %d; want %d+1", initialCount, finalCount, initialCount)
	}

	var outcome, errorCode string
	var diagnostics []byte
	if err := db.QueryRow(`SELECT outcome, error_code, diagnostics FROM configuration_activations ORDER BY started_at DESC LIMIT 1`).Scan(&outcome, &errorCode, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if outcome != "rejected" || errorCode == "" {
		t.Fatalf("activation = outcome %q, error_code %q; want rejected with non-empty code", outcome, errorCode)
	}
	if len(diagnostics) == 0 {
		t.Fatalf("diagnostics should be populated for rejected activation: %q", string(diagnostics))
	}
	if strings.Contains(string(diagnostics), "processing-secret") || strings.Contains(string(diagnostics), "provider.invalid") {
		t.Fatalf("diagnostics leaked secret or endpoint: %s", string(diagnostics))
	}
}

func TestEnabledProviderConfigStatusExcludesSecretsEndpointAndQueryInstructions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	initial := reloadConfig{
		dataDir:                 filepath.Join(root, "data"),
		runtimeDir:              filepath.Join(root, "runtime"),
		embedding:               true,
		embeddingModel:          "embedding-redact-test",
		embeddingAllowSpaceType: "user",
	}
	writeReloadConfig(t, path, initial)
	t.Setenv("DMEM_RELOAD_EMBEDDING_SECRET", "embedding-secret-value")

	d, err := StartManaged(context.Background(), config.NewManager(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	client := unixClient(d.endpoint)

	status := configAPIRequest(t, client, http.MethodGet, "/v1/config", nil, "")
	if status.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", status.StatusCode)
	}
	body, err := io.ReadAll(status.Body)
	status.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, forbidden := range []string{
		"DMEM_RELOAD_EMBEDDING_SECRET",
		"https://provider.invalid/v1/embeddings",
		"query: ",
		"query_instructions",
		"endpoint",
		"embedding-secret-value",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config status leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"embedding"`) || !strings.Contains(text, `"enabled":true`) {
		t.Fatalf("config status should include enabled embedding capability: %s", text)
	}

	db := openDaemonDB(t, d)
	var redactedDocument string
	if err := db.QueryRow(`SELECT redacted_document FROM configuration_revisions ORDER BY loaded_at DESC LIMIT 1`).Scan(&redactedDocument); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"DMEM_RELOAD_EMBEDDING_SECRET",
		"https://provider.invalid/v1/embeddings",
		"query: ",
		"embedding-secret-value",
	} {
		if strings.Contains(redactedDocument, forbidden) {
			t.Fatalf("persisted redacted_document leaked %q: %s", forbidden, redactedDocument)
		}
	}
}
