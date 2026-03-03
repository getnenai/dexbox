"""Internal Pydantic models for RPC payloads."""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel


class SuccessResponse(BaseModel):
    """Successful workflow execution result."""

    success: bool = True
    result: Any = None


class ErrorResponse(BaseModel):
    """Failed workflow execution result."""

    success: bool = False
    error: str | None = None


class DriveFileInfo(BaseModel):
    """Metadata for a file accessible via Drive."""

    name: str
    path: str
    size: int
    modified: float


class DriveFilesResponse(BaseModel):
    """Response for drive file listing."""

    files: list[DriveFileInfo]
