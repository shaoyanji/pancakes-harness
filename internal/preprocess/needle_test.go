package preprocess

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubBridge writes an executable POSIX shell script and returns its path. The
// adapter is exercised through /bin/sh, so the suite proves the process
// protocol without needing python or the weights on the test machine.
func stubBridge(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub bridge is POSIX-only")
	}
	path := filepath.Join(t.TempDir(), "bridge.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func newStubAdapter(t *testing.T, bridge string) *NeedleAdapter {
	t.Helper()
	adapter, err := NewNeedleAdapter(NeedleConfig{Python: "/bin/sh", Bridge: bridge, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewNeedleAdapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

func TestNeedleAdapterRequiresBridge(t *testing.T) {
	t.Setenv("NEEDLE_BRIDGE", "")
	if _, err := NewNeedleAdapter(NeedleConfig{}); err == nil {
		t.Fatal("expected an error when no bridge is configured")
	}
	if _, err := NewNeedleAdapter(NeedleConfig{Bridge: filepath.Join(t.TempDir(), "missing.py")}); err == nil {
		t.Fatal("expected an error when the bridge does not exist")
	}
}

func TestNeedleAdapterCall(t *testing.T) {
	bridge := stubBridge(t, `while read -r line; do
  printf '%s\n' '{"ok":true,"id":"r1","extraction":{"intent":"command","intent_confidence":0.91}}'
done
`)
	adapter := newStubAdapter(t, bridge)
	if adapter.Name() != "needle3" {
		t.Fatalf("unexpected adapter name %q", adapter.Name())
	}

	raw, err := adapter.Call(context.Background(), "extract", "run the tests")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	ext, err := parseExtraction(raw)
	if err != nil {
		t.Fatalf("parseExtraction: %v", err)
	}
	if ext.Intent != IntentCommand || ext.IntentConf != 0.91 {
		t.Fatalf("unexpected extraction: %+v", ext)
	}

	// The process is reused across calls; a second call must not need a restart.
	if _, err := adapter.Call(context.Background(), "extract", "run the tests again"); err != nil {
		t.Fatalf("second Call: %v", err)
	}
}

func TestNeedleAdapterBridgeError(t *testing.T) {
	bridge := stubBridge(t, `while read -r line; do
  printf '%s\n' '{"ok":false,"error":"weights missing"}'
done
`)
	adapter := newStubAdapter(t, bridge)
	_, err := adapter.Call(context.Background(), "", "anything")
	if err == nil || !strings.Contains(err.Error(), "weights missing") {
		t.Fatalf("expected the bridge error to surface, got %v", err)
	}
}

func TestNeedleAdapterRestartsAfterCrash(t *testing.T) {
	bridge := stubBridge(t, "read -r line\nexit 1\n")
	adapter := newStubAdapter(t, bridge)

	if _, err := adapter.Call(context.Background(), "", "first"); err == nil {
		t.Fatal("expected the first call to fail on a bridge that exits")
	}

	// Replace the stub body: the next call has to start a fresh process.
	if err := os.WriteFile(bridge, []byte(`#!/bin/sh
while read -r line; do
  printf '%s\n' '{"ok":true,"extraction":{"intent":"question","intent_confidence":0.7}}'
done
`), 0o755); err != nil {
		t.Fatalf("rewrite stub: %v", err)
	}

	raw, err := adapter.Call(context.Background(), "", "second")
	if err != nil {
		t.Fatalf("expected a restart to recover, got %v", err)
	}
	if _, err := parseExtraction(raw); err != nil {
		t.Fatalf("parseExtraction: %v", err)
	}
}

func TestNeedleAdapterTimeout(t *testing.T) {
	bridge := stubBridge(t, "sleep 5\n")
	adapter, err := NewNeedleAdapter(NeedleConfig{Python: "/bin/sh", Bridge: bridge, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewNeedleAdapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	started := time.Now()
	if _, err := adapter.Call(context.Background(), "", "slow"); err == nil {
		t.Fatal("expected a timeout")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s, expected it to cut the call short", elapsed)
	}
}
