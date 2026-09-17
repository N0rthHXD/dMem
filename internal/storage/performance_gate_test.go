package storage

import (
	"os"
	"testing"
)

// requirePerformanceGates keeps wall-clock and allocation regression gates out
// of the default and race suites. Run them explicitly with DMEM_PERFORMANCE=1.
func requirePerformanceGates(t *testing.T) {
	t.Helper()
	if os.Getenv("DMEM_PERFORMANCE") != "1" {
		t.Skip("performance gate; set DMEM_PERFORMANCE=1 to run")
	}
}
