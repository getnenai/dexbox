package pid_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getnenai/dexbox/internal/pid"
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
	f := pid.New(tempPIDPath(t), "test")
	path, err := f.Write()
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read PID file: %v", err)
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("PID file content is not an integer: %q", data)
	}
	if p != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), p)
	}
}

func TestWrite_SetsPermissions(t *testing.T) {
	f := pid.New(tempPIDPath(t), "test")
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
	// Use the actual test-binary basename so ProcessName's exact match
	// recognises this process as the live holder of the PID file.
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine test executable name:", err)
	}
	name := filepath.Base(exe)

	f := pid.New(tempPIDPath(t), name)
	if _, err := f.Write(); err != nil {
		t.Fatalf("first Write failed: %v", err)
	}
	// Second call should detect a live process and refuse to overwrite.
	_, err = f.Write()
	if err == nil {
		t.Fatal("expected Write to fail when a live process holds the PID file, got nil")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected 'already running' in error, got: %v", err)
	}
}

func TestWrite_RecoversStalePIDFile(t *testing.T) {
	// Spawn a subprocess and wait for it to exit so we have a PID that is
	// guaranteed dead by the time Write is called.
	cmd := exec.Command("sleep", "0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start subprocess: %v", err)
	}
	stalePID := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("subprocess exited with error: %v", err)
	}

	path := tempPIDPath(t)
	if err := os.WriteFile(path, []byte(strconv.Itoa(stalePID)+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	f := pid.New(path, "test")
	if _, err := f.Write(); err != nil {
		t.Fatalf("Write failed on stale file: %v", err)
	}
}

// --- Remove ----------------------------------------------------------------

func TestRemove_DeletesFile(t *testing.T) {
	f := pid.New(tempPIDPath(t), "test")
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
	f := pid.New(tempPIDPath(t), "test")
	if err := f.Remove(); err != nil {
		t.Fatalf("Remove on absent file: %v", err)
	}
}

// --- Stop ------------------------------------------------------------------

func TestStop_ReturnsFalse_WhenFileAbsent(t *testing.T) {
	f := pid.New(tempPIDPath(t), "test")
	// No file written — Stop should return false immediately.
	if f.Stop(0) {
		t.Error("Stop returned true when PID file does not exist")
	}
}

func TestStop_ReturnsFalse_WhenFileHasInvalidContent(t *testing.T) {
	path := tempPIDPath(t)
	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	f := pid.New(path, "test")
	if f.Stop(0) {
		t.Error("Stop returned true for a PID file with non-numeric content")
	}
}

func TestStop_ReturnsFalse_AndRemovesFile_WhenProcessNameMismatches(t *testing.T) {
	path := tempPIDPath(t)
	// Write our own PID so the process definitely exists and is reachable.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Use a name that will never match the test binary.
	f := pid.New(path, "zzz-no-such-process-name-zzz")
	if f.Stop(0) {
		t.Error("Stop returned true when process name does not match")
	}
	// The PID file should have been removed after the identity check failed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed after process-name mismatch")
	}
}

func TestStop_ReturnsTrue_AndRemovesFile_WhenProcessTerminated(t *testing.T) {
	// Spawn a long-lived subprocess so we have a real live PID to target.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	// Best-effort kill in case Stop fails; cmd.Wait is called explicitly below
	// to reap the child (Wait may only be called once).
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	path := tempPIDPath(t)
	if err := os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// "sleep" is the exact basename of the subprocess we spawned.
	f := pid.New(path, "sleep")
	if !f.Stop(5 * time.Second) {
		t.Fatal("Stop returned false, expected true for a live matching process")
	}

	// PID file must have been removed.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("expected PID file to be removed after Stop")
	}

	// Reap the child process. Stop already sent at least SIGTERM so Wait
	// should return promptly with a signal-terminated exit error.
	if err := cmd.Wait(); err == nil {
		t.Error("expected subprocess to exit with a signal error, got nil")
	}
}

// --- ProcessName -----------------------------------------------------------

func TestProcessName_MatchesCurrentProcess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine test executable name:", err)
	}
	base := filepath.Base(exe)

	f := pid.New(tempPIDPath(t), base)
	if !f.ProcessName(os.Getpid()) {
		t.Errorf("ProcessName returned false for own PID with name %q", base)
	}
}

func TestProcessName_ReturnsFalseForDeadPID(t *testing.T) {
	f := pid.New(tempPIDPath(t), "test")
	// PID 99999999 almost certainly doesn't exist.
	if f.ProcessName(99999999) {
		t.Error("ProcessName returned true for a PID that should not exist")
	}
}

func TestProcessName_ReturnsFalseForWrongName(t *testing.T) {
	f := pid.New(tempPIDPath(t), "zzz-no-such-process-name-zzz")
	// Our own PID exists but the name won't match.
	if f.ProcessName(os.Getpid()) {
		t.Error("ProcessName returned true for wrong process name")
	}
}
