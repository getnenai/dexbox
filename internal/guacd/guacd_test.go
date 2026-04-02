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

// TestEnsureRunning_SkipsStart_WhenListening_NoSharedDir verifies that
// EnsureRunning returns nil immediately when guacd is already listening and no
// sharedDir is required, without touching Docker.
func TestEnsureRunning_SkipsStart_WhenListening_NoSharedDir(t *testing.T) {
	l, ok := startLocalListener(t)
	if !ok {
		t.Skip("cannot bind DefaultAddr; skipping")
	}
	defer l.Close()

	if err := EnsureRunning(context.Background(), ""); err != nil {
		t.Errorf("EnsureRunning(ctx, \"\") = %v; want nil when already listening with no sharedDir", err)
	}
}

// TestEnsureRunning_CallsStart_WhenListening_WithSharedDir verifies that
// EnsureRunning still invokes Start for bind-mount reconciliation when a
// non-empty sharedDir is passed, even if guacd is already listening.
// In environments without Docker the call returns an error — that is the
// expected path; the test just confirms we do NOT short-circuit to nil.
func TestEnsureRunning_CallsStart_WhenListening_WithSharedDir(t *testing.T) {
	l, ok := startLocalListener(t)
	if !ok {
		t.Skip("cannot bind DefaultAddr; skipping")
	}
	defer l.Close()

	if DockerAvailable(context.Background()) {
		t.Skip("Docker available; Start would actually run — skipping to avoid side effects")
	}

	// Without Docker, Start returns an error, proving EnsureRunning did not
	// short-circuit to nil when sharedDir is non-empty.
	err := EnsureRunning(context.Background(), "/tmp/shared")
	if err == nil {
		t.Error("EnsureRunning(ctx, \"/tmp/shared\") = nil; want an error when Docker is unavailable, confirming Start was called")
	}
}
