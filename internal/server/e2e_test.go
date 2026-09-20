package server_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/daemon"
	"github.com/N0rthHXD/dMem/internal/server"
)

// TestEndToEndServesGatewayRoutes drives the assembled runtime over a real TCP
// listener, proving the transport layer and the protocol handler compose.
func TestEndToEndServesGatewayRoutes(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := config.Config{
		ConfigVersion:       1,
		DataDir:             dataDir,
		DatabaseFilename:    "dmem.db",
		DatabasePath:        filepath.Join(dataDir, "dmem.db"),
		ShutdownGracePeriod: 5 * time.Second,
		WorkerCount:         2,
		QueueSoftLimit:      10000,
		QueueHardLimit:      20000,
		MaxAttempts:         3,
		LeaseDuration:       5 * time.Second,
		PerJobTimeout:       10 * time.Second,
		RetryInitialBackoff: 100 * time.Millisecond,
		RetryMaxBackoff:     time.Second,
		DiskSoftFree:        1 << 30,
		DiskHardFree:        1 << 28,
		ProcessingVersion:   config.DefaultProcessingVersion,
		GraphEnabled:        true,
	}
	d, err := daemon.StartStaging(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartStaging() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	srv, err := server.New(d.Handler(), server.Config{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("server.New() error = %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("server.Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	request, err := http.NewRequest(http.MethodPost, "http://"+srv.Addr()+"/v1/scopes", strings.NewReader(`{"scope_type":"user","external_id":"e2e","display_name":"E2E"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Caller-Subject-ID", "e2e")
	request.Header.Set("Idempotency-Key", "e2e-scope")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/scopes error = %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), `"scope_id"`) {
		t.Fatalf("response missing scope_id: %s", body)
	}
}
