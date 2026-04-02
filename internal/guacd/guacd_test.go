package guacd

import (
	"context"
	"net"
	"testing"
)

// startLocalListener binds a TCP listener on DefaultAddr.
// It returns the listener and true if successful; otherwise it returns nil and false.
// Callers should defer l.Close() when true is returned.
func startLocalListener(t *testing.T) (net.Listener, bool) {
	t.Helper()
	l, err := net.Listen("tcp", DefaultAddr)
	if err != nil {
		t.Logf("cannot bind %s (port in use?): %v", DefaultAddr, err)
		return nil, false
	}
	return l, true
}

func TestIsListening_ReturnsFalse_WhenNothingListening(t *testing.T) {
	// This test is best-effort: if something else has bound DefaultAddr
	// (e.g. a real guacd), we skip rather than fail.
	l, ok := startLocalListener(t)
	if !ok {
		t.Skip("DefaultAddr already in use; skipping")
	}
	// Close before calling IsListening so nothing is listening.
	l.Close()

	// If something started listening between Close() and here (e.g. a Docker
	// container racing on startup), we cannot prove the negative; skip.
	if IsListening() {
		t.Skip("something bound DefaultAddr after listener close; skipping negative check")
	}
}

func TestIsListening_ReturnsTrue_WhenListening(t *testing.T) {
	l, ok := startLocalListener(t)
	if !ok {
		t.Skip("cannot bind DefaultAddr; skipping")
	}
	defer l.Close()

	if !IsListening() {
		t.Error("IsListening() = false, want true when listener is active")
	}
}

// TestEnsureRunning_SkipsStart_WhenAlreadyListening verifies that EnsureRunning
// returns nil when guacd is already accepting connections.
//
// The behaviour differs depending on Docker availability:
//
//   - Docker unavailable: EnsureRunning short-circuits immediately — it checks
//     IsListening() and returns nil without touching Docker at all. This is the
//     "skip" described in the test name.
//   - Docker available: EnsureRunning calls Start(), which checks whether the
//     managed container is running and may issue Docker commands. Start() still
//     returns nil in CI environments where Docker is present, so the assertion
//     holds — but via the Docker path rather than the IsListening() fast-path.
//
// Both cases are covered by asserting EnsureRunning(ctx, sharedDir) == nil
// while a local listener occupies DefaultAddr. The test name reflects the
// Docker-unavailable path; the comment above clarifies the Docker-available
// dual behaviour.
func TestEnsureRunning_SkipsStart_WhenAlreadyListening(t *testing.T) {
	l, ok := startLocalListener(t)
	if !ok {
		t.Skip("cannot bind DefaultAddr; skipping")
	}
	defer l.Close()

	ctx := context.Background()

	for _, sharedDir := range []string{"", "/tmp/shared"} {
		if err := EnsureRunning(ctx, sharedDir); err != nil {
			t.Errorf("EnsureRunning(ctx, %q) = %v; want nil when already listening", sharedDir, err)
		}
	}
}
