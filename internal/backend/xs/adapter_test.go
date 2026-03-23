package xs

import (
	"context"
	"errors"
	"testing"
)

func TestHealthCheckDiagnosticsAreClearOnFailure(t *testing.T) {
	t.Parallel()

	t.Run("bad config", func(t *testing.T) {
		t.Parallel()
		a := NewAdapter(Config{})
		status := a.HealthCheck(context.Background())
		if status.OK {
			t.Fatal("expected unhealthy status")
		}
		if len(status.Diagnostics) == 0 || status.Diagnostics[0].Code != "bad_config" {
			t.Fatalf("expected bad_config diagnostic, got %#v", status.Diagnostics)
		}
	})

	t.Run("xs unavailable", func(t *testing.T) {
		t.Parallel()
		a := NewAdapter(Config{Command: "xs-definitely-missing-binary-12345"})
		status := a.HealthCheck(context.Background())
		if status.OK {
			t.Fatal("expected unhealthy status")
		}
		if len(status.Diagnostics) == 0 || status.Diagnostics[0].Code != "xs_unavailable" {
			t.Fatalf("expected xs_unavailable diagnostic, got %#v", status.Diagnostics)
		}
	})

	t.Run("health check failed", func(t *testing.T) {
		t.Parallel()
		a := NewAdapter(
			Config{Command: "sh", HealthArgs: []string{"-c", "echo ok"}},
			WithCommandRunner(func(ctx context.Context, command string, args ...string) ([]byte, error) {
				return nil, errors.New("probe failed")
			}),
		)
		status := a.HealthCheck(context.Background())
		if status.OK {
			t.Fatal("expected unhealthy status")
		}
		if len(status.Diagnostics) == 0 || status.Diagnostics[0].Code != "health_check_failed" {
			t.Fatalf("expected health_check_failed diagnostic, got %#v", status.Diagnostics)
		}
		last := a.LastDiagnostics()
		if len(last) == 0 || last[0].Code != "health_check_failed" {
			t.Fatalf("expected retained diagnostic, got %#v", last)
		}
		a.ClearDiagnostics()
		if len(a.LastDiagnostics()) != 0 {
			t.Fatalf("expected cleared diagnostics, got %#v", a.LastDiagnostics())
		}
	})
}
