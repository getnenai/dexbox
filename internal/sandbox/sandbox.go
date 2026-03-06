package sandbox

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/getnenai/dexbox/internal/config"
)

type SandboxRuntime struct {
	Image         string
	CommandPrefix []string
}

var DefaultRuntime = SandboxRuntime{
	Image:         config.SandboxImage,
	CommandPrefix: []string{"-c"},
}

type SandboxOrchestrator struct {
	dockerClient *client.Client
}

func NewSandboxOrchestrator() (*SandboxOrchestrator, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &SandboxOrchestrator{dockerClient: cli}, nil
}

func (s *SandboxOrchestrator) RunContainer(ctx context.Context, sessionToken string, harnessScript string) (string, io.ReadCloser, error) {
	if config.SandboxPullPolicy == "always" {
		log.Printf("Pulling image %s", config.SandboxImage)
		_, err := s.dockerClient.ImagePull(ctx, config.SandboxImage, image.PullOptions{})
		if err != nil {
			return "", nil, fmt.Errorf("failed to pull image: %w", err)
		}
	}

	resp, err := s.dockerClient.ContainerCreate(ctx, &container.Config{
		Image: config.SandboxImage,
		Cmd:   append(DefaultRuntime.CommandPrefix, harnessScript),
		Env: []string{
			fmt.Sprintf("%s=%s", config.EnvSessionToken, sessionToken),
			fmt.Sprintf("%s=%s", config.EnvParentURL, config.ParentURL),
		},
		NetworkDisabled: false,
	}, &container.HostConfig{
		Resources: container.Resources{
			Memory:   512 * 1024 * 1024, // TODO: parse config.SandboxMemoryLimit
			CPUQuota: config.SandboxCPUQuota,
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		Tmpfs:       map[string]string{"/assets": fmt.Sprintf("size=%s,mode=1777", config.SandboxTmpfsSize)},
		NetworkMode: "bridge",
		AutoRemove:  false,
	}, nil, nil, "")

	if err != nil {
		return "", nil, fmt.Errorf("failed to create container: %w", err)
	}

	if err := s.dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = s.dockerClient.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
		return resp.ID, nil, fmt.Errorf("failed to start container: %w", err)
	}

	logs, err := s.dockerClient.ContainerLogs(ctx, resp.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return resp.ID, nil, fmt.Errorf("failed to get container logs: %w", err)
	}

	return resp.ID, logs, nil
}

func (s *SandboxOrchestrator) WaitAndCleanup(ctx context.Context, containerID string) (int64, error) {
	statusCh, errCh := s.dockerClient.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	var exitCode int64 = -1

	select {
	case err := <-errCh:
		if err != nil {
			log.Printf("Error waiting for container %s: %v", containerID, err)
		}
	case status := <-statusCh:
		exitCode = status.StatusCode
	case <-ctx.Done():
		log.Printf("Context cancelled while waiting for container %s", containerID)
	}

	// Always try to remove the container
	removeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.dockerClient.ContainerRemove(removeCtx, containerID, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("Error removing container %s: %v", containerID, err)
	}

	return exitCode, nil
}
