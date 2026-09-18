package config

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/N0rthHXD/dMem/internal/provider"
)

const DefaultProcessingVersion = "builtin-noop-v1"

type ProviderConfig = provider.Config

type Config struct {
	ConfigVersion     int
	SourceIdentity    string
	EffectiveIdentity string

	DataDir          string
	DatabaseFilename string
	DatabasePath     string

	ShutdownGracePeriod time.Duration

	WorkerCount         int
	QueueSoftLimit      int
	QueueHardLimit      int
	MaxAttempts         int
	LeaseDuration       time.Duration
	PerJobTimeout       time.Duration
	RetryInitialBackoff time.Duration
	RetryMaxBackoff     time.Duration

	DiskSoftFree uint64
	DiskHardFree uint64

	ProcessingVersion  string
	ProcessingProvider ProviderConfig
	EmbeddingProvider  ProviderConfig

	GraphEnabled bool

	Consolidation ConsolidationConfig
	Semantic      SemanticConfig
	Graph         GraphConfig
	Ranking       RankingConfig
	Context       ContextConfig
}

func Default() (Config, error) {
	dataDir, err := defaultDataDir()
	if err != nil {
		return Config{}, err
	}
	return Config{
		ConfigVersion:       1,
		DataDir:             dataDir,
		DatabaseFilename:    "dmem.db",
		DatabasePath:        filepath.Join(dataDir, "dmem.db"),
		ShutdownGracePeriod: 15 * time.Second,
		WorkerCount:         2,
		QueueSoftLimit:      10000,
		QueueHardLimit:      20000,
		MaxAttempts:         3,
		LeaseDuration:       30 * time.Second,
		PerJobTimeout:       2 * time.Minute,
		RetryInitialBackoff: time.Second,
		RetryMaxBackoff:     5 * time.Minute,
		DiskSoftFree:        1 << 30,
		DiskHardFree:        256 << 20,
		ProcessingVersion:   DefaultProcessingVersion,
		GraphEnabled:        true,
		Consolidation:       defaultConsolidationConfig(),
		Semantic:            defaultSemanticConfig(),
		Graph:               defaultGraphConfig(),
		Ranking:             defaultRankingConfig(),
		Context:             defaultContextConfig(),
	}, nil
}

func Load(path string) (Config, error) {
	if path == "" {
		var err error
		path, err = defaultConfigPath()
		if err != nil {
			return Config{}, err
		}
	}
	data, err := readFile(path)
	if err != nil {
		return Config{}, err
	}
	doc, diags, err := decodeDocument(data)
	if err != nil {
		return Config{}, err
	}
	if len(diags) > 0 {
		return Config{}, &ValidationError{Code: diags[0].Code, Diagnostics: diags}
	}
	cfg, err := normalize(doc, data)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func defaultDataDir() (string, error) {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg != "" {
		if !filepath.IsAbs(xdg) {
			return "", errors.New("XDG_DATA_HOME must be absolute when set")
		}
		return filepath.Join(xdg, "dmem"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "dmem"), nil
}
