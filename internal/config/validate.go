package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/provider"
	"github.com/pelletier/go-toml/v2"
)

func decodeDocument(data []byte) (fileDocument, []Diagnostic, error) {
	var doc fileDocument
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			diags := make([]Diagnostic, 0, len(strict.Errors))
			for _, e := range strict.Errors {
				key := e.Key()
				row, col := e.Position()
				diags = append(diags, Diagnostic{
					Code:   "config_unknown_field",
					Path:   strings.Join(key, "."),
					Line:   row,
					Column: col,
				})
			}
			return fileDocument{}, diags, nil
		}
		if isDuplicateKey(err) {
			return fileDocument{}, []Diagnostic{{
				Code: "config_duplicate_field",
				Path: extractPath(err),
			}}, nil
		}
		return fileDocument{}, nil, &ValidationError{Code: "config_invalid_syntax", Diagnostics: []Diagnostic{{Code: "config_invalid_syntax", Path: extractPath(err)}}}
	}
	return doc, nil, nil
}

func isDuplicateKey(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "key") && (strings.Contains(msg, "redefinition") || strings.Contains(msg, "duplicate") || strings.Contains(msg, "already"))
}

func extractPath(err error) string {
	return err.Error()
}

func normalize(doc fileDocument, data []byte) (Config, error) {
	if doc.ConfigVersion != 1 {
		return Config{}, newValidationError("config_invalid_value", Diagnostic{Code: "config_invalid_version", Path: "config_version"})
	}
	if err := expandUserPaths(&doc); err != nil {
		return Config{}, err
	}
	if err := validateInstance(&doc.Instance); err != nil {
		return Config{}, err
	}
	if err := validateServer(&doc.Server); err != nil {
		return Config{}, err
	}
	if err := validateJobs(&doc.Jobs); err != nil {
		return Config{}, err
	}
	if err := validateStorage(&doc.Storage); err != nil {
		return Config{}, err
	}
	processing, err := buildProcessingProvider(doc.Processing)
	if err != nil {
		return Config{}, err
	}
	embedding, err := buildEmbeddingProvider(doc.Embedding)
	if err != nil {
		return Config{}, err
	}
	consolidation, err := buildConsolidationConfig(doc.Consolidation)
	if err != nil {
		return Config{}, err
	}
	semantic, err := buildSemanticConfig(doc.Semantic)
	if err != nil {
		return Config{}, err
	}
	graph, err := buildGraphConfig(doc.Graph)
	if err != nil {
		return Config{}, err
	}
	ranking, err := buildRankingConfig(doc.Ranking)
	if err != nil {
		return Config{}, err
	}
	contextCfg, err := buildContextConfig(doc.Context)
	if err != nil {
		return Config{}, err
	}
	graphEnabled := true
	if doc.Processing.GraphEnabled != nil {
		graphEnabled = *doc.Processing.GraphEnabled
	}
	listenAddress := doc.Server.ListenAddress
	if listenAddress == "" {
		listenAddress = DefaultListenAddress
	}
	cfg := Config{
		ConfigVersion:       doc.ConfigVersion,
		DataDir:             doc.Instance.DataDir,
		DatabaseFilename:    doc.Instance.DatabaseFilename,
		DatabasePath:        filepath.Join(doc.Instance.DataDir, doc.Instance.DatabaseFilename),
		ShutdownGracePeriod: parseDuration(doc.Server.ShutdownGracePeriod),
		ListenAddress:       listenAddress,
		TLSCertFile:         doc.Server.TLSCertFile,
		TLSKeyFile:          doc.Server.TLSKeyFile,
		WorkerCount:         doc.Jobs.WorkerCount,
		QueueSoftLimit:      doc.Jobs.QueueSoftLimit,
		QueueHardLimit:      doc.Jobs.QueueHardLimit,
		MaxAttempts:         doc.Jobs.MaxAttempts,
		LeaseDuration:       parseDuration(doc.Jobs.LeaseDuration),
		PerJobTimeout:       parseDuration(doc.Jobs.PerJobTimeout),
		RetryInitialBackoff: parseDuration(doc.Jobs.RetryInitialBackoff),
		RetryMaxBackoff:     parseDuration(doc.Jobs.RetryMaxBackoff),
		DiskSoftFree:        doc.Storage.DiskSoftFreeBytes,
		DiskHardFree:        doc.Storage.DiskHardFreeBytes,
		ProcessingVersion:   ProcessingVersionFor(processing),
		ProcessingProvider:  processing,
		EmbeddingProvider:   embedding,
		GraphEnabled:        graphEnabled,
		Consolidation:       consolidation,
		Semantic:            semantic,
		Graph:               graph,
		Ranking:             ranking,
		Context:             contextCfg,
	}
	cfg.SourceIdentity = sourceIdentity(data)
	cfg.EffectiveIdentity = effectiveIdentity(cfg)
	return cfg, nil
}

