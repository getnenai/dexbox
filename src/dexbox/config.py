"""Centralized configuration — all environment variable reads in one place.

This module is imported by the server-side components (app, sandbox
orchestrator, routes).  The SDK files (agent, computer, rpc) read
their own env vars directly since they run inside the sandbox where
only a minimal set of env vars is available.
"""

from __future__ import annotations

import os
from pathlib import Path

# ---------------------------------------------------------------------------
# Server / Desktop container config
# ---------------------------------------------------------------------------

# FastAPI host and port for the parent service
SERVER_HOST: str = os.environ.get("DEXBOX_HOST", "0.0.0.0")
SERVER_PORT: int = int(os.environ.get("DEXBOX_PORT", "8600"))

# Docker sandbox settings
SANDBOX_MEMORY_LIMIT: str = os.environ.get("DEXBOX_SANDBOX_MEMORY", "512m")
SANDBOX_CPU_QUOTA: int = int(os.environ.get("DEXBOX_SANDBOX_CPU_QUOTA", "50000"))
SANDBOX_TIMEOUT: int = int(os.environ.get("DEXBOX_SANDBOX_TIMEOUT", "600"))
SANDBOX_PULL_POLICY: str = os.environ.get("DEXBOX_SANDBOX_PULL_POLICY", "never")
SANDBOX_TMPFS_SIZE: str = os.environ.get("DEXBOX_SANDBOX_TMPFS_SIZE", "64m")
SANDBOX_IMAGE: str = os.environ.get("DEXBOX_SANDBOX_IMAGE", "dexbox-sandbox-python:latest")

# Parent URL — how the sandbox reaches back to the parent service
PARENT_URL: str = os.environ.get("DEXBOX_PARENT_URL", "http://172.17.0.1:8600")

# Default LLM model
DEFAULT_MODEL: str = os.environ.get("DEXBOX_MODEL", "claude-haiku-4-5-20251001")
DEFAULT_PROVIDER: str = os.environ.get("DEXBOX_PROVIDER", "anthropic")

# Drive access — comma-separated list of allowed host paths
DRIVE_PATHS: list[str] = [
    p.strip() for p in os.environ.get("DRIVE_PATHS", "/mnt/tmp,/home/dexbox").split(",") if p.strip()
]

# Artifacts directory
ARTIFACTS_DIR: Path = Path(os.environ.get("DEXBOX_ARTIFACTS_DIR", "/tmp/dexbox-artifacts"))

# Screen dimensions (read by tools inside the desktop container)
SCREEN_WIDTH: int = int(os.environ.get("WIDTH", "1280"))
SCREEN_HEIGHT: int = int(os.environ.get("HEIGHT", "1024"))

# ---------------------------------------------------------------------------
# Sandbox env var names (shared constants)
# ---------------------------------------------------------------------------

ENV_SESSION_TOKEN: str = "DEXBOX_SESSION_TOKEN"
ENV_PARENT_URL: str = "DEXBOX_PARENT_URL"

# Marker for parsing container output
STDOUT_MARKER: str = "[STDOUT]"
