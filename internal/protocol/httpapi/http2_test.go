package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandlerServesOverHTTP2 proves the application adapter is transport
// agnostic: it never inspects the HTTP protocol version and works unchanged
// over an ALPN-negotiated HTTP/2 connection.
func TestHandlerServesOverHTTP2(t *testing.T) {
	handler, _, _ := testHandler(t)
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("protocol = %s, want HTTP/2", response.Proto)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
