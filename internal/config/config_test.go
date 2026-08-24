package config

import (
	"flag"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseResolvesDerivedPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	runtimeDir := filepath.Join(root, "runtime")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg, err := Parse(fs, []string{"-data-dir", dataDir, "-runtime-dir", runtimeDir})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.DatabasePath != filepath.Join(dataDir, "dmem.db") {
		t.Errorf("DatabasePath = %q", cfg.DatabasePath)
	}
	if cfg.Endpoint != filepath.Join(runtimeDir, "dmem.sock") {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
}

func TestParseJobConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg, err := Parse(fs, []string{
		"-data-dir", filepath.Join(root, "data"),
		"-runtime-dir", filepath.Join(root, "runtime"),
		"-lease-duration", "10s",
		"-per-job-timeout", "30s",
		"-retry-initial-backoff", "250ms",
		"-retry-max-backoff", "2s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeaseDuration != 10*time.Second || cfg.PerJobTimeout != 30*time.Second || cfg.RetryInitialBackoff != 250*time.Millisecond || cfg.RetryMaxBackoff != 2*time.Second {
		t.Fatalf("job config = %+v", cfg)
	}
}

func TestParseProcessingVersionPrecedenceAndDefault(t *testing.T) {
	t.Setenv("DMEM_PROCESSING_VERSION", "")
	root := t.TempDir()
	paths := []string{"-data-dir", filepath.Join(root, "data"), "-runtime-dir", filepath.Join(root, "runtime")}
	cfg, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), paths)
	if err != nil || cfg.ProcessingVersion != DefaultProcessingVersion {
		t.Fatalf("default processing version = %q, %v", cfg.ProcessingVersion, err)
	}
	t.Setenv("DMEM_PROCESSING_VERSION", "env-v2")
	cfg, err = Parse(flag.NewFlagSet("test", flag.ContinueOnError), paths)
	if err != nil || cfg.ProcessingVersion != "env-v2" {
		t.Fatalf("environment processing version = %q, %v", cfg.ProcessingVersion, err)
	}
	cfg, err = Parse(flag.NewFlagSet("test", flag.ContinueOnError), append(paths, "-processing-version", "flag-v3"))
	if err != nil || cfg.ProcessingVersion != "flag-v3" {
		t.Fatalf("flag processing version = %q, %v", cfg.ProcessingVersion, err)
	}
}

func TestParseRejectsInvalidProcessingVersion(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"", " leading-space", "version/2", strings.Repeat("v", 129)} {
		_, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), []string{
			"-data-dir", filepath.Join(root, "data"), "-runtime-dir", filepath.Join(root, "runtime"), "-processing-version", version,
		})
		if err == nil {
			t.Fatalf("Parse() accepted processing version %q", version)
		}
	}
}

func TestParseRejectsInvalidJobConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_, err := Parse(fs, []string{
		"-data-dir", filepath.Join(root, "data"),
		"-runtime-dir", filepath.Join(root, "runtime"),
		"-lease-duration", "30s",
		"-per-job-timeout", "10s",
	})
	if err == nil {
		t.Fatal("Parse() accepted per-job timeout shorter than lease duration")
	}
}

func TestParseRejectsSocketOutsideRuntimeDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_, err := Parse(fs, []string{
		"-data-dir", filepath.Join(root, "data"),
		"-runtime-dir", filepath.Join(root, "runtime"),
		"-endpoint", filepath.Join(root, "elsewhere", "dmem.sock"),
	})
	if err == nil {
		t.Fatal("Parse() accepted endpoint outside runtime directory")
	}
}

func TestProvidersDisabledByDefault(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), []string{"-data-dir", filepath.Join(root, "data"), "-runtime-dir", filepath.Join(root, "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProcessingProvider.Enabled || cfg.EmbeddingProvider.Enabled {
		t.Fatal("providers are enabled by default")
	}
}

