package config

import (
	"encoding/json"
	"time"

	"github.com/N0rthHXD/dMem/internal/provider"
)

type CapabilitySnapshot struct {
	Enabled               bool
	Kind                  string
	Endpoint              string
	Model                 string
	TelemetryDisabled     bool
	Secret                provider.SecretReference
	Options               provider.EmbeddingOptions
	Limits                provider.Limits
	Policy                provider.Policy
	ConfigurationIdentity string
	CompatibilityIdentity string
	PolicyIdentity        string
}

type Snapshot struct {
	Config               Config
	ActivationID         string
	ActivationGeneration int
	ActivatedAt          time.Time
	ProcessingCapability CapabilitySnapshot
	EmbeddingCapability  CapabilitySnapshot
}

func newSnapshot(cfg Config, generation int, activationID string) Snapshot {
	proc := CapabilitySnapshot{
		Enabled:               cfg.ProcessingProvider.Enabled,
		Kind:                  cfg.ProcessingProvider.Kind,
		Endpoint:              cfg.ProcessingProvider.Endpoint,
		Model:                 cfg.ProcessingProvider.Model,
		TelemetryDisabled:     cfg.ProcessingProvider.TelemetryDisabled,
		Secret:                cfg.ProcessingProvider.Secret,
		Limits:                cfg.ProcessingProvider.Limits,
		Policy:                cfg.ProcessingProvider.Policy,
		ConfigurationIdentity: CapabilityIdentity(cfg.ProcessingProvider),
		CompatibilityIdentity: CompatibilityIdentity(cfg.ProcessingProvider, true),
		PolicyIdentity:        CapabilityPolicyIdentity(cfg.ProcessingProvider),
	}
	emb := CapabilitySnapshot{
		Enabled:               cfg.EmbeddingProvider.Enabled,
		Kind:                  cfg.EmbeddingProvider.Kind,
		Endpoint:              cfg.EmbeddingProvider.Endpoint,
		Model:                 cfg.EmbeddingProvider.Model,
		TelemetryDisabled:     cfg.EmbeddingProvider.TelemetryDisabled,
		Secret:                cfg.EmbeddingProvider.Secret,
		Options:               cfg.EmbeddingProvider.Options,
		Limits:                cfg.EmbeddingProvider.Limits,
		Policy:                cfg.EmbeddingProvider.Policy,
		ConfigurationIdentity: CapabilityIdentity(cfg.EmbeddingProvider),
		CompatibilityIdentity: CompatibilityIdentity(cfg.EmbeddingProvider, false),
		PolicyIdentity:        CapabilityPolicyIdentity(cfg.EmbeddingProvider),
	}
	return Snapshot{
		Config:               cfg,
		ActivationID:         activationID,
		ActivationGeneration: generation,
		ActivatedAt:          time.Now(),
		ProcessingCapability: proc,
		EmbeddingCapability:  emb,
	}
}

// CapabilityPolicyIdentity returns the policy identity only for an enabled
// provider. A disabled provider has no egress policy and must not look like one.
func CapabilityPolicyIdentity(pcfg provider.Config) string {
	if !pcfg.Enabled {
		return ""
	}
	return PolicyIdentity(pcfg.Policy)
}

type RedactedConfig struct {
	ConfigVersion       int                `json:"config_version"`
	SourceIdentity      string             `json:"source_identity"`
	EffectiveIdentity   string             `json:"effective_identity"`
	DataDir             string             `json:"data_dir"`
	DatabaseFilename    string             `json:"database_filename"`
	ShutdownGracePeriod string             `json:"shutdown_grace_period"`
	ListenAddress       string             `json:"listen_address"`
	TLSEnabled          bool               `json:"tls_enabled"`
	WorkerCount         int                `json:"worker_count"`
	QueueSoftLimit      int                `json:"queue_soft_limit"`
	QueueHardLimit      int                `json:"queue_hard_limit"`
	MaxAttempts         int                `json:"max_attempts"`
	LeaseDuration       string             `json:"lease_duration"`
	PerJobTimeout       string             `json:"per_job_timeout"`
	RetryInitialBackoff string             `json:"retry_initial_backoff"`
	RetryMaxBackoff     string             `json:"retry_max_backoff"`
	DiskSoftFree        uint64             `json:"disk_soft_free_bytes"`
	DiskHardFree        uint64             `json:"disk_hard_free_bytes"`
	ProcessingVersion   string             `json:"processing_version"`
	GraphEnabled        bool               `json:"graph_enabled"`
	AutoConfirm         RedactedReview     `json:"review_auto_confirm"`
	Processing          RedactedCapability `json:"processing"`
	Embedding           RedactedCapability `json:"embedding"`
}

