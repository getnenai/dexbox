// Package guacd manages the Apache Guacamole guacd Docker container
// used as the RDP connection proxy.
package guacd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
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
		if sharedDir != "" && !containerHasMount(ctx, sharedDir) {
			log.Printf("guacd: container %s is missing bind mount for %s; removing and recreating", ContainerName, sharedDir)
			_ = run(ctx, "docker", "stop", ContainerName)
			_ = run(ctx, "docker", "rm", "--force", ContainerName)
			return createAndStart(ctx, sharedDir)
		}
		return nil
	}

	if containerExists(ctx) {
		if sharedDir != "" && !containerHasMount(ctx, sharedDir) {
			log.Printf("guacd: container %s is missing bind mount for %s; removing and recreating", ContainerName, sharedDir)
			_ = run(ctx, "docker", "rm", "--force", ContainerName)
			return createAndStart(ctx, sharedDir)
		}
		return startExisting(ctx)
	}

	return createAndStart(ctx, sharedDir)
}

// Stop stops and removes the guacd container.
func Stop(ctx context.Context) error {
	if !containerExists(ctx) {
		return nil
	}
	_ = run(ctx, "docker", "stop", ContainerName)
	_ = run(ctx, "docker", "rm", "--force", ContainerName)
	return nil
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

// EnsureRunning starts guacd if it's not already running, and reconciles the
// bind-mount when sharedDir is non-empty. If Docker is unavailable but guacd
// is already listening on DefaultAddr with no sharedDir required, this is
// treated as a success — useful when guacd was started externally.
func EnsureRunning(ctx context.Context, sharedDir string) error {
	if IsListening() && sharedDir == "" {
		return nil
	}
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
