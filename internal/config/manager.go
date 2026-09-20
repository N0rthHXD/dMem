package config

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync/atomic"
	"time"
)

type ActivationTrigger string

const (
	TriggerStartup ActivationTrigger = "startup"
	TriggerAPI     ActivationTrigger = "api"
	TriggerSIGHUP  ActivationTrigger = "sighup"
)

type ActivationOutcome string

const (
	OutcomeActivated ActivationOutcome = "activated"
	OutcomeNoChange  ActivationOutcome = "no_change"
	OutcomeRejected  ActivationOutcome = "rejected"
	OutcomeFailed    ActivationOutcome = "failed"
)

type ActivationResult struct {
	AttemptID                 string
	Outcome                   ActivationOutcome
	Trigger                   ActivationTrigger
	SourceIdentity            string
	EffectiveIdentity         string
	PreviousEffectiveIdentity string
	ChangedCapabilities       []string
	ReconciliationClasses     []string
	Diagnostics               []Diagnostic
	ErrorCode                 string
	StartedAt                 time.Time
	FinishedAt                time.Time
}

type Manager struct {
	path       string
	current    atomic.Pointer[Snapshot]
	generation atomic.Int64
	mu         chan struct{}
}

func NewManager(path string, initial *Snapshot) *Manager {
	m := &Manager{
		path: path,
		mu:   make(chan struct{}, 1),
	}
	if initial != nil {
		m.current.Store(initial)
		m.generation.Store(int64(initial.ActivationGeneration))
	}
	return m
}

func (m *Manager) Snapshot() *Snapshot {
	return m.current.Load()
}

func (m *Manager) ActivationGeneration() int {
	return int(m.generation.Load())
}

func (m *Manager) Reload(ctx context.Context, trigger ActivationTrigger) ActivationResult {
	startedAt := time.Now()
	attemptID := newAttemptID()
	result := m.tryReload(ctx, trigger, startedAt, attemptID)
	result.FinishedAt = time.Now()
	return result
}

func (m *Manager) tryReload(ctx context.Context, trigger ActivationTrigger, startedAt time.Time, attemptID string) ActivationResult {
	select {
	case m.mu <- struct{}{}:
		defer func() { <-m.mu }()
	case <-ctx.Done():
		return ActivationResult{
			AttemptID:  attemptID,
			Outcome:    OutcomeFailed,
			Trigger:    trigger,
			StartedAt:  startedAt,
			FinishedAt: time.Now(),
			ErrorCode:  "reload_conflict",
		}
	}
	cfg, err := Load(m.path)
	if err != nil {
		code := "config_invalid"
		if ve, ok := err.(*ValidationError); ok && ve.Code != "" {
			code = ve.Code
		}
		return ActivationResult{
			AttemptID:   attemptID,
			Outcome:     OutcomeRejected,
			Trigger:     trigger,
			StartedAt:   startedAt,
			ErrorCode:   code,
			Diagnostics: extractDiagnostics(err),
		}
	}
	prev := m.current.Load()
	prevID := ""
	if prev != nil {
		prevID = prev.Config.EffectiveIdentity
		if changed := restartFieldsChanged(cfg, prev.Config); len(changed) > 0 {
			return ActivationResult{
				AttemptID:                 attemptID,
				Outcome:                   OutcomeRejected,
				Trigger:                   trigger,
				SourceIdentity:            cfg.SourceIdentity,
				EffectiveIdentity:         cfg.EffectiveIdentity,
				PreviousEffectiveIdentity: prevID,
				StartedAt:                 startedAt,
				ErrorCode:                 "config_restart_required",
				Diagnostics:               restartDiagnostics(changed),
			}
		}
	}
	if prev != nil && cfg.EffectiveIdentity == prev.Config.EffectiveIdentity {
		return ActivationResult{
			AttemptID:                 attemptID,
			Outcome:                   OutcomeNoChange,
			Trigger:                   trigger,
			SourceIdentity:            cfg.SourceIdentity,
			EffectiveIdentity:         cfg.EffectiveIdentity,
			PreviousEffectiveIdentity: prevID,
			StartedAt:                 startedAt,
		}
	}
	var changed []string
	if prev != nil {
		if cfg.ProcessingVersion != prev.Config.ProcessingVersion ||
			CapabilityIdentity(cfg.ProcessingProvider) != prev.ProcessingCapability.ConfigurationIdentity ||
			PolicyIdentity(cfg.ProcessingProvider.Policy) != prev.ProcessingCapability.PolicyIdentity {
			changed = append(changed, "processing")
		}
		if CapabilityIdentity(cfg.EmbeddingProvider) != prev.EmbeddingCapability.ConfigurationIdentity ||
			PolicyIdentity(cfg.EmbeddingProvider.Policy) != prev.EmbeddingCapability.PolicyIdentity {
			changed = append(changed, "embedding")
		}
	} else {
		if cfg.ProcessingProvider.Enabled {
			changed = append(changed, "processing")
		}
		if cfg.EmbeddingProvider.Enabled {
			changed = append(changed, "embedding")
		}
	}
	gen := int(m.generation.Add(1))
	snap := newSnapshot(cfg, gen, attemptID)
	m.current.Store(&snap)
	classes := reconciliationClasses(prev, cfg, changed)
	return ActivationResult{
		AttemptID:                 attemptID,
		Outcome:                   OutcomeActivated,
		Trigger:                   trigger,
		SourceIdentity:            cfg.SourceIdentity,
		EffectiveIdentity:         cfg.EffectiveIdentity,
		PreviousEffectiveIdentity: prevID,
		ChangedCapabilities:       changed,
		ReconciliationClasses:     classes,
		StartedAt:                 startedAt,
	}
}

