package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/provider"
)

func embeddingErrorClient(t *testing.T, server *httptest.Server) *provider.Client {
	t.Helper()
	t.Setenv("DMEM_TEST_EMBED_ERROR_SECRET", "secret")
	client, err := provider.New(provider.Config{
		Enabled: true, Kind: provider.KindOpenAICompatible, Endpoint: server.URL, Model: "model-a",
		Secret: provider.SecretReference{Source: "environment", Name: "DMEM_TEST_EMBED_ERROR_SECRET"}, TelemetryDisabled: true,
		Options: provider.EmbeddingOptions{Dimensions: 2},
		Limits: provider.Limits{MaxEgressInputChars: 1000, MaxResponseBytes: 4096,
			RequestTimeout: time.Second, MaxConcurrent: 1, RequestsPerMinute: 60000},
		Policy: provider.Policy{Default: "deny", ContentTypes: []string{"text/plain"}, SourceTypes: []string{"manual"}, Sensitivities: []string{"normal"},
			Assignments: []provider.PolicyAssignment{{Selector: "space", SpaceID: "space-1", Effect: "allow"}}},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestEmbedMemoryClassifiesProviderFailures proves permanent provider failures
// and malformed model output are not reported as retryable transport failures.
func TestEmbedMemoryClassifiesProviderFailures(t *testing.T) {
	for name, test := range map[string]struct {
		handler http.HandlerFunc
		want    error
	}{
		"permanent rejection": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }, ErrProviderPermanent},
		"transient outage":    {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, ErrProviderTransient},
		"invalid model output": {func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[]}`))
		}, ErrProviderInvalidOutput},
		"invalid vector": {func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0,0]}]}`))
		}, ErrProviderInvalidOutput},
	} {
		t.Run(name, func(t *testing.T) {
			repository, _ := newMemoryTestRepository(t)
			memory := createTestMemory(t, repository, "provider-error-"+name)
			server := httptest.NewTLSServer(test.handler)
			defer server.Close()
			client := embeddingErrorClient(t, server)
			repository.ConfigureSemantic(client, client.Identity(), client.ModelIdentity(), 0)
			err := repository.EmbedMemory(context.Background(), memory.CurrentRevision.RevisionID)
			if !errors.Is(err, test.want) {
				t.Fatalf("EmbedMemory() error = %v, want %v", err, test.want)
			}
		})
	}
}