// expandUserPaths resolves the portable ${HOME} marker used by the packaged
// default configuration. SourceIdentity still identifies the file bytes,
// while EffectiveIdentity identifies the resolved runtime configuration.
func expandUserPaths(doc *fileDocument) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return newValidationError("config_home_unavailable", Diagnostic{Code: "config_home_unavailable", Path: "instance.data_dir"})
	}
	expand := func(value string) string {
		return strings.ReplaceAll(value, "${HOME}", filepath.Clean(home))
	}
	doc.Instance.DataDir = expand(doc.Instance.DataDir)
	doc.Server.TLSCertFile = expand(doc.Server.TLSCertFile)
	doc.Server.TLSKeyFile = expand(doc.Server.TLSKeyFile)
	return nil
}

func validateInstance(t *instanceTable) error {
	if t.DataDir == "" {
		return newValidationError("config_missing_field", Diagnostic{Code: "config_missing_field", Path: "instance.data_dir"})
	}
	if !filepath.IsAbs(t.DataDir) {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_relative_path", Path: "instance.data_dir"})
	}
	if t.DatabaseFilename == "" || filepath.Dir(t.DatabaseFilename) != "." {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_not_basename", Path: "instance.database_filename"})
	}
	return nil
}

func validateServer(t *serverTable) error {
	if t.ShutdownGracePeriod == "" {
		return newValidationError("config_missing_field", Diagnostic{Code: "config_missing_field", Path: "server.shutdown_grace_period"})
	}
	d := parseDuration(t.ShutdownGracePeriod)
	if d == 0 {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_invalid_duration", Path: "server.shutdown_grace_period"})
	}
	address := t.ListenAddress
	if address == "" {
		address = DefaultListenAddress
	}
	if err := validateListenAddress(address); err != nil {
		return err
	}
	if (t.TLSCertFile == "") != (t.TLSKeyFile == "") {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_tls_pair_required", Path: "server.tls_cert_file"})
	}
	for _, file := range []struct {
		path  string
		field string
	}{{t.TLSCertFile, "server.tls_cert_file"}, {t.TLSKeyFile, "server.tls_key_file"}} {
		if file.path != "" && !filepath.IsAbs(file.path) {
			return newValidationError("config_invalid_value", Diagnostic{Code: "config_relative_path", Path: file.field})
		}
	}
	return nil
}

func validateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_invalid_address", Path: "server.listen_address"})
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_invalid_address", Path: "server.listen_address"})
	}
	if host != "" && net.ParseIP(host) == nil && host != "localhost" {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_invalid_address", Path: "server.listen_address"})
	}
	return nil
}

func validateJobs(t *jobsTable) error {
	if t.WorkerCount < 1 || t.WorkerCount > 128 {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.worker_count"})
	}
	if t.QueueSoftLimit < 1 || t.QueueSoftLimit >= t.QueueHardLimit {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.queue_soft_limit"})
	}
	if t.QueueHardLimit < 1 || t.QueueHardLimit > 1000000 {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.queue_hard_limit"})
	}
	if t.MaxAttempts < 1 || t.MaxAttempts > 20 {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.max_attempts"})
	}
	leaseDur := parseDuration(t.LeaseDuration)
	if leaseDur < 5*time.Second || leaseDur > 10*time.Minute {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.lease_duration"})
	}
	perJobDur := parseDuration(t.PerJobTimeout)
	if perJobDur < leaseDur {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.per_job_timeout"})
	}
	retryInit := parseDuration(t.RetryInitialBackoff)
	if retryInit <= 0 {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.retry_initial_backoff"})
	}
	retryMax := parseDuration(t.RetryMaxBackoff)
	if retryMax < retryInit {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "jobs.retry_max_backoff"})
	}
	return nil
}