func (m *Manager) Revert(prev *Snapshot) {
	if prev == nil {
		return
	}
	m.current.Store(prev)
	m.generation.Store(int64(prev.ActivationGeneration))
}

func extractDiagnostics(err error) []Diagnostic {
	if ve, ok := err.(*ValidationError); ok {
		return ve.Diagnostics
	}
	return nil
}

func restartFieldsChanged(candidate, previous Config) []string {
	var changed []string
	if candidate.DataDir != previous.DataDir {
		changed = append(changed, "instance.data_dir")
	}
	if candidate.DatabaseFilename != previous.DatabaseFilename {
		changed = append(changed, "instance.database_filename")
	}
	if candidate.WorkerCount != previous.WorkerCount {
		changed = append(changed, "jobs.worker_count")
	}
	if candidate.ListenAddress != previous.ListenAddress {
		changed = append(changed, "server.listen_address")
	}
	if candidate.TLSCertFile != previous.TLSCertFile || candidate.TLSKeyFile != previous.TLSKeyFile {
		changed = append(changed, "server.tls")
	}
	return changed
}

func restartDiagnostics(paths []string) []Diagnostic {
	diags := make([]Diagnostic, 0, len(paths))
	for _, p := range paths {
		diags = append(diags, Diagnostic{Code: "config_restart_required", Path: p})
	}
	return diags
}

func reconciliationClasses(prev *Snapshot, cfg Config, changed []string) []string {
	if prev == nil {
		var classes []string
		for _, c := range changed {
			classes = append(classes, c+":enable")
		}
		return classes
	}
	var classes []string
	for _, c := range changed {
		var prevCap CapabilitySnapshot
		var next ProviderConfig
		switch c {
		case "processing":
			prevCap = prev.ProcessingCapability
			next = cfg.ProcessingProvider
		case "embedding":
			prevCap = prev.EmbeddingCapability
			next = cfg.EmbeddingProvider
		default:
			continue
		}
		switch {
		case !prevCap.Enabled && next.Enabled:
			classes = append(classes, c+":enable")
		case prevCap.Enabled && !next.Enabled:
			classes = append(classes, c+":disable")
		case prevCap.Enabled && next.Enabled:
			classes = append(classes, capabilityDiffClass(c, prevCap, next))
		}
	}
	return classes
}

func capabilityDiffClass(capability string, prevCap CapabilitySnapshot, next ProviderConfig) string {
	isProcessing := capability == "processing"
	nextCompat := CompatibilityIdentity(next, isProcessing)
	nextPolicy := PolicyIdentity(next.Policy)
	nextConfig := CapabilityIdentity(next)
	switch {
	case nextCompat != prevCap.CompatibilityIdentity:
		return capability + ":compatibility"
	case nextPolicy != prevCap.PolicyIdentity:
		// A policy identity alone cannot establish whether every selector became
		// broader. Treat it as a narrowing change so recovery re-evaluates all
		// records before any further egress is allowed.
		return capability + ":policy_narrow"
	case nextConfig != prevCap.ConfigurationIdentity:
		return capability + ":runtime_limits"
	default:
		return capability + ":runtime_limits"
	}
}

func newAttemptID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("attempt-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
