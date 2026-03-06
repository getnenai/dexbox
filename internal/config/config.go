package config

import (
	"os"
	"strconv"
)

var (
	SandboxMemoryLimit = getEnv("DEXBOX_SANDBOX_MEMORY", "512m")
	SandboxCPUQuota    = getEnvInt64("DEXBOX_SANDBOX_CPU_QUOTA", 50000)
	SandboxTimeout     = getEnvInt("DEXBOX_SANDBOX_TIMEOUT", 600)
	SandboxPullPolicy  = getEnv("DEXBOX_SANDBOX_PULL_POLICY", "never")
	SandboxTmpfsSize   = getEnv("DEXBOX_SANDBOX_TMPFS_SIZE", "64m")
	SandboxImage       = getEnv("DEXBOX_SANDBOX_IMAGE", "dexbox-sandbox-python:latest")
	ParentURL          = getEnv("DEXBOX_PARENT_URL", "http://host.docker.internal:8600")
	StdoutMarker       = "[STDOUT]"
	ArtifactsDir       = getEnv("DEXBOX_ARTIFACTS_DIR", "/tmp/dexbox-artifacts")
)

const (
	EnvSessionToken = "DEXBOX_SESSION_TOKEN"
	EnvParentURL    = "DEXBOX_PARENT_URL"
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i
		}
	}
	return fallback
}