func TestParseProviderCombinations(t *testing.T) {
	t.Setenv("DMEM_PROCESSING_VERSION", "")
	for _, test := range []struct {
		name                  string
		processing, embedding bool
	}{
		{"neither", false, false},
		{"processing only", true, false},
		{"embedding only", false, true},
		{"both", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			args := []string{"-data-dir", filepath.Join(root, "data"), "-runtime-dir", filepath.Join(root, "runtime")}
			if test.processing {
				args = append(args, "-processing-version", "processing-model-v1")
				args = append(args, providerArgs("processing-provider", "processing-model")...)
			}
			if test.embedding {
				args = append(args, providerArgs("embedding-provider", "embedding-model")...)
			}
			cfg, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), args)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ProcessingProvider.Enabled != test.processing || cfg.EmbeddingProvider.Enabled != test.embedding {
				t.Fatalf("provider states = processing %t, embedding %t", cfg.ProcessingProvider.Enabled, cfg.EmbeddingProvider.Enabled)
			}
			if test.processing && (cfg.ProcessingProvider.Model != "processing-model" || cfg.ProcessingProvider.MaxContextItems != 4) {
				t.Fatalf("processing provider = %+v", cfg.ProcessingProvider)
			}
			if test.embedding && cfg.EmbeddingProvider.Model != "embedding-model" {
				t.Fatalf("embedding provider = %+v", cfg.EmbeddingProvider)
			}
		})
	}
}

func TestProcessingProviderRequiresExplicitNonDefaultVersion(t *testing.T) {
	t.Setenv("DMEM_PROCESSING_VERSION", "")
	root := t.TempDir()
	base := []string{"-data-dir", filepath.Join(root, "data"), "-runtime-dir", filepath.Join(root, "runtime")}
	if _, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), append(base, providerArgs("processing-provider", "model-a")...)); err == nil {
		t.Fatal("Parse() accepted processing provider without explicit processing version")
	}
	args := append(append([]string{}, base...), "-processing-version", DefaultProcessingVersion)
	if _, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), append(args, providerArgs("processing-provider", "model-a")...)); err == nil {
		t.Fatal("Parse() accepted processing provider with explicit default processing version")
	}
	if _, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), append(base, providerArgs("embedding-provider", "model-a")...)); err != nil {
		t.Fatalf("embedding-only provider requires processing version: %v", err)
	}
}

func TestParseRejectsLegacyAndUnsupportedProviderFlags(t *testing.T) {
	root := t.TempDir()
	base := []string{"-data-dir", filepath.Join(root, "data"), "-runtime-dir", filepath.Join(root, "runtime")}
	for _, name := range []string{
		"provider-enabled", "provider-kind", "provider-endpoint", "provider-model", "provider-secret-env",
		"provider-allowed-spaces", "provider-allowed-content-types", "provider-allowed-source-types", "provider-allowed-sensitivities",
		"provider-telemetry-disabled", "provider-egress-policy-version", "provider-max-content-chars", "provider-max-context-items",
		"provider-max-response-size", "provider-request-timeout", "provider-max-concurrent", "provider-requests-per-minute",
		"embedding-provider-max-context-items",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), append(base, "-"+name)); err == nil {
				t.Fatalf("Parse() accepted -%s", name)
			}
		})
	}
}

func TestParseRejectsUnsafeProviderConfiguration(t *testing.T) {
	root := t.TempDir()
	args := []string{"-data-dir", filepath.Join(root, "data"), "-runtime-dir", filepath.Join(root, "runtime")}
	provider := providerArgs("embedding-provider", "model-a")
	for i := range provider {
		if provider[i] == "https://provider.example/v1" {
			provider[i] = "http://provider.example/v1"
		}
	}
	if _, err := Parse(flag.NewFlagSet("test", flag.ContinueOnError), append(args, provider...)); err == nil {
		t.Fatal("Parse() accepted unsafe embedding provider configuration")
	}
}

func providerArgs(prefix, model string) []string {
	args := []string{
		"-" + prefix + "-enabled", "-" + prefix + "-kind", "openai-compatible",
		"-" + prefix + "-endpoint", "https://provider.example/v1", "-" + prefix + "-model", model,
		"-" + prefix + "-secret-env", "DMEM_PROVIDER_API_KEY", "-" + prefix + "-allowed-spaces", "space-a, space-b,space-a",
		"-" + prefix + "-allowed-content-types", "text/plain,application/json", "-" + prefix + "-allowed-source-types", "message,manual",
		"-" + prefix + "-allowed-sensitivities", "normal", "-" + prefix + "-telemetry-disabled",
		"-" + prefix + "-egress-policy-version", "policy-v1", "-" + prefix + "-max-content-chars", "2048",
		"-" + prefix + "-request-timeout", "10s", "-" + prefix + "-max-concurrent", "3", "-" + prefix + "-requests-per-minute", "120",
	}
	if prefix == "processing-provider" {
		args = append(args, "-processing-provider-max-context-items", "4")
	}
	return args
}