func validateStorage(t *storageTable) error {
	if t.DiskSoftFreeBytes == 0 || t.DiskHardFreeBytes == 0 || t.DiskHardFreeBytes >= t.DiskSoftFreeBytes {
		return newValidationError("config_invalid_value", Diagnostic{Code: "config_out_of_bounds", Path: "storage"})
	}
	return nil
}

func buildProcessingProvider(t processingTable) (provider.Config, error) {
	enabled := t.Enabled != nil && *t.Enabled
	pcfg := provider.Config{
		Enabled:           enabled,
		Kind:              t.Provider.Kind,
		Endpoint:          t.Provider.Endpoint,
		Model:             t.Provider.Model,
		TelemetryDisabled: t.Provider.TelemetryDisabled,
		Secret: provider.SecretReference{
			Source: t.Provider.Secret.Source,
			Name:   t.Provider.Secret.Name,
		},
		Limits: provider.Limits{
			MaxEgressInputChars:    t.Provider.Limits.MaxEgressInputChars,
			MaxAggregateInputChars: t.Provider.Limits.MaxAggregateInputChars,
			MaxContextItems:        t.Provider.Limits.MaxContextItems,
			MaxCandidateChars:      t.Provider.Limits.MaxCandidateChars,
			MaxTotalCandidateChars: t.Provider.Limits.MaxTotalCandidateChars,
			MaxResponseBytes:       t.Provider.Limits.MaxResponseBytes,
			RequestTimeout:         parseDuration(t.Provider.Limits.RequestTimeout),
			MaxConcurrent:          t.Provider.Limits.MaxConcurrent,
			RequestsPerMinute:      t.Provider.Limits.RequestsPerMinute,
		},
		Policy: provider.Policy{
			Default:       t.Provider.Policy.Default,
			ContentTypes:  t.Provider.Policy.ContentTypes,
			SourceTypes:   t.Provider.Policy.SourceTypes,
			Sensitivities: t.Provider.Policy.Sensitivities,
			Assignments:   buildAssignments(t.Provider.Policy.Assignments),
		},
	}
	if pcfg.Enabled {
		if err := validateProviderEnabled(pcfg, true); err != nil {
			return provider.Config{}, err
		}
	}
	return pcfg, nil
}

func buildEmbeddingProvider(t embeddingTable) (provider.Config, error) {
	enabled := t.Enabled != nil && *t.Enabled
	pcfg := provider.Config{
		Enabled:           enabled,
		Kind:              t.Provider.Kind,
		Endpoint:          t.Provider.Endpoint,
		Model:             t.Provider.Model,
		TelemetryDisabled: t.Provider.TelemetryDisabled,
		Secret: provider.SecretReference{
			Source: t.Provider.Secret.Source,
			Name:   t.Provider.Secret.Name,
		},
		Options: provider.EmbeddingOptions{
			Dimensions:        t.Provider.Options.Dimensions,
			QueryInstructions: t.Provider.Options.QueryInstructions,
		},
		Limits: provider.Limits{
			MaxEgressInputChars: t.Provider.Limits.MaxEgressInputChars,
			MaxResponseBytes:    t.Provider.Limits.MaxResponseBytes,
			RequestTimeout:      parseDuration(t.Provider.Limits.RequestTimeout),
			MaxConcurrent:       t.Provider.Limits.MaxConcurrent,
			RequestsPerMinute:   t.Provider.Limits.RequestsPerMinute,
		},
		Policy: provider.Policy{
			Default:       t.Provider.Policy.Default,
			Query:         t.Provider.Policy.Query,
			ContentTypes:  t.Provider.Policy.ContentTypes,
			SourceTypes:   t.Provider.Policy.SourceTypes,
			Sensitivities: t.Provider.Policy.Sensitivities,
			Assignments:   buildAssignments(t.Provider.Policy.Assignments),
		},
	}
	if pcfg.Enabled {
		if err := validateProviderEnabled(pcfg, false); err != nil {
			return provider.Config{}, err
		}
	}
	return pcfg, nil
}

