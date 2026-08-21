package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	defaultRequestTimeout      = 30 * time.Second
	defaultShutdownGracePeriod = 15 * time.Second
	defaultMaxConnections      = 64
	defaultWorkerCount         = 2
	defaultMaxRequestSize      = 4 * 1024 * 1024
	defaultQueueSoftLimit      = 10000
	defaultQueueHardLimit      = 20000
	defaultMaxAttempts         = 3
	defaultLeaseDuration       = 30 * time.Second
	defaultPerJobTimeout       = 2 * time.Minute
	defaultRetryInitialBackoff = time.Second
	defaultRetryMaxBackoff     = 5 * time.Minute
	defaultDiskSoftFree        = 1 << 30
	defaultDiskHardFree        = 256 << 20
)

type Config struct {
	DataDir             string
	RuntimeDir          string
	DatabasePath        string
	Endpoint            string
	RequestTimeout      time.Duration
	ShutdownGracePeriod time.Duration
	MaxConnections      int
	WorkerCount         int
	MaxRequestSize      int64
	QueueSoftLimit      int
	QueueHardLimit      int
	MaxAttempts         int
	LeaseDuration       time.Duration
	PerJobTimeout       time.Duration
	RetryInitialBackoff time.Duration
	RetryMaxBackoff     time.Duration
	ProcessingVersion   string
	DiskSoftFree        uint64
	DiskHardFree        uint64
}

func Default() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home directory: %w", err)
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	runtimeHome := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeHome == "" {
		runtimeHome = filepath.Join(os.TempDir(), "dmem-"+strconv.Itoa(os.Getuid()))
	}
	dataDir := filepath.Join(dataHome, "dmem")
	runtimeDir := filepath.Join(runtimeHome, "dmem")
	return Config{
		DataDir:             dataDir,
		RuntimeDir:          runtimeDir,
		DatabasePath:        filepath.Join(dataDir, "dmem.db"),
		Endpoint:            filepath.Join(runtimeDir, "dmem.sock"),
		RequestTimeout:      defaultRequestTimeout,
		ShutdownGracePeriod: defaultShutdownGracePeriod,
		MaxConnections:      defaultMaxConnections,
		WorkerCount:         defaultWorkerCount,
		MaxRequestSize:      defaultMaxRequestSize,
		QueueSoftLimit:      defaultQueueSoftLimit,
		QueueHardLimit:      defaultQueueHardLimit,
		MaxAttempts:         defaultMaxAttempts,
		LeaseDuration:       defaultLeaseDuration,
		PerJobTimeout:       defaultPerJobTimeout,
		RetryInitialBackoff: defaultRetryInitialBackoff,
		RetryMaxBackoff:     defaultRetryMaxBackoff,
		ProcessingVersion:   "phase1-noop-v1",
		DiskSoftFree:        defaultDiskSoftFree,
		DiskHardFree:        defaultDiskHardFree,
	}, nil
}

func Parse(fs *flag.FlagSet, args []string) (Config, error) {
	cfg, err := Default()
	if err != nil {
		return Config{}, err
	}
	dataDir := fs.String("data-dir", envOr("DMEM_DATA_DIR", cfg.DataDir), "persistent data directory")
	runtimeDir := fs.String("runtime-dir", envOr("DMEM_RUNTIME_DIR", cfg.RuntimeDir), "runtime directory")
	databasePath := fs.String("database-path", os.Getenv("DMEM_DATABASE_PATH"), "SQLite database path")
	endpoint := fs.String("endpoint", os.Getenv("DMEM_ENDPOINT"), "Unix socket path")
	requestTimeout := fs.Duration("request-timeout", cfg.RequestTimeout, "HTTP request timeout")
	shutdownGrace := fs.Duration("shutdown-grace-period", cfg.ShutdownGracePeriod, "shutdown grace period")
	maxConnections := fs.Int("max-connections", cfg.MaxConnections, "maximum concurrent connections")
	workerCount := fs.Int("worker-count", cfg.WorkerCount, "job worker and storage read connection count")
	maxRequestSize := fs.Int64("max-request-size", cfg.MaxRequestSize, "maximum request body size")
	queueSoftLimit := fs.Int("queue-soft-limit", cfg.QueueSoftLimit, "soft job queue limit")
	queueHardLimit := fs.Int("queue-hard-limit", cfg.QueueHardLimit, "hard job queue limit")
	maxAttempts := fs.Int("max-attempts", cfg.MaxAttempts, "maximum job attempts")
	leaseDuration := fs.Duration("lease-duration", cfg.LeaseDuration, "job lease duration")
	perJobTimeout := fs.Duration("per-job-timeout", cfg.PerJobTimeout, "job processing timeout")
	retryInitialBackoff := fs.Duration("retry-initial-backoff", cfg.RetryInitialBackoff, "initial job retry backoff")
	retryMaxBackoff := fs.Duration("retry-max-backoff", cfg.RetryMaxBackoff, "maximum job retry backoff")
	diskSoftFree := fs.Uint64("disk-soft-free", cfg.DiskSoftFree, "soft minimum filesystem free bytes")
	diskHardFree := fs.Uint64("disk-hard-free", cfg.DiskHardFree, "hard minimum filesystem free bytes")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	cfg.DataDir = *dataDir
	cfg.RuntimeDir = *runtimeDir
	if *databasePath == "" {
		cfg.DatabasePath = filepath.Join(cfg.DataDir, "dmem.db")
	} else {
		cfg.DatabasePath = *databasePath
	}
	if *endpoint == "" {
		cfg.Endpoint = filepath.Join(cfg.RuntimeDir, "dmem.sock")
	} else {
		cfg.Endpoint = *endpoint
	}
	cfg.RequestTimeout = *requestTimeout
	cfg.ShutdownGracePeriod = *shutdownGrace
	cfg.MaxConnections = *maxConnections
	cfg.WorkerCount = *workerCount
	cfg.MaxRequestSize = *maxRequestSize
	cfg.QueueSoftLimit = *queueSoftLimit
	cfg.QueueHardLimit = *queueHardLimit
	cfg.MaxAttempts = *maxAttempts
	cfg.LeaseDuration = *leaseDuration
	cfg.PerJobTimeout = *perJobTimeout
	cfg.RetryInitialBackoff = *retryInitialBackoff
	cfg.RetryMaxBackoff = *retryMaxBackoff
	cfg.DiskSoftFree = *diskSoftFree
	cfg.DiskHardFree = *diskHardFree
	return cfg.normalized()
}

