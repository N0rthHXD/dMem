package provider

import (
	"os"
)

func resolveSecret(ref SecretReference) string {
	if ref.Source != "environment" || ref.Name == "" {
		return ""
	}
	return os.Getenv(ref.Name)
}

func SecretAvailable(ref SecretReference) bool {
	return resolveSecret(ref) != ""
}

// ProcessorIdentity returns the stable processor identity of a configured
// provider. It is empty when the provider is disabled, so a disabled provider
// never contributes a model identity to provenance.
func ProcessorIdentity(cfg Config) string {
	if !cfg.Enabled {
		return ""
	}
	return KindOpenAICompatible + "/" + cfg.Model
}
