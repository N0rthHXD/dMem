package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/N0rthHXD/dMem/pkg/logging"
)

// Transport hardening defaults. They are deliberately not configuration keys:
// operators choose the bind address and optional TLS material only.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultMaxHeaderBytes    = 1 << 20
	DefaultShutdownGrace     = 15 * time.Second
	DefaultTLSMinVersion     = tls.VersionTLS12
)

// Config is the transport configuration resolved by the composition root.
type Config struct {
	ListenAddress       string
	TLSCertFile         string
	TLSKeyFile          string
	ShutdownGracePeriod time.Duration
	Logger              *slog.Logger
}

// Server owns one TCP listener and its http.Server. Start binds synchronously so
// bind failures surface before the caller reports readiness.
type Server struct {
	httpServer *http.Server
	listener   net.Listener
	tlsConfig  *tls.Config
	grace      time.Duration
	logger     *slog.Logger
	serveErr   chan error
}

// New validates the transport configuration and returns an unstarted server.
func New(handler http.Handler, cfg Config) (*Server, error) {
	if handler == nil {
		return nil, errors.New("server: handler is required")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	logger := logging.WithComponent(cfg.Logger, logging.ComponentServer)
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	tlsConfig, err := loadTLSConfig(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, err
	}
	grace := cfg.ShutdownGracePeriod
	if grace <= 0 {
		grace = DefaultShutdownGrace
	}
	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.ListenAddress,
			Handler:           handler,
			ReadHeaderTimeout: DefaultReadHeaderTimeout,
			ReadTimeout:       DefaultReadTimeout,
			WriteTimeout:      DefaultWriteTimeout,
			IdleTimeout:       DefaultIdleTimeout,
			MaxHeaderBytes:    DefaultMaxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
		tlsConfig: tlsConfig,
		grace:     grace,
		logger:    logger,
	}, nil
}

// Start binds the configured address and serves in the background. A bind
// failure is returned directly; later serving failures are reported on Errors.
func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("server: listen: %w", err)
	}
	s.listener = listener
	s.warnIfExposed(listener.Addr())
	s.serveErr = make(chan error, 1)
	go func() {
		var serveErr error
		if s.tlsConfig != nil {
			s.httpServer.TLSConfig = s.tlsConfig
			serveErr = s.httpServer.ServeTLS(listener, "", "")
		} else {
			serveErr = s.httpServer.Serve(listener)
		}
		s.serveErr <- serveErr
	}()
	return nil
}

// Addr returns the bound address, valid after Start.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Errors reports the terminal serving result. It returns nil before Start.
func (s *Server) Errors() <-chan error {
	return s.serveErr
}

// GracePeriod returns the configured graceful shutdown budget.
func (s *Server) GracePeriod() time.Duration {
	return s.grace
}

// Shutdown stops accepting connections and drains in-flight requests until the
// context expires.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		_ = s.httpServer.Close()
		return err
	}
	return nil
}

func (s *Server) warnIfExposed(addr net.Addr) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP.IsLoopback() {
		return
	}
	s.logger.Warn("http server bound to a non-loopback address without authentication", "addr", addr.String())
}
