package config

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/provider"
)

// LocalProcessingVersion labels deterministic, non-model work such as index
// maintenance when the dMem Provider Route is disabled. It is never a model
// identity and never authorizes extraction.
const LocalProcessingVersion = domain.LocalProcessingVersion

// DefaultListenAddress is the loopback HTTP listen address used when
// [server].listen_address is omitted.
const DefaultListenAddress = "127.0.0.1:7373"

type ProviderConfig = provider.Config

type Config struct {
	ConfigVersion     int
	SourceIdentity    string
	EffectiveIdentity string

	DataDir          string
	DatabaseFilename string
	DatabasePath     string

	ShutdownGracePeriod time.Duration

	ListenAddress string
	TLSCertFile   string
	TLSKeyFile    string

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

	// ProcessingVersion is derived, never read from the file. It is the
	// configured processor identity when the Provider Route is enabled and
	// LocalProcessingVersion otherwise. It is the provenance value stored on
	// provider jobs and revisions.
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

// ProcessingVersionFor derives the provenance version for a provider
// configuration. A disabled provider never contributes a model identity.
func ProcessingVersionFor(pcfg ProviderConfig) string {
	if !pcfg.Enabled {
		return LocalProcessingVersion
	}
	return provider.ProcessorIdentity(pcfg)
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
		ListenAddress:       DefaultListenAddress,
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
		ProcessingVersion:   LocalProcessingVersion,
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
