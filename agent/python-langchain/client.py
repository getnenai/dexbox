"""HTTP client for the dexbox tool server.

Thin wrapper around the dexbox REST API. All tool logic lives server-side;
this module just shuttles JSON between the LangChain agent and dexbox.
"""

from __future__ import annotations

import os

import httpx

DEXBOX_URL = os.getenv("DEXBOX_URL", "http://localhost:8600")
DEXBOX_MODEL = os.getenv("DEXBOX_MODEL", "claude-sonnet-4-20250514")

# Generous timeout — screenshots and PowerShell commands can be slow
_TIMEOUT = httpx.Timeout(connect=10.0, read=120.0, write=10.0, pool=10.0)


def health_check() -> bool:
    """Return True if dexbox server is reachable and healthy."""
    try:
        r = httpx.get(f"{DEXBOX_URL}/health", timeout=5.0)
        return r.status_code == 200
    except httpx.HTTPError:
        return False


def fetch_tool_schemas() -> list[dict]:
    """Fetch tool JSON Schemas from GET /tools.

    Returns a list of tool schema dicts, each containing:
        name, description, parameters, and optionally
        display_width_px / display_height_px.
    """
    r = httpx.get(f"{DEXBOX_URL}/tools", timeout=10.0)
    r.raise_for_status()
    return r.json()


def call_dexbox(tool_call: dict) -> dict:
    """POST a tool call to dexbox and return the result.

    Args:
        tool_call: Tool call body forwarded to POST /actions.

    Returns:
        Parsed JSON response from dexbox.

    Raises:
        httpx.HTTPStatusError: On non-2xx response.
        RuntimeError: If the response contains an error field.
    """
    r = httpx.post(
        f"{DEXBOX_URL}/actions",
        params={"model": DEXBOX_MODEL},
        json=tool_call,
        timeout=_TIMEOUT,
    )
    if r.status_code >= 400:
        print(f"  [dexbox] HTTP {r.status_code}: {r.text}", flush=True)
    r.raise_for_status()

    data = r.json()
    if isinstance(data, dict) and "error" in data:
        raise RuntimeError(f"dexbox error ({data['error']}): {data.get('message', '')}")
    return data


def call_dexbox_raw(tool_call: dict) -> bytes:
    """POST a tool call and return raw PNG bytes (for screenshots).

    Uses Accept: image/png to get raw image bytes instead of JSON.
    """
    r = httpx.post(
        f"{DEXBOX_URL}/actions",
        params={"model": DEXBOX_MODEL},
        json=tool_call,
        headers={"Accept": "image/png"},
        timeout=_TIMEOUT,
    )
    r.raise_for_status()
    return r.content
