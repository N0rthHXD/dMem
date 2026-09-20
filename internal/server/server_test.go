package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{ListenAddress: "127.0.0.1:0", ShutdownGracePeriod: time.Second}
}

func startTestServer(t *testing.T, handler http.Handler, cfg Config) *Server {
	t.Helper()
	srv, err := New(handler, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

func TestServeAndShutdown(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), testConfig())

	response, err := http.Get("http://" + srv.Addr())
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = %d %q", response.StatusCode, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-srv.Errors():
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve loop did not exit after shutdown")
	}
}

func TestBindFailureIsSynchronous(t *testing.T) {
	t.Parallel()
	first := startTestServer(t, http.NotFoundHandler(), testConfig())

	second, err := New(http.NotFoundHandler(), Config{ListenAddress: first.Addr()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := second.Start(); err == nil {
		t.Fatal("Start() succeeded on an occupied address")
	}
}

func TestGracefulShutdownDrainsInflightRequest(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	srv := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	}), testConfig())

	type result struct {
		status int
		err    error
	}
	responseCh := make(chan result, 1)
	go func() {
		response, err := http.Get("http://" + srv.Addr())
		if err != nil {
			responseCh <- result{err: err}
			return
		}
		defer response.Body.Close()
		responseCh <- result{status: response.StatusCode}
	}()

	<-started
	shutdownCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownCh <- srv.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)

	response := <-responseCh
	if response.err != nil {
		t.Fatalf("in-flight request failed: %v", response.err)
	}
	if response.status != http.StatusOK {
		t.Fatalf("in-flight status = %d", response.status)
	}
	if err := <-shutdownCh; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTLSNegotiatesHTTP2(t *testing.T) {
	t.Parallel()
	certFile, keyFile := generateCertificate(t)
	cfg := testConfig()
	cfg.TLSCertFile = certFile
	cfg.TLSKeyFile = keyFile
	srv := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}), cfg)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		ForceAttemptHTTP2: true,
	}}
	response, err := client.Get("https://" + srv.Addr())
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("ProtoMajor = %d, want HTTP/2", response.ProtoMajor)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	certFile, keyFile := generateCertificate(t)
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"missing address", Config{}},
		{"bad address", Config{ListenAddress: "not-an-address"}},
		{"cert without key", Config{ListenAddress: "127.0.0.1:0", TLSCertFile: certFile}},
		{"key without cert", Config{ListenAddress: "127.0.0.1:0", TLSKeyFile: keyFile}},
		{"missing cert file", Config{ListenAddress: "127.0.0.1:0", TLSCertFile: filepath.Join(t.TempDir(), "absent.pem"), TLSKeyFile: keyFile}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(http.NotFoundHandler(), tc.cfg); err == nil {
				t.Fatal("New() accepted invalid config")
			}
		})
	}
}

func TestNewRejectsSymlinkedTLSFile(t *testing.T) {
	t.Parallel()
	certFile, keyFile := generateCertificate(t)
	link := filepath.Join(t.TempDir(), "link.pem")
	if err := os.Symlink(certFile, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(http.NotFoundHandler(), Config{ListenAddress: "127.0.0.1:0", TLSCertFile: link, TLSKeyFile: keyFile}); err == nil {
		t.Fatal("New() accepted a symlinked TLS file")
	}
}

func TestHeaderLimitIsEnforced(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, http.NotFoundHandler(), testConfig())
	request, err := http.NewRequest(http.MethodGet, "http://"+srv.Addr(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Oversized", string(make([]byte, DefaultMaxHeaderBytes+1)))
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		response.Body.Close()
		t.Fatalf("oversized header accepted with status %d", response.StatusCode)
	}
}

func generateCertificate(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "dmem-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
