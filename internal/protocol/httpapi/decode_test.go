package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/gateway/memory"
)

func TestStrictDecodeRejectsUnknownField(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"scope_type":"project","external_id":"e","display_name":"d","bogus":true}`, writeHeaders())
	assertErrorCode(t, recorder, http.StatusBadRequest, codeInvalidJSON)
}

func TestStrictDecodeRejectsUnknownNestedField(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodPost, "/v1/events",
		`{"scope_id":"s","source_type":"message","source_name":"a","content":{"kind":"text","media_type":"text/plain","text":"x","bogus":1}}`,
		writeHeaders())
	assertErrorCode(t, recorder, http.StatusBadRequest, codeInvalidJSON)
}

func TestStrictDecodeRejectsTrailingValue(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"scope_type":"project","external_id":"e","display_name":"d"} {"second":true}`, writeHeaders())
	assertErrorCode(t, recorder, http.StatusBadRequest, codeInvalidJSON)
}

func TestStrictDecodeRejectsNonObjectRoots(t *testing.T) {
	roots := []string{`[]`, `null`, `"text"`, `42`, `true`}
	for _, body := range roots {
		t.Run(body, func(t *testing.T) {
			handler := newTestHandler(t, newTestGateway())
			recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes", body, writeHeaders())
			assertErrorCode(t, recorder, http.StatusBadRequest, codeInvalidJSON)
		})
	}
}

func TestStrictDecodeRejectsEmptyBody(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	request := newRequest(t, http.MethodPost, "/v1/scopes", "", writeHeaders())
	request.Header.Set("Content-Type", "application/json")
	recorder := serve(handler, request)
	assertErrorCode(t, recorder, http.StatusBadRequest, codeInvalidJSON)
}

func TestStrictDecodeRejectsWrongType(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"scope_type":5,"external_id":"e","display_name":"d"}`, writeHeaders())
	assertErrorCode(t, recorder, http.StatusBadRequest, codeInvalidJSON)
}

func TestStrictDecodeRejectsFractionalInteger(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodPost, "/v1/memories",
		`{"scope_id":"s","content":"c","sequence":1.5}`, writeHeaders())
	assertErrorCode(t, recorder, http.StatusBadRequest, codeInvalidJSON)
}

func TestStrictDecodeRejectsOversizedBodyByContentLength(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	body := `{"display_name":"` + strings.Repeat("a", 70<<10) + `"}`
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes", body, writeHeaders())
	assertErrorCode(t, recorder, http.StatusRequestEntityTooLarge, codeRequestTooLarge)
}

func TestStrictDecodeRejectsOversizedBodyWithUnknownLength(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	body := `{"display_name":"` + strings.Repeat("a", 70<<10) + `"}`
	request := newRequest(t, http.MethodPost, "/v1/scopes", body, writeHeaders())
	request.ContentLength = -1
	recorder := serve(handler, request)
	assertErrorCode(t, recorder, http.StatusRequestEntityTooLarge, codeRequestTooLarge)
}

func TestWriteRequiresJSONContentType(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	headers := writeHeaders()
	headers["Content-Type"] = "text/plain"
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"scope_type":"project","external_id":"e","display_name":"d"}`, headers)
	assertErrorCode(t, recorder, http.StatusUnsupportedMediaType, codeUnsupportedMediaType)
}

func TestWriteRequiresContentTypeHeader(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	headers := writeHeaders()
	headers["Content-Type"] = ""
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"scope_type":"project","external_id":"e","display_name":"d"}`, headers)
	assertErrorCode(t, recorder, http.StatusUnsupportedMediaType, codeUnsupportedMediaType)
}

func TestJSONContentTypeParametersAreAccepted(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	headers := writeHeaders()
	headers["Content-Type"] = "application/json; charset=utf-8"
	recorder := doRequest(t, handler, http.MethodPost, "/v1/scopes",
		`{"scope_type":"project","external_id":"e","display_name":"d"}`, headers)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
}

func TestReadRouteRejectsRequestBody(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	recorder := doRequest(t, handler, http.MethodGet, "/v1/memories/mem-1", `{"unexpected":true}`, callerHeaders())
	assertErrorCode(t, recorder, http.StatusBadRequest, codeUnexpectedBody)
}

func TestNumberPrecisionIsPreservedInMetadata(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	recorder := doRequest(t, handler, http.MethodPost, "/v1/memories",
		`{"scope_id":"s","content":"c","metadata":{"big":9007199254740993,"small":1}}`, writeHeaders())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
	}
	command := lastCommand(t, fake, "memory.remember").command.(memory.RememberCommand)
	if got, want := command.Metadata["big"], json.Number("9007199254740993"); got != want {
		t.Errorf("big number = %#v, want json.Number %q", got, want)
	}
	if got, want := command.Metadata["small"], json.Number("1"); got != want {
		t.Errorf("small number = %#v, want json.Number %q", got, want)
	}
}
