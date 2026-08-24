package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/N0rthHXD/dMem/internal/aggregate"
	"github.com/N0rthHXD/dMem/internal/api"
	"github.com/N0rthHXD/dMem/internal/application"
	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/identity"
	"github.com/N0rthHXD/dMem/internal/instance"
	"github.com/N0rthHXD/dMem/internal/jobs"
	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/N0rthHXD/dMem/internal/storage"
	"github.com/N0rthHXD/dMem/internal/vector"
)

type Daemon struct {
	lock       *instance.Lock
	store      *storage.Store
	listener   net.Listener
	server     *http.Server
	handler    *api.Handler
	jobs       *jobs.Service
	endpoint   string
	socketInfo os.FileInfo
	closeOnce  sync.Once
	closeErr   error
}

func Start(ctx context.Context, cfg config.Config) (_ *Daemon, err error) {
	processor := jobs.Processor(jobs.NoopProcessor{})
	var aggregateProvider domain.AggregateProvider
	if cfg.ProcessingProvider.Enabled {
		client, err := provider.New(cfg.ProcessingProvider, nil)
		if err != nil {
			return nil, err
		}
		processor = jobs.NewProviderProcessor(client)
		aggregateProvider = client
	}
	return startService(ctx, cfg, processor, aggregateProvider, true)
}

func start(ctx context.Context, cfg config.Config, processor jobs.Processor) (_ *Daemon, err error) {
	return startService(ctx, cfg, processor, nil, true)
}

// StartStaging starts the production service assembly without workers or providers.
func StartStaging(ctx context.Context, cfg config.Config) (_ *Daemon, err error) {
	cfg.ProcessingProvider = config.ProviderConfig{}
	cfg.EmbeddingProvider = config.ProviderConfig{}
	return startService(ctx, cfg, jobs.NoopProcessor{}, nil, false)
}

func startService(ctx context.Context, cfg config.Config, processor jobs.Processor, aggregateProvider domain.AggregateProvider, startWorkers bool) (_ *Daemon, err error) {
	if err := prepareDir(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare data directory: %w", err)
	}
	if err := prepareDir(cfg.RuntimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare runtime directory: %w", err)
	}

	lock, err := instance.Acquire(filepath.Join(cfg.DataDir, "daemon.lock"))
	if err != nil {
		return nil, err
	}
	d := &Daemon{lock: lock, endpoint: cfg.Endpoint}
	defer func() {
		if err != nil {
			_ = d.cleanup()
		}
	}()

	d.store, err = storage.Open(ctx, cfg.DatabasePath, storage.Options{ReadConnections: cfg.WorkerCount})
	if err != nil {
		return nil, err
	}
	ftsState, err := d.store.IndexState(ctx)
	if err != nil {
		return nil, fmt.Errorf("read FTS state: %w", err)
	}
	repository := storage.NewRepository(d.store, domain.Runtime{Now: time.Now, ID: identity.UUID}, cfg.QueueSoftLimit, cfg.QueueHardLimit, cfg.MaxAttempts, cfg.ProcessingVersion)
	repository.SetDiskFreeThresholds(cfg.DiskSoftFree, cfg.DiskHardFree)
	index, err := vector.Open(filepath.Join(cfg.DataDir, "semantic-index"))
	if err != nil {
		return nil, err
	}
	if cfg.EmbeddingProvider.Enabled {
		embedding, providerErr := provider.New(cfg.EmbeddingProvider, nil)
		if providerErr != nil {
			return nil, providerErr
		}
		repository.ConfigureSemantic(embedding, index, cfg.EmbeddingProvider.Kind, cfg.EmbeddingProvider.Model)
		if err := repository.EnsureSemanticRecoveryJobs(ctx); err != nil {
			return nil, fmt.Errorf("prepare semantic recovery: %w", err)
		}
	}
	aggregateService := aggregate.NewService(repository, aggregateProvider, "aggregate/provider", cfg.ProcessingProvider.EgressPolicyVersion, cfg.ProcessingVersion, cfg.ProcessingProvider.MaxContextItems)
	d.jobs, err = jobs.NewService(repository, cfg, processor, jobs.Dependencies{
		Indexes: repository, EntityIndexes: repository, RelationIndexes: repository,
		FeedbackAggregates: repository, AggregateRebuilder: aggregateService, DiskPressure: repository,
	})
	if err != nil {
		return nil, err
	}
	if startWorkers {
		if err := d.jobs.Start(ctx); err != nil {
			return nil, err
		}
	}
	if err := clearStaleSocket(cfg.Endpoint); err != nil {
		return nil, err
	}
	d.listener, err = net.Listen("unix", cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen on Unix socket: %w", err)
	}
	if err := os.Chmod(cfg.Endpoint, 0o600); err != nil {
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}
	d.socketInfo, err = os.Lstat(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("inspect Unix socket: %w", err)
	}
	d.listener = limitListener(d.listener, cfg.MaxConnections)
	d.handler = api.NewHandler(ftsState, api.HandlerDependencies{
		Repository: repository, AggregateService: aggregateService, ProcessorHealth: d.jobs,
		MemoryService:  application.NewService(repository),
		MaxRequestSize: cfg.MaxRequestSize, ID: identity.UUID,
	})
	d.server = &http.Server{
		Handler:           d.handler.Routes(),
		ReadHeaderTimeout: cfg.RequestTimeout,
		ReadTimeout:       cfg.RequestTimeout,
		WriteTimeout:      cfg.RequestTimeout,
		IdleTimeout:       cfg.RequestTimeout,
	}
	listener := d.listener
	go func() {
		if serveErr := d.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			d.handler.SetReady(false)
		}
	}()
	return d, nil
}

func (d *Daemon) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		if d.handler != nil {
			d.handler.SetReady(false)
		}
		if d.server != nil {
			d.closeErr = d.server.Shutdown(ctx)
		}
		if d.jobs != nil {
			d.closeErr = errors.Join(d.closeErr, d.jobs.Shutdown(ctx))
			d.jobs = nil
		}
		d.closeErr = errors.Join(d.closeErr, d.cleanup())
	})
	return d.closeErr
}

func (d *Daemon) cleanup() error {
	var errs []error
	if d.listener != nil {
		if err := d.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
		d.listener = nil
	}
	if d.jobs != nil {
		errs = append(errs, d.jobs.Shutdown(context.Background()))
		d.jobs = nil
	}
	if d.store != nil {
		errs = append(errs, d.store.Close())
		d.store = nil
	}
	if d.socketInfo != nil {
		if current, err := os.Lstat(d.endpoint); err == nil && os.SameFile(current, d.socketInfo) {
			errs = append(errs, os.Remove(d.endpoint))
		}
		d.socketInfo = nil
	}
	if d.lock != nil {
		errs = append(errs, d.lock.Close())
		d.lock = nil
	}
	return errors.Join(errs...)
}

func prepareDir(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Clean(path) {
		return errors.New("directory path must not contain symlinks")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("directory must be owned by the current user")
	}
	return os.Chmod(path, mode)
}

func clearStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing endpoint: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("existing endpoint is not a Unix socket")
	}
	conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return errors.New("Unix socket is already served by an active process")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale Unix socket: %w", err)
	}
	return nil
}

type limitedListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func limitListener(listener net.Listener, maximum int) net.Listener {
	return &limitedListener{
		Listener: listener,
		sem:      make(chan struct{}, maximum),
		done:     make(chan struct{}),
	}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: conn, release: func() { <-l.sem }}, nil
}

func (l *limitedListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	releaseOnce sync.Once
	release     func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}
