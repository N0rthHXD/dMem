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