type RedactedReview struct {
	Enabled            bool     `json:"enabled"`
	Version            string   `json:"version"`
	MinConfidence      float64  `json:"min_confidence"`
	Kinds              []string `json:"kinds"`
	ProcessingVersions []string `json:"processing_versions,omitempty"`
	MaxPerSpacePerHour int      `json:"max_per_space_per_hour"`
}

type RedactedCapability struct {
	Enabled                 bool     `json:"enabled"`
	Kind                    string   `json:"kind,omitempty"`
	Model                   string   `json:"model,omitempty"`
	ConfigurationIdentity   string   `json:"configuration_identity,omitempty"`
	CompatibilityIdentity   string   `json:"compatibility_identity,omitempty"`
	PolicyIdentity          string   `json:"policy_identity,omitempty"`
	SecretAvailable         bool     `json:"secret_available,omitempty"`
	PolicyContentTypes      []string `json:"policy_content_types,omitempty"`
	PolicySourceTypes       []string `json:"policy_source_types,omitempty"`
	PolicySensitivities     []string `json:"policy_sensitivities,omitempty"`
	PolicyAssignmentCount   int      `json:"policy_assignment_count,omitempty"`
	State                   string   `json:"state"`
	ConfigurationGeneration int      `json:"configuration_generation"`
	PolicyGeneration        int      `json:"policy_generation"`
	BlockedWorkCount        int      `json:"blocked_work_count"`
	ReconciliationState     string   `json:"reconciliation_state"`
}

func (s Snapshot) Redacted() RedactedConfig {
	return RedactedConfig{
		ConfigVersion:       s.Config.ConfigVersion,
		SourceIdentity:      s.Config.SourceIdentity,
		EffectiveIdentity:   s.Config.EffectiveIdentity,
		DataDir:             s.Config.DataDir,
		DatabaseFilename:    s.Config.DatabaseFilename,
		ShutdownGracePeriod: s.Config.ShutdownGracePeriod.String(),
		ListenAddress:       s.Config.ListenAddress,
		TLSEnabled:          s.Config.TLSCertFile != "" && s.Config.TLSKeyFile != "",
		WorkerCount:         s.Config.WorkerCount,
		QueueSoftLimit:      s.Config.QueueSoftLimit,
		QueueHardLimit:      s.Config.QueueHardLimit,
		MaxAttempts:         s.Config.MaxAttempts,
		LeaseDuration:       s.Config.LeaseDuration.String(),
		PerJobTimeout:       s.Config.PerJobTimeout.String(),
		RetryInitialBackoff: s.Config.RetryInitialBackoff.String(),
		RetryMaxBackoff:     s.Config.RetryMaxBackoff.String(),
		DiskSoftFree:        s.Config.DiskSoftFree,
		DiskHardFree:        s.Config.DiskHardFree,
		ProcessingVersion:   s.Config.ProcessingVersion,
		GraphEnabled:        s.Config.GraphEnabled,
		AutoConfirm: RedactedReview{
			Enabled:            s.Config.Review.AutoConfirmEnabled,
			Version:            s.Config.Review.AutoConfirmVersion,
			MinConfidence:      s.Config.Review.AutoConfirmMinConfidence,
			Kinds:              s.Config.Review.AutoConfirmKinds,
			ProcessingVersions: s.Config.Review.AutoConfirmProcessingVersions,
			MaxPerSpacePerHour: s.Config.Review.AutoConfirmMaxPerSpacePerHour,
		},
		Processing: redactCapability(s.ProcessingCapability),
		Embedding:  redactCapability(s.EmbeddingCapability),
	}
}

func redactCapability(c CapabilitySnapshot) RedactedCapability {
	r := RedactedCapability{
		Enabled:               c.Enabled,
		Kind:                  c.Kind,
		Model:                 c.Model,
		ConfigurationIdentity: c.ConfigurationIdentity,
		CompatibilityIdentity: c.CompatibilityIdentity,
		PolicyIdentity:        c.PolicyIdentity,
		PolicyContentTypes:    c.Policy.ContentTypes,
		PolicySourceTypes:     c.Policy.SourceTypes,
		PolicySensitivities:   c.Policy.Sensitivities,
		PolicyAssignmentCount: len(c.Policy.Assignments),
		State:                 "disabled",
		ReconciliationState:   "idle",
	}
	if c.Enabled {
		r.SecretAvailable = provider.SecretAvailable(c.Secret)
		r.State = "ready"
	}
	return r
}

func (r RedactedConfig) JSON() ([]byte, error) {
	return json.Marshal(r)
}
