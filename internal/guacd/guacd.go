// Package guacd manages the guacd RDP proxy. It supports three modes:
//
//  1. Native binary (bundled via Homebrew libexec or found in PATH)
//  2. Docker container (guacamole/guacd:latest)
//  3. External (already listening on DefaultAddr)
package guacd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	ContainerName  = "dexbox-guacd"
	Image          = "guacamole/guacd:latest"
	DefaultPort    = 4822
	DefaultAddr    = "localhost:4822"
	ContainerMount = "/guacd-shared"
)

// Start launches the guacd Docker container. Idempotent: if the container
// is already running with the correct bind mount, this is a no-op. If the
// container exists but is missing the required sharedDir mount, it is
// removed and recreated. If it exists but is stopped, it is restarted.
func Start(ctx context.Context, sharedDir string) error {
	running, err := IsRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		_, err := recreateIfMountMissing(ctx, sharedDir)
		return err
	}

	if containerExists(ctx) {
		if recreated, err := recreateIfMountMissing(ctx, sharedDir); recreated || err != nil {
			return err
		}
		return startExisting(ctx)
	}

	return createAndStart(ctx, sharedDir)
}

// recreateIfMountMissing removes and recreates the guacd container when
// sharedDir is required but the container lacks the bind mount.
// Returns (true, nil) when the container was recreated, (false, nil) when
// no action was needed, and (false, err) on failure.
func recreateIfMountMissing(ctx context.Context, sharedDir string) (recreated bool, err error) {
	if sharedDir == "" || containerHasMount(ctx, sharedDir) {
		return false, nil
	}
	log.Printf("guacd: container %s is missing bind mount for %s; removing and recreating", ContainerName, sharedDir)
	_ = run(ctx, "docker", "rm", "--force", ContainerName)
	return true, createAndStart(ctx, sharedDir)
}

// Stop stops and removes the guacd container.
func Stop(ctx context.Context) error {
	if !containerExists(ctx) {
		return nil
	}
	return run(ctx, "docker", "rm", "--force", ContainerName)
}

// IsRunning reports whether the guacd container is running.
func IsRunning(ctx context.Context) (bool, error) {
	if !DockerAvailable(ctx) {
		return false, fmt.Errorf("docker is not available; install Docker to use RDP connections")
	}
	out, err := output(ctx, "docker", "inspect", "--format", "{{.State.Running}}", ContainerName)
	if err != nil {
		return false, nil // container doesn't exist
	}
	return strings.TrimSpace(out) == "true", nil
}

// IsListening reports whether something is already accepting connections on
// DefaultAddr, regardless of Docker. Used as a fallback when Docker is
// unavailable but guacd was started externally.
func IsListening() bool {
	conn, err := net.DialTimeout("tcp", DefaultAddr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// FindNativeBinary searches for a guacd binary in the following order:
//  1. Homebrew libexec relative to the dexbox binary (../libexec/sbin/guacd)
//  2. guacd in PATH
//
// Returns the absolute path if found, empty string otherwise.
func FindNativeBinary() string {
	// Check Homebrew libexec layout relative to dexbox binary
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "libexec", "sbin", "guacd")
		if abs, err := filepath.Abs(candidate); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}

	// Check PATH
	if path, err := exec.LookPath("guacd"); err == nil {
		return path
	}

	return ""
}

// nativeProcess holds the running guacd subprocess so it can be stopped later.
var nativeProcess *os.Process

// StartNative launches guacd as a native subprocess in the foreground on the
// given port. The process runs in the background and is tracked for cleanup.
func StartNative(ctx context.Context, binaryPath string, port int) error {
	cmd := exec.CommandContext(ctx, binaryPath, "-f", "-b", "127.0.0.1", "-l", fmt.Sprintf("%d", port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start native guacd: %w", err)
	}
	nativeProcess = cmd.Process
	log.Printf("[guacd] started native binary (pid %d) on port %d", cmd.Process.Pid, port)

	// Wait for it to be ready
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			return fmt.Errorf("timeout waiting for native guacd to become ready")
		case <-ticker.C:
			if IsListening() {
				return nil
			}
		}
	}
}

// StopNative kills the native guacd subprocess if one was started.
func StopNative() {
	if nativeProcess != nil {
		_ = nativeProcess.Kill()
		nativeProcess = nil
	}
}

// EnsureRunning starts guacd if it's not already running. Tries in order:
//  1. Already listening on DefaultAddr (externally managed)
//  2. Native binary (Homebrew libexec or PATH)
//  3. Docker container (with optional sharedDir bind mount)
func EnsureRunning(ctx context.Context, sharedDir string) error {
	if !DockerAvailable(ctx) {
		if IsListening() {
			return nil
		}
		return fmt.Errorf("docker is not available and guacd is not listening on %s", DefaultAddr)
	}

	// Try native binary first (no sharedDir support for native yet)
	if sharedDir == "" {
		if bin := FindNativeBinary(); bin != "" {
			log.Printf("[guacd] found native binary: %s", bin)
			if err := StartNative(ctx, bin, DefaultPort); err == nil {
				return nil
			}
			log.Printf("[guacd] native binary failed, falling back to Docker")
		}
	}

	// Fall back to Docker
	return Start(ctx, sharedDir)
}

// DockerAvailable reports whether the docker CLI is present and the daemon is reachable.
func DockerAvailable(ctx context.Context) bool {
	return run(ctx, "docker", "info") == nil
}

// containerHasMount reports whether the named container has sharedDir as a
// bind-mount source.
func containerHasMount(ctx context.Context, sharedDir string) bool {
	out, err := output(ctx, "docker", "inspect", "--format", "{{range .Mounts}}{{.Source}}\n{{end}}", ContainerName)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == sharedDir {
			return true
		}
	}
	return false
}

func containerExists(ctx context.Context) bool {
	return run(ctx, "docker", "inspect", ContainerName) == nil
}

func startExisting(ctx context.Context) error {
	if err := run(ctx, "docker", "start", ContainerName); err != nil {
		return fmt.Errorf("start guacd container: %w", err)
	}
	return waitForReady(ctx)
}

func createAndStart(ctx context.Context, sharedDir string) error {
	args := []string{"run", "--detach",
		"--name", ContainerName,
		"--publish", fmt.Sprintf("%d:%d", DefaultPort, DefaultPort),
		"--restart", "unless-stopped",
	}
	if sharedDir != "" {
		args = append(args, "--volume", sharedDir+":"+ContainerMount)
	}
	args = append(args, Image)

	if err := run(ctx, "docker", args...); err != nil {
		return fmt.Errorf("create guacd container: %w", err)
	}
	return waitForReady(ctx)
}

func waitForReady(ctx context.Context) error {
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for guacd to become ready")
		case <-ticker.C:
			if IsListening() {
				return nil
			}
		}
	}
}

func run(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