func (c Config) normalized() (Config, error) {
	var err error
	if c.DataDir, err = filepath.Abs(c.DataDir); err != nil {
		return Config{}, fmt.Errorf("resolve data directory: %w", err)
	}
	if c.RuntimeDir, err = filepath.Abs(c.RuntimeDir); err != nil {
		return Config{}, fmt.Errorf("resolve runtime directory: %w", err)
	}
	if c.DatabasePath, err = filepath.Abs(c.DatabasePath); err != nil {
		return Config{}, fmt.Errorf("resolve database path: %w", err)
	}
	if c.Endpoint, err = filepath.Abs(c.Endpoint); err != nil {
		return Config{}, fmt.Errorf("resolve endpoint: %w", err)
	}
	if c.RequestTimeout < time.Second || c.RequestTimeout > 5*time.Minute {
		return Config{}, errors.New("request timeout must be between 1s and 5m")
	}
	if c.ShutdownGracePeriod < time.Second || c.ShutdownGracePeriod > 2*time.Minute {
		return Config{}, errors.New("shutdown grace period must be between 1s and 2m")
	}
	if c.MaxConnections < 1 || c.MaxConnections > 1024 {
		return Config{}, errors.New("max connections must be between 1 and 1024")
	}
	if c.WorkerCount < 1 || c.WorkerCount > 32 {
		return Config{}, errors.New("worker count must be between 1 and 32")
	}
	if c.MaxRequestSize < 1024 || c.MaxRequestSize > 16*1024*1024 {
		return Config{}, errors.New("max request size must be between 1 KiB and 16 MiB")
	}
	if c.QueueSoftLimit < 1 || c.QueueSoftLimit > 1000000 {
		return Config{}, errors.New("queue soft limit must be between 1 and 1000000")
	}
	if c.QueueHardLimit <= c.QueueSoftLimit {
		return Config{}, errors.New("queue hard limit must be greater than soft limit")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 20 {
		return Config{}, errors.New("max attempts must be between 1 and 20")
	}
	if c.LeaseDuration < 5*time.Second || c.LeaseDuration > 10*time.Minute {
		return Config{}, errors.New("lease duration must be between 5s and 10m")
	}
	if c.PerJobTimeout < c.LeaseDuration {
		return Config{}, errors.New("per-job timeout must not be shorter than lease duration")
	}
	if c.RetryInitialBackoff < 100*time.Millisecond || c.RetryInitialBackoff > time.Minute {
		return Config{}, errors.New("retry initial backoff must be between 100ms and 1m")
	}
	if c.RetryMaxBackoff < c.RetryInitialBackoff {
		return Config{}, errors.New("retry max backoff must not be shorter than initial backoff")
	}
	if c.DiskHardFree == 0 || c.DiskSoftFree <= c.DiskHardFree {
		return Config{}, errors.New("disk soft free must be greater than non-zero hard free")
	}
	if filepath.Dir(c.DatabasePath) != c.DataDir {
		return Config{}, errors.New("database path must be directly inside data directory")
	}
	if filepath.Dir(c.Endpoint) != c.RuntimeDir {
		return Config{}, errors.New("endpoint must be directly inside runtime directory")
	}
	if len(c.Endpoint) > 107 {
		return Config{}, errors.New("Unix socket path exceeds Linux limit")
	}
	return c, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
