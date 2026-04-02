package pidfile_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/getnenai/dexbox/internal/pidfile"
)

// tempPIDPath returns a path inside a fresh temp directory that is cleaned
// up automatically at the end of the test.
func tempPIDPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.pid")
}

// --- Write -----------------------------------------------------------------

func TestWrite_CreatesFile(t *testing.T) {
	f := pidfile.New(tempPIDPath(t), "test")
	path, err := f.Write()
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read PID file: %v", err)
	}
	pid, err := strconv.Atoi(string(data[:len(data)-1])) // strip newline
	if err != nil {
		t.Fatalf("PID file content is not an integer: %q", data)
	}
	if pid != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), pid)
	}
}

func TestWrite_SetsPermissions(t *testing.T) {
	f := pidfile.New(tempPIDPath(t), "test")
	path, err := f.Write()
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("expected mode 0600, got %04o", got)
	}
}

func TestWrite_FailsWhenLiveProcessHoldsFile(t *testing.T) {
	// The name must match the test binary so that ProcessName returns true,
	// simulating a live daemon that already holds the PID file.
	f := pidfile.New(tempPIDPath(t), "test")
	if _, err := f.Write(); err != nil {
		t.Fatalf("first Write failed: %v", err)
	}
	// Second call should detect a live process and refuse to overwrite.
	_, err := f.Write()
	if err == nil {
		t.Fatal("expected Write to fail when a live process holds the PID file, got nil")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected 'already running' in error, got: %v", err)
	}
}

func TestWrite_RecoversStalePIDFile(t *testing.T) {
	path := tempPIDPath(t)
	// Pre-create a stale file containing a PID that is certainly dead.
	_ = os.WriteFile(path, []byte("99999999\n"), 0o600)

	f := pidfile.New(path, "test")
	_, err := f.Write()
	if err != nil {
		t.Fatalf("Write failed on stale file: %v", err)
	}
}

// --- Remove ----------------------------------------------------------------

func TestRemove_DeletesFile(t *testing.T) {
	f := pidfile.New(tempPIDPath(t), "test")
	path, err := f.Write()
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be gone after Remove")
	}
}

func TestRemove_NoErrorWhenAbsent(t *testing.T) {
	f := pidfile.New(tempPIDPath(t), "test")
	if err := f.Remove(); err != nil {
		t.Fatalf("Remove on absent file: %v", err)
	}
}

// --- Stop ------------------------------------------------------------------

func TestStop_ReturnsFalse_WhenFileAbsent(t *testing.T) {
	f := pidfile.New(tempPIDPath(t), "test")
	// No file written — Stop should return false immediately.
	if f.Stop(0) {
		t.Error("Stop returned true when PID file does not exist")
	}
}

func TestStop_ReturnsFalse_WhenFileHasInvalidContent(t *testing.T) {
	path := tempPIDPath(t)
	_ = os.WriteFile(path, []byte("not-a-pid\n"), 0o600)

	f := pidfile.New(path, "test")
	if f.Stop(0) {
		t.Error("Stop returned true for a PID file with non-numeric content")
	}
}

func TestStop_ReturnsFalse_AndRemovesFile_WhenProcessNameMismatches(t *testing.T) {
	path := tempPIDPath(t)
	// Write our own PID so the process definitely exists and is reachable.
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)

	// Use a name that will never match the test binary.
	f := pidfile.New(path, "zzz-no-such-process-name-zzz")
	if f.Stop(0) {
		t.Error("Stop returned true when process name does not match")
	}
	// The PID file should have been removed after the identity check failed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed after process-name mismatch")
	}
}

func TestProcessName_MatchesCurrentProcess(t *testing.T) {
	// The test binary name contains the package path component "pidfile" so we
	// use a fragment that is guaranteed to appear in os.Executable().
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine test executable name:", err)
	}
	base := filepath.Base(exe)

	f := pidfile.New(tempPIDPath(t), base)
	if !f.ProcessName(os.Getpid()) {
		t.Errorf("ProcessName returned false for own PID with name %q", base)
	}
}

func TestProcessName_ReturnsFalseForDeadPID(t *testing.T) {
	f := pidfile.New(tempPIDPath(t), "test")
	// PID 99999999 almost certainly doesn't exist.
	if f.ProcessName(99999999) {
		t.Error("ProcessName returned true for a PID that should not exist")
	}
}

func TestProcessName_ReturnsFalseForWrongName(t *testing.T) {
	f := pidfile.New(tempPIDPath(t), "zzz-no-such-process-name-zzz")
	// Our own PID exists but the name won't match.
	if f.ProcessName(os.Getpid()) {
		t.Error("ProcessName returned true for wrong process name")
	}
}
