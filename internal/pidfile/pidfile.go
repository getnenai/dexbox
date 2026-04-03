// Package pidfile manages a PID file for a named daemon process.
//
// It provides atomic creation (O_CREATE|O_EXCL), stale-file recovery,
// process-identity verification, and graceful SIGTERM→SIGKILL shutdown.
// The parent directory is created with mode 0700 if absent.
package pidfile

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// File manages a single PID file at a fixed path.
type File struct {
	path string
	name string // expected process name for identity checks (e.g. "dexbox")
}

// New returns a File for the given path and process name.
// The name is used by ProcessName to verify that a running PID actually
// belongs to the expected process before sending signals.
func New(path, name string) *File {
	return &File{path: path, name: name}
}

// Write atomically creates the PID file recording the current process PID.
// It creates the parent directory (mode 0700) if absent, then opens the
// file with O_CREATE|O_EXCL. On EEXIST the existing file is inspected:
// if it contains a live PID whose process name matches f.name, Write returns
// an error indicating the daemon is already running; if the process is dead
// or the name does not match, the stale file is removed and the write is
// retried once. Returns the resolved path so the caller can defer Remove on
// success.
func (f *File) Write() (string, error) {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return "", fmt.Errorf("create PID dir: %w", err)
	}
	content := []byte(strconv.Itoa(os.Getpid()) + "\n")
	for i := 0; i < 2; i++ {
		fh, err := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := fh.Write(content)
			cerr := fh.Close()
			if werr != nil {
				return "", werr
			}
			return f.path, cerr
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		// A file already exists. Check whether it belongs to a live daemon
		// before deciding to remove it.
		if data, readErr := os.ReadFile(f.path); readErr == nil {
			if existingPID, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && existingPID > 0 {
				// On Unix, os.FindProcess never returns an error; liveness is
				// determined by proc.Signal(0).
				proc, _ := os.FindProcess(existingPID)
				if proc.Signal(syscall.Signal(0)) == nil && f.ProcessName(existingPID) {
					return "", fmt.Errorf("PID file %s: process %d is already running", f.path, existingPID)
				}
			}
		}
		// Dead process or unrecognised name — remove the stale file and retry.
		if removeErr := os.Remove(f.path); removeErr != nil {
			return "", fmt.Errorf("PID file %s: could not remove stale copy: %w", f.path, removeErr)
		}
	}
	return "", fmt.Errorf("PID file %s: could not create after removing stale copy", f.path)
}

// Stop reads the PID file, verifies the recorded process is actually named
// f.name (guarding against recycled PIDs), sends SIGTERM, waits up to
// gracePeriod for the process to exit, then escalates to SIGKILL.
// Removes the file regardless of outcome. Returns true if a process was
// found and signalled; false when the file is absent, the PID is gone, or
// the identity check fails.
func (f *File) Stop(gracePeriod time.Duration) bool {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	// On Unix, os.FindProcess never returns an error; liveness is determined
	// by proc.Signal(0).
	proc, _ := os.FindProcess(pid)
	// Signal 0 checks liveness without actually signalling the process.
	if proc.Signal(syscall.Signal(0)) != nil {
		_ = os.Remove(f.path)
		return false
	}
	// Verify the live process is actually the expected daemon before
	// sending any signal. A recycled PID could belong to an unrelated process.
	if !f.ProcessName(pid) {
		_ = os.Remove(f.path)
		return false
	}
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(gracePeriod)
	for time.Now().Before(deadline) {
		if proc.Signal(syscall.Signal(0)) != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Escalate if still alive after the grace period.
	if proc.Signal(syscall.Signal(0)) == nil {
		_ = proc.Kill()
	}
	_ = os.Remove(f.path)
	return true
}

// Remove deletes the PID file. Returns any error from os.Remove except
// ENOENT, which is silently ignored (the file may already be gone).
func (f *File) Remove() error {
	err := os.Remove(f.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ProcessName returns true if the process with the given PID appears to be
// running under a command name that contains f.name (case-insensitive).
// On Linux it reads /proc/<pid>/cmdline; on other platforms it falls back to
// "ps -p <pid> -o comm=" to get the command name.
func (f *File) ProcessName(pid int) bool {
	// Try /proc first (Linux).
	cmdlineFile := fmt.Sprintf("/proc/%d/cmdline", pid)
	if data, err := os.ReadFile(cmdlineFile); err == nil {
		// /proc/<pid>/cmdline is NUL-separated; only the first token is the exe.
		name := strings.ToLower(strings.SplitN(string(data), "\x00", 2)[0])
		return strings.Contains(filepath.Base(name), strings.ToLower(f.name))
	}
	// Fallback: ask ps for the command name (macOS / BSD).
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(string(out))), strings.ToLower(f.name))
}
