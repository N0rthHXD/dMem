package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
)

func TestBoundaryLogsAccessAndError(t *testing.T) {
	var buffer bytes.Buffer
	handler, err := New(Dependencies{AgentTools: &fakeTools{}, Scopes: &fakeScopes{}, Events: &fakeEvents{}, Reviews: &fakeReviews{}},
		WithLogger(slog.New(slog.NewJSONHandler(&buffer, nil))))
	if err != nil {
		t.Fatal(err)
	}

	response := request(t, handler, http.MethodGet, "/v1/capabilities", "", map[string]string{correlationHeader: "corr-1"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	access := decodeLogRecord(t, &buffer)
	for key, want := range map[string]any{
		"component":      "http",
		"correlation_id": "corr-1",
		"operation":      string(OperationCapabilities),
		"method":         http.MethodGet,
		"route":          "/v1/capabilities",
		"status":         float64(http.StatusOK),
		"error_code":     "",
	} {
		if got := access[key]; got != want {
			t.Errorf("access log %s = %#v, want %#v", key, got, want)
		}
	}
	if _, ok := access["request_id"].(string); !ok {
		t.Errorf("access log request_id missing")
	}
	if _, ok := access["duration_ms"].(float64); !ok {
		t.Errorf("access log duration_ms missing")
	}

	response = request(t, handler, http.MethodGet, "/v1/missing", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	failure := decodeLogRecord(t, &buffer)
	for key, want := range map[string]any{
		"component":  "http",
		"operation":  "",
		"route":      "",
		"status":     float64(http.StatusNotFound),
		"error_code": codeNotFound,
	} {
		if got := failure[key]; got != want {
			t.Errorf("error log %s = %#v, want %#v", key, got, want)
		}
	}
}

func decodeLogRecord(t *testing.T, buffer *bytes.Buffer) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	buffer.Reset()
	return record
}
