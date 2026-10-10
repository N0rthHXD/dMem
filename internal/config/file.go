package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxConfigFileSize = 1 << 20

type fileDocument struct {
	ConfigVersion int                `toml:"config_version"`
	Instance      instanceTable      `toml:"instance"`
	Server        serverTable        `toml:"server"`
	Jobs          jobsTable          `toml:"jobs"`
	Storage       storageTable       `toml:"storage"`
	Processing    processingTable    `toml:"processing"`
	Embedding     embeddingTable     `toml:"embedding"`
	Consolidation consolidationTable `toml:"consolidation"`
	Semantic      semanticTable      `toml:"semantic"`
	Graph         graphTable         `toml:"graph"`
	Ranking       rankingTable       `toml:"ranking"`
	Context       contextTable       `toml:"context"`
}

type consolidationTable struct {
	Version               *string  `toml:"version"`
	MaxCandidates         *int     `toml:"max_candidates"`
	ExactHashEnabled      *bool    `toml:"exact_hash_enabled"`
	SameSourceEventCosine *float64 `toml:"same_source_event_cosine"`
	SameSubjectKindCosine *float64 `toml:"same_subject_kind_cosine"`
}

type semanticTable struct {
	CalibrationVersion *string  `toml:"calibration_version"`
	AbsoluteFloor      *float64 `toml:"absolute_floor"`
	TopMargin          *float64 `toml:"top_margin"`
}

type graphTable struct {
	Policy              *string  `toml:"policy"`
	MinEntityConfidence *float64 `toml:"min_entity_confidence"`
	MinFactConfidence   *float64 `toml:"min_fact_confidence"`
	BoostCap            *float64 `toml:"boost_cap"`
}

type rankingTable struct {
	Version         *string  `toml:"version"`
	SemanticWeight  *float64 `toml:"semantic_weight"`
	KeywordWeight   *float64 `toml:"keyword_weight"`
	ExactWeight     *float64 `toml:"exact_weight"`
	TemporalWeight  *float64 `toml:"temporal_weight"`
	QualityWeight   *float64 `toml:"quality_weight"`
	AgreementWeight *float64 `toml:"agreement_weight"`
}

type contextTable struct {
	MaxContextTokens *int `toml:"max_context_tokens"`
	MaxContextChars  *int `toml:"max_context_chars"`
	MaxResults       *int `toml:"max_results"`
}

type instanceTable struct {
	DataDir          string `toml:"data_dir"`
	DatabaseFilename string `toml:"database_filename"`
}

type serverTable struct {
	ListenAddress       string `toml:"listen_address"`
	TLSCertFile         string `toml:"tls_cert_file"`
	TLSKeyFile          string `toml:"tls_key_file"`
	ShutdownGracePeriod string `toml:"shutdown_grace_period"`
}

type jobsTable struct {
	WorkerCount         int    `toml:"worker_count"`
	QueueSoftLimit      int    `toml:"queue_soft_limit"`
	QueueHardLimit      int    `toml:"queue_hard_limit"`
	MaxAttempts         int    `toml:"max_attempts"`
	LeaseDuration       string `toml:"lease_duration"`
	PerJobTimeout       string `toml:"per_job_timeout"`
	RetryInitialBackoff string `toml:"retry_initial_backoff"`
	RetryMaxBackoff     string `toml:"retry_max_backoff"`
}

type storageTable struct {
	DiskSoftFreeBytes uint64 `toml:"disk_soft_free_bytes"`
	DiskHardFreeBytes uint64 `toml:"disk_hard_free_bytes"`
}

type processingTable struct {
	Enabled      *bool                   `toml:"enabled"`
	GraphEnabled *bool                   `toml:"graph_enabled"`
	Provider     processingProviderTable `toml:"provider"`
}

type embeddingTable struct {
	Enabled  *bool                  `toml:"enabled"`
	Provider embeddingProviderTable `toml:"provider"`
}

type processingProviderTable struct {
	Kind              string                `toml:"kind"`
	Endpoint          string                `toml:"endpoint"`
	Model             string                `toml:"model"`
	TelemetryDisabled bool                  `toml:"telemetry_disabled"`
	Secret            secretTable           `toml:"secret"`
	Limits            processingLimitsTable `toml:"limits"`
	Policy            processingPolicyTable `toml:"policy"`
}

