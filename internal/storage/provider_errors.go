package storage

import (
	"errors"
	"fmt"

	"github.com/N0rthHXD/dMem/internal/provider"
)

// ClassifyProviderError maps a provider call failure onto the storage failure
// class the worker uses to choose between blocking, retrying, and terminating a
// job. Policy rejections are returned with the caller-supplied policy sentinel so
// embedding and aggregate work keep their distinct blocking reasons. An error
// that is not a provider error is returned unchanged.
func ClassifyProviderError(err error, policy error) error {
	if err == nil {
		return nil
	}
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		return err
	}
	code := providerErr.Code()
	switch providerErr.Class() {
	case provider.ErrorPolicy:
		return fmt.Errorf("%w: %s", policy, code)
	case provider.ErrorInvalidOutput:
		return fmt.Errorf("%w: %s", ErrProviderInvalidOutput, code)
	case provider.ErrorInvalidInput, provider.ErrorPermanent:
		return fmt.Errorf("%w: %s", ErrProviderPermanent, code)
	default:
		// Disabled and transient classes are retryable: configuration or the
		// upstream provider may recover, and the generation fence still guards
		// the commit.
		return fmt.Errorf("%w: %s", ErrProviderTransient, code)
	}
}
