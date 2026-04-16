package main

import (
	"bytes"
	"testing"

	"github.com/getnenai/dexbox/internal/desktop"
)

// runRDPAdd constructs a fresh `dexbox rdp add` command, points the connection
// store at a tmp HOME, and invokes it with the given args. It returns the
// persisted RDPConfig for the added name so individual cases can assert on
// the fields that were threaded through from the flags.
//
// This mirrors how `--ignore-cert` and other flags are exercised: execute the
// command and inspect what ended up in the JSON-backed store on disk.
func runRDPAdd(t *testing.T, name string, extraArgs ...string) desktop.RDPConfig {
	t.Helper()

	// Isolate the connection store to a per-test temp dir. DefaultStorePath
	// resolves to ~/.dexbox/connections.json via os.UserHomeDir, which honors
	// $HOME on Unix and %USERPROFILE% on Windows.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	cmd := cmdRDPAdd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	args := append([]string{name,
		"--host", "10.0.0.1",
		"--user", "Administrator",
		"--pass", "secret",
	}, extraArgs...)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("rdp add failed: %v (stderr=%q)", err, stderr.String())
	}

	store := desktop.NewConnectionStore(desktop.DefaultStorePath())
	if err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	cfg, ok := store.Get(name)
	if !ok {
		t.Fatalf("connection %q not persisted", name)
	}
	return cfg
}

func TestRDPAdd_DriveDefaultsOff(t *testing.T) {
	cfg := runRDPAdd(t, "no-drive")
	if cfg.DriveEnabled {
		t.Errorf("DriveEnabled: got true, want false by default")
	}
	if cfg.DriveName != "" {
		t.Errorf("DriveName: got %q, want empty by default", cfg.DriveName)
	}
}

func TestRDPAdd_DriveNameEnablesRedirection(t *testing.T) {
	cfg := runRDPAdd(t, "drive-foo", "--drive-name", "Foo")
	if !cfg.DriveEnabled {
		t.Errorf("DriveEnabled: got false, want true (non-empty drive-name should auto-enable)")
	}
	if cfg.DriveName != "Foo" {
		t.Errorf("DriveName: got %q, want %q", cfg.DriveName, "Foo")
	}
}