type embeddingProviderTable struct {
	Kind              string               `toml:"kind"`
	Endpoint          string               `toml:"endpoint"`
	Model             string               `toml:"model"`
	TelemetryDisabled bool                 `toml:"telemetry_disabled"`
	Secret            secretTable          `toml:"secret"`
	Options           optionsTable         `toml:"options"`
	Limits            embeddingLimitsTable `toml:"limits"`
	Policy            embeddingPolicyTable `toml:"policy"`
}

type secretTable struct {
	Source string `toml:"source"`
	Name   string `toml:"name"`
}

type optionsTable struct {
	Dimensions        int    `toml:"dimensions"`
	QueryInstructions string `toml:"query_instructions"`
}

type processingLimitsTable struct {
	MaxEgressInputChars    int    `toml:"max_egress_input_chars"`
	MaxAggregateInputChars int    `toml:"max_aggregate_input_chars"`
	MaxContextItems        int    `toml:"max_context_items"`
	MaxCandidateChars      int    `toml:"max_candidate_chars"`
	MaxTotalCandidateChars int    `toml:"max_total_candidate_chars"`
	MaxResponseBytes       int64  `toml:"max_response_bytes"`
	RequestTimeout         string `toml:"request_timeout"`
	MaxConcurrent          int    `toml:"max_concurrent"`
	RequestsPerMinute      int    `toml:"requests_per_minute"`
}

type embeddingLimitsTable struct {
	MaxEgressInputChars int    `toml:"max_egress_input_chars"`
	MaxResponseBytes    int64  `toml:"max_response_bytes"`
	RequestTimeout      string `toml:"request_timeout"`
	MaxConcurrent       int    `toml:"max_concurrent"`
	RequestsPerMinute   int    `toml:"requests_per_minute"`
}

type processingPolicyTable struct {
	Default       string            `toml:"default"`
	ContentTypes  []string          `toml:"content_types"`
	SourceTypes   []string          `toml:"source_types"`
	Sensitivities []string          `toml:"sensitivities"`
	Assignments   []assignmentTable `toml:"assignments"`
}

type embeddingPolicyTable struct {
	Default       string            `toml:"default"`
	Query         string            `toml:"query"`
	ContentTypes  []string          `toml:"content_types"`
	SourceTypes   []string          `toml:"source_types"`
	Sensitivities []string          `toml:"sensitivities"`
	Assignments   []assignmentTable `toml:"assignments"`
}

type assignmentTable struct {
	Selector           string `toml:"selector"`
	SpaceID            string `toml:"space_id"`
	SpaceType          string `toml:"space_type"`
	ExternalID         string `toml:"external_id"`
	Effect             string `toml:"effect"`
	IncludeDescendants bool   `toml:"include_descendants"`
	Priority           int    `toml:"priority"`
}

type Diagnostic struct {
	Code   string
	Path   string
	Line   int
	Column int
}

type ValidationError struct {
	Code        string
	Diagnostics []Diagnostic
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Diagnostics) > 0 {
		d := e.Diagnostics[0]
		return fmt.Sprintf("config: %s at %s", e.Code, d.Path)
	}
	return "config: " + e.Code
}

func newValidationError(code string, diagnostics ...Diagnostic) *ValidationError {
	return &ValidationError{Code: code, Diagnostics: diagnostics}
}

func readFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, newValidationError("config_unavailable", Diagnostic{Code: "config_unavailable", Path: path})
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, newValidationError("config_unavailable", Diagnostic{Code: "config_unavailable", Path: path})
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, newValidationError("config_unavailable", Diagnostic{Code: "config_unavailable", Path: path})
	}
	if filepath.Clean(resolved) != filepath.Clean(path) {
		return nil, newValidationError("config_unavailable", Diagnostic{Code: "config_unavailable", Path: path})
	}
	if info.Size() > maxConfigFileSize {
		return nil, newValidationError("config_too_large", Diagnostic{Code: "config_too_large", Path: path})
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, newValidationError("config_unavailable", Diagnostic{Code: "config_unavailable", Path: path})
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() {
		return nil, newValidationError("config_unavailable", Diagnostic{Code: "config_unavailable", Path: path})
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxConfigFileSize {
		return nil, newValidationError("config_too_large", Diagnostic{Code: "config_too_large", Path: path})
	}
	return data, nil
}

func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "dmem", "config.toml"), nil
}
