package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/N0rthHXD/dMem/internal/provider"
)

func sourceIdentity(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func effectiveIdentity(cfg Config) string {
	canonical := canonicalConfig(cfg)
	data, _ := json.Marshal(canonical)
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func CapabilityIdentity(pcfg provider.Config) string {
	if !pcfg.Enabled {
		return ""
	}
	type capabilityCanonical struct {
		Kind     string `json:"kind"`
		Endpoint string `json:"endpoint"`
		Model    string `json:"model"`
	}
	c := capabilityCanonical{Kind: pcfg.Kind, Endpoint: pcfg.Endpoint, Model: pcfg.Model}
	data, _ := json.Marshal(c)
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func CompatibilityIdentity(pcfg provider.Config, isProcessing bool) string {
	if !pcfg.Enabled {
		return ""
	}
	type compatCanonical struct {
		Model             string `json:"model"`
		Dimensions        int    `json:"dimensions,omitempty"`
		QueryInstructions string `json:"query_instructions,omitempty"`
	}
	c := compatCanonical{Model: pcfg.Model, Dimensions: pcfg.Options.Dimensions, QueryInstructions: pcfg.Options.QueryInstructions}
	data, _ := json.Marshal(c)
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func PolicyIdentity(policy provider.Policy) string {
	type policyCanonical struct {
		Default       string                `json:"default"`
		Query         string                `json:"query,omitempty"`
		ContentTypes  []string              `json:"content_types"`
		SourceTypes   []string              `json:"source_types"`
		Sensitivities []string              `json:"sensitivities"`
		Assignments   []canonicalAssignment `json:"assignments"`
	}
	c := policyCanonical{
		Default:       policy.Default,
		Query:         policy.Query,
		ContentTypes:  sortedUnique(normalizeMIMEs(policy.ContentTypes)),
		SourceTypes:   sortedUnique(normalizeSet(policy.SourceTypes)),
		Sensitivities: sortedUnique(normalizeSet(policy.Sensitivities)),
	}
	for _, a := range policy.Assignments {
		c.Assignments = append(c.Assignments, canonicalAssignment{
			Selector:           a.Selector,
			SpaceID:            a.SpaceID,
			SpaceType:          a.SpaceType,
			ExternalID:         a.ExternalID,
			Effect:             a.Effect,
			IncludeDescendants: a.IncludeDescendants,
			Priority:           a.Priority,
		})
	}
	sort.Slice(c.Assignments, func(i, j int) bool {
		return canonicalAssignmentLess(c.Assignments[i], c.Assignments[j])
	})
	data, _ := json.Marshal(c)
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

type canonicalConfigStruct struct {
	ConfigVersion       int               `json:"config_version"`
	DataDir             string            `json:"data_dir"`
	DatabaseFilename    string            `json:"database_filename"`
	ShutdownGracePeriod string            `json:"shutdown_grace_period"`
	ListenAddress       string            `json:"listen_address"`
	TLSCertFile         string            `json:"tls_cert_file,omitempty"`
	TLSKeyFile          string            `json:"tls_key_file,omitempty"`
	WorkerCount         int               `json:"worker_count"`
	QueueSoftLimit      int               `json:"queue_soft_limit"`
	QueueHardLimit      int               `json:"queue_hard_limit"`
	MaxAttempts         int               `json:"max_attempts"`
	LeaseDuration       string            `json:"lease_duration"`
	PerJobTimeout       string            `json:"per_job_timeout"`
	RetryInitialBackoff string            `json:"retry_initial_backoff"`
	RetryMaxBackoff     string            `json:"retry_max_backoff"`
	DiskSoftFree        uint64            `json:"disk_soft_free_bytes"`
	DiskHardFree        uint64            `json:"disk_hard_free_bytes"`
	ProcessingVersion   string            `json:"processing_version"`
	GraphEnabled        bool              `json:"graph_enabled"`
	ProcessingProvider  canonicalProvider `json:"processing_provider"`
	EmbeddingProvider   canonicalProvider `json:"embedding_provider"`

	Consolidation ConsolidationConfig `json:"consolidation"`
	Semantic      SemanticConfig      `json:"semantic"`
	Graph         GraphConfig         `json:"graph"`
	Ranking       RankingConfig       `json:"ranking"`
	Context       ContextConfig       `json:"context"`
}

type canonicalProvider struct {
	Enabled           bool             `json:"enabled"`
	Kind              string           `json:"kind,omitempty"`
	Endpoint          string           `json:"endpoint,omitempty"`
	Model             string           `json:"model,omitempty"`
	TelemetryDisabled bool             `json:"telemetry_disabled,omitempty"`
	Secret            canonicalSecret  `json:"secret,omitempty"`
	Options           canonicalOptions `json:"options,omitempty"`
	Limits            canonicalLimits  `json:"limits,omitempty"`
	Policy            canonicalPolicy  `json:"policy,omitempty"`
}

type canonicalSecret struct {
	Source string `json:"source,omitempty"`
	Name   string `json:"name,omitempty"`
}

type canonicalOptions struct {
	Dimensions        int    `json:"dimensions,omitempty"`
	QueryInstructions string `json:"query_instructions,omitempty"`
}

type canonicalLimits struct {
	MaxEgressInputChars    int    `json:"max_egress_input_chars,omitempty"`
	MaxAggregateInputChars int    `json:"max_aggregate_input_chars,omitempty"`
	MaxContextItems        int    `json:"max_context_items,omitempty"`
	MaxCandidateChars      int    `json:"max_candidate_chars,omitempty"`
	MaxTotalCandidateChars int    `json:"max_total_candidate_chars,omitempty"`
	MaxResponseBytes       int64  `json:"max_response_bytes,omitempty"`
	RequestTimeout         string `json:"request_timeout,omitempty"`
	MaxConcurrent          int    `json:"max_concurrent,omitempty"`
	RequestsPerMinute      int    `json:"requests_per_minute,omitempty"`
}

type canonicalPolicy struct {
	Default       string                `json:"default,omitempty"`
	Query         string                `json:"query,omitempty"`
	ContentTypes  []string              `json:"content_types,omitempty"`
	SourceTypes   []string              `json:"source_types,omitempty"`
	Sensitivities []string              `json:"sensitivities,omitempty"`
	Assignments   []canonicalAssignment `json:"assignments,omitempty"`
}

type canonicalAssignment struct {
	Selector           string `json:"selector"`
	SpaceID            string `json:"space_id,omitempty"`
	SpaceType          string `json:"space_type,omitempty"`
	ExternalID         string `json:"external_id,omitempty"`
	Effect             string `json:"effect"`
	IncludeDescendants bool   `json:"include_descendants,omitempty"`
	Priority           int    `json:"priority,omitempty"`
}

func canonicalConfig(cfg Config) canonicalConfigStruct {
	c := canonicalConfigStruct{
		ConfigVersion:       cfg.ConfigVersion,
		DataDir:             cfg.DataDir,
		DatabaseFilename:    cfg.DatabaseFilename,
		ShutdownGracePeriod: cfg.ShutdownGracePeriod.String(),
		ListenAddress:       cfg.ListenAddress,
		TLSCertFile:         cfg.TLSCertFile,
		TLSKeyFile:          cfg.TLSKeyFile,
		WorkerCount:         cfg.WorkerCount,
		QueueSoftLimit:      cfg.QueueSoftLimit,
		QueueHardLimit:      cfg.QueueHardLimit,
		MaxAttempts:         cfg.MaxAttempts,
		LeaseDuration:       cfg.LeaseDuration.String(),
		PerJobTimeout:       cfg.PerJobTimeout.String(),
		RetryInitialBackoff: cfg.RetryInitialBackoff.String(),
		RetryMaxBackoff:     cfg.RetryMaxBackoff.String(),
		DiskSoftFree:        cfg.DiskSoftFree,
		DiskHardFree:        cfg.DiskHardFree,
		ProcessingVersion:   cfg.ProcessingVersion,
		GraphEnabled:        cfg.GraphEnabled,
		ProcessingProvider:  canonicalProviderConfig(cfg.ProcessingProvider, true),
		EmbeddingProvider:   canonicalProviderConfig(cfg.EmbeddingProvider, false),
		Consolidation:       cfg.Consolidation,
		Semantic:            cfg.Semantic,
		Graph:               cfg.Graph,
		Ranking:             cfg.Ranking,
		Context:             cfg.Context,
	}
	return c
}

func canonicalProviderConfig(pcfg provider.Config, isProcessing bool) canonicalProvider {
	p := canonicalProvider{
		Enabled:           pcfg.Enabled,
		Kind:              pcfg.Kind,
		Endpoint:          pcfg.Endpoint,
		Model:             pcfg.Model,
		TelemetryDisabled: pcfg.TelemetryDisabled,
		Secret: canonicalSecret{
			Source: pcfg.Secret.Source,
			Name:   pcfg.Secret.Name,
		},
		Options: canonicalOptions{
			Dimensions:        pcfg.Options.Dimensions,
			QueryInstructions: pcfg.Options.QueryInstructions,
		},
		Limits: canonicalLimits{
			MaxEgressInputChars:    pcfg.Limits.MaxEgressInputChars,
			MaxAggregateInputChars: pcfg.Limits.MaxAggregateInputChars,
			MaxContextItems:        pcfg.Limits.MaxContextItems,
			MaxCandidateChars:      pcfg.Limits.MaxCandidateChars,
			MaxTotalCandidateChars: pcfg.Limits.MaxTotalCandidateChars,
			MaxResponseBytes:       pcfg.Limits.MaxResponseBytes,
			RequestTimeout:         pcfg.Limits.RequestTimeout.String(),
			MaxConcurrent:          pcfg.Limits.MaxConcurrent,
			RequestsPerMinute:      pcfg.Limits.RequestsPerMinute,
		},
		Policy: canonicalPolicy{
			Default:       pcfg.Policy.Default,
			Query:         pcfg.Policy.Query,
			ContentTypes:  sortedUnique(normalizeMIMEs(pcfg.Policy.ContentTypes)),
			SourceTypes:   sortedUnique(normalizeSet(pcfg.Policy.SourceTypes)),
			Sensitivities: sortedUnique(normalizeSet(pcfg.Policy.Sensitivities)),
		},
	}
	for _, a := range pcfg.Policy.Assignments {
		p.Policy.Assignments = append(p.Policy.Assignments, canonicalAssignment{
			Selector:           a.Selector,
			SpaceID:            a.SpaceID,
			SpaceType:          a.SpaceType,
			ExternalID:         a.ExternalID,
			Effect:             a.Effect,
			IncludeDescendants: a.IncludeDescendants,
			Priority:           a.Priority,
		})
	}
	sort.Slice(p.Policy.Assignments, func(i, j int) bool {
		return canonicalAssignmentLess(p.Policy.Assignments[i], p.Policy.Assignments[j])
	})
	return p
}

func canonicalAssignmentLess(a, b canonicalAssignment) bool {
	if a.Selector != b.Selector {
		return a.Selector < b.Selector
	}
	if a.SpaceID != b.SpaceID {
		return a.SpaceID < b.SpaceID
	}
	if a.SpaceType != b.SpaceType {
		return a.SpaceType < b.SpaceType
	}
	if a.ExternalID != b.ExternalID {
		return a.ExternalID < b.ExternalID
	}
	if a.Effect != b.Effect {
		return a.Effect < b.Effect
	}
	if a.IncludeDescendants != b.IncludeDescendants {
		return !a.IncludeDescendants
	}
	return a.Priority < b.Priority
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}

var _ = strings.TrimSpace
