// Package guacd manages the Apache Guacamole guacd Docker container
// used as the RDP connection proxy.
package guacd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	ContainerName = "dexbox-guacd"
	Image         = "guacamole/guacd:latest"
	DefaultPort   = 4822
	DefaultAddr   = "localhost:4822"
)

// Start launches the guacd Docker container. Idempotent: if the container
// is already running, this is a no-op. If it exists but is stopped, it
// restarts it.
func Start(ctx context.Context) error {
	running, err := IsRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		return nil
	}

	// Check if container exists but is stopped
	if containerExists(ctx) {
		return startExisting(ctx)
	}

	return createAndStart(ctx)
}

// Stop stops and removes the guacd container.
func Stop(ctx context.Context) error {
	if !containerExists(ctx) {
		return nil
	}
	_ = run(ctx, "docker", "stop", ContainerName)
	_ = run(ctx, "docker", "rm", "-f", ContainerName)
	return nil
}

// IsRunning reports whether the guacd container is running.
func IsRunning(ctx context.Context) (bool, error) {
	if !DockerAvailable(ctx) {
		return false, fmt.Errorf("docker is not available; install Docker to use RDP connections")
	}
	out, err := output(ctx, "docker", "inspect", "-f", "{{.State.Running}}", ContainerName)
	if err != nil {
		return false, nil // container doesn't exist
	}
	return strings.TrimSpace(out) == "true", nil
}

// EnsureRunning starts guacd if it's not already running.
func EnsureRunning(ctx context.Context) error {
	running, err := IsRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	return Start(ctx)
}

// DockerAvailable reports whether the docker CLI is present and the daemon is reachable.
func DockerAvailable(ctx context.Context) bool {
	return run(ctx, "docker", "info") == nil
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

func createAndStart(ctx context.Context) error {
	err := run(ctx, "docker", "run", "-d",
		"--name", ContainerName,
		"-p", fmt.Sprintf("%d:%d", DefaultPort, DefaultPort),
		"--restart", "unless-stopped",
		Image,
	)
	if err != nil {
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
			running, _ := IsRunning(ctx)
			if running {
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
