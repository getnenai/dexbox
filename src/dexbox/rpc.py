"""RPC client — sandbox → parent HTTP communication.

All calls from the sandbox container back to the parent service go
through this module.  Communication uses a session token for
authentication and the parent URL from environment variables.
"""

from __future__ import annotations

import os
from typing import Any

import httpx

from dexbox.exceptions import RPCError

# Read connection details from environment once at import time
_PARENT_URL = os.environ.get("DEXBOX_PARENT_URL", "http://172.17.0.1:8600")
_SESSION_TOKEN = os.environ.get("DEXBOX_SESSION_TOKEN", "")

# Default timeout for RPC calls (seconds)
_DEFAULT_TIMEOUT = 600.0


def _headers() -> dict[str, str]:
    return {"X-Session-Token": _SESSION_TOKEN}


def call(
    endpoint: str,
    *,
    method: str = "POST",
    json: dict[str, Any] | None = None,
    params: dict[str, Any] | None = None,
    timeout: float = _DEFAULT_TIMEOUT,
) -> Any:
    """Make an RPC call from the sandbox to the parent service.

    Args:
        endpoint: Path relative to parent URL (e.g. ``/internal/workflow/execute``).
        method: HTTP method.
        json: JSON body payload.
        params: Query parameters.
        timeout: Request timeout in seconds.

    Returns:
        Parsed JSON response.

    Raises:
        RPCError: On any communication failure.
    """
    url = f"{_PARENT_URL}{endpoint}"
    # RPC parameters
    rpc_params = params.copy() if params else {}

    try:
        response = httpx.request(
            method,
            url,
            headers={"X-Session-Token": _SESSION_TOKEN},
            json=json,
            params=rpc_params,
            timeout=timeout,
        )
        response.raise_for_status()
        return response.json()
    except httpx.HTTPStatusError as exc:
        raise RPCError(
            f"RPC call to {endpoint} failed with status {exc.response.status_code}: {exc.response.text}"
        ) from exc
    except httpx.RequestError as exc:
        raise RPCError(f"RPC call to {endpoint} failed: {exc}") from exc


def call_bytes(
    endpoint: str,
    *,
    params: dict[str, Any] | None = None,
    timeout: float = _DEFAULT_TIMEOUT,
) -> bytes:
    """Make an RPC call that returns raw bytes (e.g. file downloads).

    Args:
        endpoint: Path relative to parent URL.
        params: Query parameters.
        timeout: Request timeout in seconds.

    Returns:
        Raw response bytes.

    Raises:
        RPCError: On any communication failure.
    """
    url = f"{_PARENT_URL}{endpoint}"
    # RPC parameters
    rpc_params = params.copy() if params else {}

    try:
        response = httpx.get(
            url,
            headers={"X-Session-Token": _SESSION_TOKEN},
            params=rpc_params,
            timeout=timeout,
        )
        response.raise_for_status()
        return response.content
    except httpx.HTTPStatusError as exc:
        raise RPCError(f"RPC call to {endpoint} failed with status {exc.response.status_code}") from exc
    except httpx.RequestError as exc:
        raise RPCError(f"RPC call to {endpoint} failed: {exc}") from exc
