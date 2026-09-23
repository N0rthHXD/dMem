package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/N0rthHXD/dMem/internal/config"
)

func TestRuntimeLogsReloadAndDegradationEvents(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	path := filepath.Join(root, "config.toml")
	writeManagedConfig(t, path, dataDir, false)
	t.Setenv("DMEM_RUNTIME_PROCESSING_SECRET", "processing-secret")

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	d, err := StartManaged(context.Background(), config.NewManager(path, nil), WithLogger(logger))
	if err != nil {
		t.Fatalf("StartManaged() error = %v", err)
	}
	defer d.Close(context.Background())

	startup := collectLogRecords(t, &buffer)
	thresholds := findLogRecord(startup, "disk free thresholds configured")
	if thresholds == nil {
		t.Fatal("startup did not log disk thresholds")
	}
	if thresholds["soft_free_bytes"] != float64(1073741824) || thresholds["hard_free_bytes"] != float64(268435456) {
		t.Fatalf("disk threshold log = %+v", thresholds)
	}
	if findLogRecord(startup, "processing provider disabled") == nil {
		t.Fatal("startup did not log disabled processing provider")
	}
	if findLogRecord(startup, "embedding provider disabled, semantic retrieval degraded") == nil {
		t.Fatal("startup did not log degraded semantic retrieval")
	}

	writeManagedConfig(t, path, dataDir, true)
	result, err := d.Reload(context.Background(), config.TriggerAPI)
	if err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("processing reload = %+v, %v", result, err)
	}
	applied := collectLogRecords(t, &buffer)
	record := findLogRecord(applied, "configuration reload applied")
	if record == nil {
		t.Fatal("reload did not log success")
	}
	changed, ok := record["changed_capabilities"].([]any)
	if !ok || len(changed) != 1 || changed[0] != "processing" {
		t.Fatalf("reload log changed_capabilities = %+v", record["changed_capabilities"])
	}
	if record["trigger"] != string(config.TriggerAPI) {
		t.Fatalf("reload log trigger = %v", record["trigger"])
	}
	if findLogRecord(applied, "index recovery result") == nil {
		t.Fatal("reload did not log index recovery result")
	}

	writeManagedConfig(t, path, dataDir, false)
	if result, err := d.Reload(context.Background(), config.TriggerSIGHUP); err != nil || result.Outcome != config.OutcomeActivated {
		t.Fatalf("disable reload = %+v, %v", result, err)
	}
	degraded := collectLogRecords(t, &buffer)
	for _, message := range []string{
		"processing provider disabled",
		"embedding provider disabled, semantic retrieval degraded",
		"graph extraction disabled, graph retrieval degraded",
	} {
		if findLogRecord(degraded, message) == nil {
			t.Errorf("disable reload did not log %q", message)
		}
	}
}

func collectLogRecords(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buffer.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		records = append(records, record)
	}
	buffer.Reset()
	return records
}

func findLogRecord(records []map[string]any, message string) map[string]any {
	for _, record := range records {
		if record["msg"] == message {
			return record
		}
	}
	return nil
}