func buildAssignments(tables []assignmentTable) []provider.PolicyAssignment {
	result := make([]provider.PolicyAssignment, 0, len(tables))
	for _, t := range tables {
		result = append(result, provider.PolicyAssignment{
			Selector:           t.Selector,
			SpaceID:            t.SpaceID,
			SpaceType:          t.SpaceType,
			ExternalID:         t.ExternalID,
			Effect:             t.Effect,
			IncludeDescendants: t.IncludeDescendants,
			Priority:           t.Priority,
		})
	}
	sort.Slice(result, func(i, j int) bool { return assignmentLess(result[i], result[j]) })
	return result
}

func validateProviderEnabled(pcfg provider.Config, isProcessing bool) error {
	if err := provider.ValidateConfig(pcfg); err != nil {
		return newValidationError("config_invalid_value", Diagnostic{Code: "provider_invalid", Path: "provider"})
	}
	if pcfg.Limits.RequestTimeout < time.Second {
		return newValidationError("config_invalid_value", Diagnostic{Code: "provider_timeout_too_short", Path: "provider.limits.request_timeout"})
	}
	if isProcessing && (pcfg.Limits.MaxAggregateInputChars < 1 || pcfg.Limits.MaxCandidateChars < 1 || pcfg.Limits.MaxTotalCandidateChars < pcfg.Limits.MaxCandidateChars) {
		return newValidationError("config_invalid_value", Diagnostic{Code: "provider_candidate_bounds", Path: "processing.provider.limits"})
	}
	if err := validatePolicy(pcfg.Policy, isProcessing); err != nil {
		return err
	}
	return nil
}

func validatePolicy(policy provider.Policy, isProcessing bool) error {
	if policy.Default != "deny" {
		return newValidationError("config_invalid_value", Diagnostic{Code: "policy_default_must_deny", Path: "provider.policy.default"})
	}
	if !isProcessing && policy.Query != "allow" && policy.Query != "deny" {
		return newValidationError("config_invalid_value", Diagnostic{Code: "policy_invalid_query", Path: "embedding.provider.policy.query"})
	}
	if len(policy.Assignments) == 0 {
		return newValidationError("config_missing_field", Diagnostic{Code: "policy_assignment_required", Path: "provider.policy.assignments"})
	}
	for _, assignment := range policy.Assignments {
		if assignment.Effect != "allow" && assignment.Effect != "deny" {
			return newValidationError("config_invalid_value", Diagnostic{Code: "policy_invalid_effect", Path: "provider.policy.assignments.effect"})
		}
		switch assignment.Selector {
		case "space":
			if assignment.SpaceID == "" || assignment.SpaceType != "" || assignment.ExternalID != "" {
				return newValidationError("config_invalid_value", Diagnostic{Code: "policy_invalid_selector", Path: "provider.policy.assignments"})
			}
		case "space_type":
			if assignment.SpaceType == "" || assignment.SpaceID != "" || assignment.ExternalID != "" || assignment.IncludeDescendants {
				return newValidationError("config_invalid_value", Diagnostic{Code: "policy_invalid_selector", Path: "provider.policy.assignments"})
			}
		case "external_id":
			if assignment.ExternalID == "" || assignment.SpaceID != "" || assignment.SpaceType != "" || assignment.IncludeDescendants {
				return newValidationError("config_invalid_value", Diagnostic{Code: "policy_invalid_selector", Path: "provider.policy.assignments"})
			}
		default:
			return newValidationError("config_invalid_value", Diagnostic{Code: "policy_invalid_selector", Path: "provider.policy.assignments.selector"})
		}
	}
	return nil
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

func normalizeMIME(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return value
	}
	return strings.TrimSpace(parts[0]) + "/" + strings.TrimSpace(parts[1])
}

func normalizeMIMEs(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, v := range values {
		n := normalizeMIME(v)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		result = append(result, n)
	}
	return result
}

func normalizeSet(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	return result
}

func assignmentLess(a, b provider.PolicyAssignment) bool {
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

var _ = fmt.Sprintf
