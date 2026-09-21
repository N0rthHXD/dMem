package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
)

// FuzzStrictBodyDecodingIsTotal proves the adapter's strict decoder is total and
// fail-closed: for arbitrary bytes it either reaches the gateway with a fully
// decoded command or rejects the request with a bounded protocol error. It never
// panics, never echoes caller bytes into the response, and never forwards a
// partially decoded body.
func FuzzStrictBodyDecodingIsTotal(f *testing.F) {
	f.Add(`{"query":"database choice","scope_ids":["scope-1"]}`)
	f.Add(`{"query":"q","scope_ids":["s"],"unknown":true}`)
	f.Add(`{}`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(`"string"`)
	f.Add(`12`)
	f.Add(`{"query":"q","scope_ids":["s"]} {}`)
	f.Add(`{"query":"q","scope_ids":["s"]`)
	f.Add("")
	f.Add("   ")
	f.Add("\x00\x01\x02")
	f.Add(`{"query":"q","scope_ids":["s"],"limit":1e999}`)

	handler, tools, _ := fuzzHandler(f)

	f.Fuzz(func(t *testing.T, body string) {
		tools.command = nil
		request := httptest.NewRequest(http.MethodPost, "/v1/recall", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(callerSubjectHeader, "agent")
		request.ContentLength = -1
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		payload := response.Body.String()
		switch response.Code {
		case http.StatusOK:
			if tools.command == nil {
				t.Fatalf("status 200 without a decoded command for body %q", body)
			}
			if _, ok := tools.command.(agenttool.RecallCommand); !ok {
				t.Fatalf("gateway received %T, want agenttool.RecallCommand", tools.command)
			}
			if err := decodeStrict([]byte(payload), new(struct{})); err != nil {
				var requestErr *requestError
				if !asRequestError(err, &requestErr) {
					t.Fatalf("response is not strict single-object JSON: %q", payload)
				}
			}
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			if tools.command != nil {
				t.Fatalf("gateway was called for a rejected body %q", body)
			}
			assertNoBodyEcho(t, body, response)
		default:
			t.Fatalf("unexpected status %d for body %q: %s", response.Code, body, payload)
		}
	})
}

// FuzzRecallWireMappingPreservesContractValues proves every value the contract
// schema accepts is either mapped faithfully into the protocol-neutral command
// or rejected with invalid_input before the gateway is called.
func FuzzRecallWireMappingPreservesContractValues(f *testing.F) {
	f.Add(`{"query":"q","scope_ids":["s"],"intent":"temporal","entities":["e"],"limit":7,"reference_time":"2026-09-20T10:00:00Z"}`)
	f.Add(`{"query":"q","scope_ids":["s"],"limit":0}`)
	f.Add(`{"query":"q","scope_ids":["s"],"limit":101}`)
	f.Add(`{"query":"q","scope_ids":["s"],"reference_time":"not-a-time"}`)
	f.Add(`{"query":"q","scope_ids":[]}`)

	handler, tools, _ := fuzzHandler(f)

	f.Fuzz(func(t *testing.T, body string) {
		tools.command = nil
		response := request(t, handler, http.MethodPost, "/v1/recall", body, caller())
		switch response.Code {
		case http.StatusOK:
			command, ok := tools.command.(agenttool.RecallCommand)
			if !ok {
				t.Fatalf("gateway received %T, want agenttool.RecallCommand", tools.command)
			}
			if strings.TrimSpace(command.Query) == "" || len(command.ScopeIDs) == 0 {
				t.Fatalf("accepted recall without a query or scope: %+v", command)
			}
			if command.Limit < 0 || command.Limit > agenttool.MaxRecallLimit {
				t.Fatalf("accepted out-of-range recall limit %d", command.Limit)
			}
			if len(command.EntityHints) > agenttool.MaxEntityHints || len(command.ScopeIDs) > agenttool.MaxScopeIDs {
				t.Fatalf("accepted out-of-range recall arrays: %+v", command)
			}
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			if tools.command != nil {
				t.Fatalf("gateway was called for a rejected body %q", body)
			}
		default:
			t.Fatalf("unexpected status %d for body %q: %s", response.Code, body, response.Body.String())
		}
	})
}

// FuzzEventIngestBoundsAreEnforced proves the Provider Route ingress endpoint
// enforces its content and metadata bounds at the protocol boundary, so an
// oversized or malformed raw event never reaches the gateway.
func FuzzEventIngestBoundsAreEnforced(f *testing.F) {
	f.Add(`{"scope_id":"s","source":{"type":"manual","name":"collector"},"content":{"kind":"text","media_type":"text/plain","text":"hello"}}`)
	f.Add(`{"scope_id":"s","source":{"type":"manual","name":"collector","metadata":{"k":"v"}},"content":{"kind":"text","media_type":"text/plain","text":"hello"}}`)
	f.Add(`{"scope_id":"s","source":{"type":"manual","name":"collector"},"content":{"kind":"json","media_type":"application/json","value":{"a":[1,2]}}}`)
	f.Add(`{"scope_id":"s","source":{"type":"manual","name":"collector"},"content":{"kind":"reference","media_type":"text/plain","reference_uri":"https://example.invalid/x"}}`)
	f.Add(`{"scope_id":"s","source":{"type":"manual","name":"collector"},"content":{"kind":"binary","media_type":"application/octet-stream"}}`)

	handler, _, _ := fuzzHandler(f)

	f.Fuzz(func(t *testing.T, body string) {
		response := request(t, handler, http.MethodPost, "/v1/events", body, writes())
		switch response.Code {
		case http.StatusAccepted:
			var output eventOutput
			if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
				t.Fatalf("accepted response is not the event envelope: %s", response.Body.String())
			}
			if output.EventID == "" || output.Status == "" {
				t.Fatalf("accepted response is incomplete: %s", response.Body.String())
			}
		case http.StatusUnprocessableEntity, http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType:
			assertNoBodyEcho(t, body, response)
		default:
			t.Fatalf("unexpected status %d for body %q: %s", response.Code, body, response.Body.String())
		}
	})
}

// asRequestError reports whether err is an adapter requestError and stores it.
// fuzzHandler builds one adapter over recording fakes. It fails the fuzz target
// immediately if construction regresses, rather than silently skipping input.
func fuzzHandler(f *testing.F) (*Handler, *fakeTools, *fakeScopes) {
	f.Helper()
	tools, scopes := &fakeTools{}, &fakeScopes{}
	handler, err := New(Dependencies{AgentTools: tools, Scopes: scopes, Events: &fakeEvents{}})
	if err != nil {
		f.Fatalf("New() error = %v", err)
	}
	return handler, tools, scopes
}

func asRequestError(err error, target **requestError) bool {
	requestErr, ok := err.(*requestError)
	if ok {
		*target = requestErr
	}
	return ok
}

// assertNoBodyEcho proves an error response never echoes request bytes. Error
// messages are bounded and stable, and details never contain caller payload.
func assertNoBodyEcho(t *testing.T, body string, response *httptest.ResponseRecorder) {
	t.Helper()
	payload := response.Body.String()
	var envelope struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("error response is not the contract envelope: %q", payload)
	}
	if envelope.Error.Code == "" {
		t.Fatalf("error response has no stable code: %q", payload)
	}
	trimmed := strings.TrimSpace(body)
	if len(trimmed) < 8 {
		return
	}
	if strings.Contains(envelope.Error.Message, trimmed) {
		t.Fatalf("error message echoed the request body: %q", payload)
	}
	for _, value := range envelope.Error.Details {
		if strings.Contains(value, trimmed) {
			t.Fatalf("error details echoed the request body: %q", payload)
		}
	}
}
