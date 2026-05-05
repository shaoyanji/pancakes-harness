package backend_test

import (
	"testing"

	"pancakes-harness/internal/backend"
)

// TestMemoryBackendCompliance runs the full compliance suite against the memory backend.
func TestMemoryBackendCompliance(t *testing.T) {
	BackendComplianceTest(t, func(tb testing.TB) backend.Backend {
		return backend.NewMemoryBackend()
	})
}
