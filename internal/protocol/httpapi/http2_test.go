package httpapi

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// newHTTP2Server starts a test-only HTTP/2 server for the handler. It uses the
// httptest transport, which negotiates h2 with the bundled standard-library
// client; no production listener, TLS configuration, or h2c upgrade is added.
func newHTTP2Server(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestHTTP2ReadCompatibility(t *testing.T) {
	handler := newTestHandler(t, newTestGateway())
	server := newHTTP2Server(t, handler)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/memories/mem-h2", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set(callerSubjectHeader, "agent-1")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if response.ProtoMajor != 2 {
		t.Fatalf("ProtoMajor = %d, want 2", response.ProtoMajor)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if response.Header.Get(requestIDHeader) == "" {
		t.Errorf("missing request id header over HTTP/2")
	}
}

func TestHTTP2WriteCompatibility(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	server := newHTTP2Server(t, handler)

	body := `{"parent_scope_id":null,"scope_type":"project","external_id":"ext","display_name":"Project"}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/scopes", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(callerSubjectHeader, "agent-1")
	request.Header.Set(idempotencyHeader, "key-h2")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if response.ProtoMajor != 2 {
		t.Fatalf("ProtoMajor = %d, want 2", response.ProtoMajor)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.StatusCode)
	}
	if fake.CallCount() != 1 {
		t.Errorf("gateway calls = %d, want 1", fake.CallCount())
	}
}

func TestHTTP2MultiplexedConcurrency(t *testing.T) {
	fake := newTestGateway()
	handler := newTestHandler(t, fake)
	server := newHTTP2Server(t, handler)
	client := server.Client()

	const workers = 32

	var (
		mu    sync.Mutex
		conns = map[net.Conn]struct{}{}
		ids   = map[string]struct{}{}
		proto = map[int]int{}
	)
	failures := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/memories/mem-"+strconv.Itoa(index), nil)
			if err != nil {
				failures <- err
				return
			}
			request.Header.Set(callerSubjectHeader, "agent-1")
			trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				mu.Lock()
				conns[info.Conn] = struct{}{}
				mu.Unlock()
			}}
			request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
			response, err := client.Do(request)
			if err != nil {
				failures <- err
				return
			}
			defer response.Body.Close()
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				failures <- err
				return
			}
			mu.Lock()
			proto[response.ProtoMajor]++
			ids[response.Header.Get(requestIDHeader)] = struct{}{}
			mu.Unlock()
			if response.StatusCode != http.StatusOK {
				failures <- fmt.Errorf("request %d: status %d", index, response.StatusCode)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	if proto[2] != workers {
		t.Errorf("HTTP/2 responses = %d, want %d (protocols %v)", proto[2], workers, proto)
	}
	if len(ids) != workers {
		t.Errorf("unique request ids = %d, want %d", len(ids), workers)
	}
	if len(conns) != 1 {
		t.Errorf("multiplexed connections = %d, want 1", len(conns))
	}
	if got := fake.CallCount(); got != workers {
		t.Errorf("gateway calls = %d, want %d", got, workers)
	}
}
