"""Internal RPC routes — sandbox → parent HTTP endpoints.

Extracted from cup/workflow_handler/internal_routes.py with cloud-specific
imports removed and imports updated to dexbox namespace.
"""

from __future__ import annotations

import base64
import glob
import logging
import os
from typing import Any

from fastapi import APIRouter, Header, HTTPException, Query, Request
from fastapi.responses import Response
from pydantic import BaseModel, Field, model_validator

from dexbox.archive import tar_to_zip
from dexbox.logging import COMPONENT_LOGGERS, QueueHandler
from dexbox.loop import sampling_loop
from dexbox.perception import UIPerceptionClient
from dexbox.session import SessionManager
from dexbox.tools.groups import build_runtime_tools

logger = logging.getLogger("dexbox.server")

router = APIRouter()

# Session manager singleton — set at app startup
_session_manager: SessionManager | None = None


def set_session_manager(manager: SessionManager) -> None:
    global _session_manager
    _session_manager = manager


def _get_session_manager() -> SessionManager:
    if _session_manager is None:
        raise RuntimeError("Session manager not initialized")
    return _session_manager


# ---------------------------------------------------------------------------
# Request / response models
# ---------------------------------------------------------------------------


class ExecuteRequest(BaseModel):
    instruction: str
    max_iterations: int = 10
    model_override: str | None = None


class ExecuteResponse(BaseModel):
    success: bool
    messages: list[dict[str, Any]] = Field(default_factory=list)
    error: str | None = None


class ValidateRequest(BaseModel):
    question: str
    timeout: int = 10
    model_override: str | None = None


class ValidateResponse(BaseModel):
    success: bool
    is_valid: bool
    reason: str = ""
    error: str | None = None


class ExtractRequest(BaseModel):
    query: str
    schema_def: dict[str, Any]
    model_override: str | None = None


class ExtractResponse(BaseModel):
    success: bool
    data: Any = None
    error: str | None = None


class KeyboardTypeRequest(BaseModel):
    text: str | None = None
    secure_value_id: str | None = None
    interval: float = 0.02

    @model_validator(mode="after")
    def exactly_one_text_source(self) -> "KeyboardTypeRequest":
        if bool(self.text) == bool(self.secure_value_id):
            raise ValueError("Exactly one of 'text' or 'secure_value_id' must be provided")
        return self


class KeyboardPressRequest(BaseModel):
    key: str


class KeyboardHotkeyRequest(BaseModel):
    keys: list[str]


class MouseClickRequest(BaseModel):
    x: int
    y: int
    button: str = "left"


class MouseMoveRequest(BaseModel):
    x: int
    y: int


class MouseScrollRequest(BaseModel):
    direction: str = "down"
    amount: int = 3
    x: int | None = None
    y: int | None = None


class ActionResponse(BaseModel):
    success: bool
    error: str | None = None


# ---------------------------------------------------------------------------
# Auth helper
# ---------------------------------------------------------------------------


def _require_session(token: str):
    """Validate session token and return session, raising 401 on failure."""
    manager = _get_session_manager()
    session, error = manager.get_session_status(token)
    if error == "not_found":
        raise HTTPException(status_code=401, detail="Invalid or missing session token")
    if error == "terminated":
        raise HTTPException(status_code=401, detail="Session terminated")
    if error == "expired":
        raise HTTPException(status_code=401, detail="Session expired")
    return session


# ---------------------------------------------------------------------------
# Workflow lifecycle endpoints
# ---------------------------------------------------------------------------


@router.get("/internal/workflow/load")
async def load_workflow(token: str = Header(..., alias="X-Session-Token")):
    """Sandbox calls this to fetch its workflow code and input data."""
    session = _require_session(token)
    return {"data": session.input_data or {}, "code": session.code}


@router.post("/internal/workflow/output")
async def set_output(payload: dict[str, Any], token: str = Header(..., alias="X-Session-Token")):
    """Sandbox calls this to submit execution results."""
    session = _require_session(token)
    session.output_data = payload
    return {"ok": True}


@router.post("/internal/workflow/assets")
async def set_assets(request: Request, token: str = Header(..., alias="X-Session-Token")):
    """Sandbox calls this to upload the /assets archive."""
    session = _require_session(token)
    body = await request.body()
    session.assets_archive = body

    try:
        zip_bytes = tar_to_zip(body)
        if zip_bytes:
            # Save to the workflow-specific artifacts directory (mounted on host)
            assets_path = None
            if session.artifacts_dir:
                from pathlib import Path

                zip_filename = "assets.zip"
                # Ensure the artifacts directory exists
                artifacts_path = Path(session.artifacts_dir)
                artifacts_path.mkdir(parents=True, exist_ok=True)
                assets_path = artifacts_path / zip_filename
                assets_path.write_bytes(zip_bytes)

            # Send assets event to the parent stream for CLI download
            await session.event_queue.put(
                {
                    "type": "assets",
                    "data": {
                        "filename": "assets.zip",
                        "content_b64": base64.b64encode(zip_bytes).decode(),
                    },
                }
            )

            # Log successful upload with path
            logger.info(
                "Assets archive uploaded successfully",
                extra={"path": str(assets_path) if assets_path else "memory-only", "size_bytes": len(body)},
            )
    except Exception as e:
        logger.warning("Failed to process assets: %s", e)

    return {"ok": True}


# ---------------------------------------------------------------------------
# VLM endpoints
# ---------------------------------------------------------------------------


@router.post("/internal/workflow/execute", response_model=ExecuteResponse)
async def execute(request: ExecuteRequest, token: str = Header(..., alias="X-Session-Token")):
    """Run a VLM agent instruction sequence."""
    session = _require_session(token)
    model = request.model_override or session.model

    if session._tool_collection is None:
        session._tool_collection = build_runtime_tools()

    async def on_vlm_output(event_type: str, data: Any):
        await session.event_queue.put({"type": event_type, "data": data})

    messages = [{"role": "user", "content": [{"type": "text", "text": request.instruction}]}]

    # Capture platform logs and pipe them into the session event queue
    queue_handler = QueueHandler(session.event_queue)
    loggers = [logging.getLogger(f"dexbox.{name}") for name in COMPONENT_LOGGERS]
    try:
        for lg in loggers:
            lg.addHandler(queue_handler)

        logger.info(
            "Execute request: model=%s, api_key_len=%d, api_key_start=%s, api_key_end=%s",
            model,
            len(session.api_key),
            session.api_key[:8],
            session.api_key[-8:],
        )
        messages, _ = await sampling_loop(
            model=model,
            api_key=session.api_key,
            base_url=session.anthropic_base_url,
            tool_collection=session._tool_collection,
            messages=messages,
            max_iterations=request.max_iterations,
            on_output=on_vlm_output,
        )
        # Strip base64 image data from messages to prevent sandbox OOM
        import copy

        stripped_messages = []
        for m in messages:
            msg = copy.deepcopy(m)
            if "content" in msg and isinstance(msg["content"], list):
                for block in msg["content"]:
                    if isinstance(block, dict) and block.get("type") == "image":
                        source = block.get("source", {})
                        if source.get("type") == "base64" and "data" in source:
                            source["data"] = "<stripped to prevent OOM>"
            stripped_messages.append(msg)

        return ExecuteResponse(success=True, messages=stripped_messages)
    except Exception as exc:
        logger.exception("execute failed: %s", exc)
        return ExecuteResponse(success=False, error=str(exc))
    finally:
        for lg in loggers:
            lg.removeHandler(queue_handler)


@router.post("/internal/workflow/validate", response_model=ValidateResponse)
async def validate(request: ValidateRequest, token: str = Header(..., alias="X-Session-Token")):
    """Run a VLM yes/no check on the current screen."""
    session = _require_session(token)
    model = request.model_override or session.model
    client = UIPerceptionClient(api_key=session.api_key, model=model)
    try:
        screenshot = await client.take_screenshot()
        result = await client.is_vlm_match(screenshot, request.question)
        return ValidateResponse(
            success=True,
            is_valid=result.get("is_match", False),
            reason=result.get("match_reason", ""),
        )
    except Exception as exc:
        logger.exception("validate failed: %s", exc)
        return ValidateResponse(success=False, is_valid=False, error=str(exc))


@router.post("/internal/workflow/extract", response_model=ExtractResponse)
async def extract(request: ExtractRequest, token: str = Header(..., alias="X-Session-Token")):
    """Extract structured data from the current screen."""
    session = _require_session(token)
    model = request.model_override or session.model
    client = UIPerceptionClient(api_key=session.api_key, model=model)

    import json as _json

    import anthropic as _anthropic

    ac = _anthropic.Anthropic(api_key=session.api_key)
    prompt = (
        f"Look at this screenshot. {request.query}\n\n"
        f"Return your answer as JSON matching this schema: {_json.dumps(request.schema_def)}\n"
        f"Return ONLY valid JSON, do not include markdown formatting or explanations."
    )
    try:
        screenshot = await client.take_screenshot()
        response = ac.beta.messages.create(
            model=model,
            max_tokens=1024,
            messages=[
                {
                    "role": "user",
                    "content": [
                        {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": screenshot}},
                        {"type": "text", "text": prompt},
                    ],
                }
            ],
            betas=["prompt-caching-2024-07-31"],
        )
        text = response.content[0].text

        # Try to find JSON block in markdown backticks first
        import re

        json_match = re.search(r"```(?:json)?\s*(.*?)\s*```", text, re.DOTALL)
        if json_match:
            text = json_match.group(1)

        data = _json.loads(text)
        return ExtractResponse(success=True, data=data)
    except Exception as exc:
        logger.exception("extract failed: %s", exc)
        return ExtractResponse(success=False, error=str(exc))


# ---------------------------------------------------------------------------
# Keyboard endpoints
# ---------------------------------------------------------------------------


@router.post("/internal/workflow/keyboard/type", response_model=ActionResponse)
async def keyboard_type(request: KeyboardTypeRequest, token: str = Header(..., alias="X-Session-Token")):
    session = _require_session(token)
    if session._tool_collection is None:
        session._tool_collection = build_runtime_tools()

    if request.secure_value_id is not None:
        text = session.secure_params.get(request.secure_value_id)
        if text is None:
            return ActionResponse(success=False, error=f"Secure value '{request.secure_value_id}' not found in session")
    else:
        text = request.text

    result = await session._tool_collection.run(name="computer", tool_input={"action": "type", "text": text})
    return ActionResponse(success=not bool(result.error), error=result.error)


@router.post("/internal/workflow/keyboard/press", response_model=ActionResponse)
async def keyboard_press(request: KeyboardPressRequest, token: str = Header(..., alias="X-Session-Token")):
    session = _require_session(token)
    if session._tool_collection is None:
        session._tool_collection = build_runtime_tools()
    result = await session._tool_collection.run(name="computer", tool_input={"action": "key", "text": request.key})
    return ActionResponse(success=not bool(result.error), error=result.error)


@router.post("/internal/workflow/keyboard/hotkey", response_model=ActionResponse)
async def keyboard_hotkey(request: KeyboardHotkeyRequest, token: str = Header(..., alias="X-Session-Token")):
    session = _require_session(token)
    if session._tool_collection is None:
        session._tool_collection = build_runtime_tools()
    result = await session._tool_collection.run(
        name="computer", tool_input={"action": "key", "text": "+".join(request.keys)}
    )
    return ActionResponse(success=not bool(result.error), error=result.error)


# ---------------------------------------------------------------------------
# Mouse endpoints
# ---------------------------------------------------------------------------


@router.post("/internal/workflow/mouse/click", response_model=ActionResponse)
async def mouse_click(request: MouseClickRequest, token: str = Header(..., alias="X-Session-Token")):
    session = _require_session(token)
    if session._tool_collection is None:
        session._tool_collection = build_runtime_tools()
    action = {"left": "left_click", "right": "right_click", "middle": "middle_click"}.get(request.button, "left_click")
    result = await session._tool_collection.run(
        name="computer", tool_input={"action": action, "coordinate": [request.x, request.y]}
    )
    return ActionResponse(success=not bool(result.error), error=result.error)


@router.post("/internal/workflow/mouse/move", response_model=ActionResponse)
async def mouse_move(request: MouseMoveRequest, token: str = Header(..., alias="X-Session-Token")):
    session = _require_session(token)
    if session._tool_collection is None:
        session._tool_collection = build_runtime_tools()
    result = await session._tool_collection.run(
        name="computer", tool_input={"action": "mouse_move", "coordinate": [request.x, request.y]}
    )
    return ActionResponse(success=not bool(result.error), error=result.error)


@router.post("/internal/workflow/mouse/scroll", response_model=ActionResponse)
async def mouse_scroll(request: MouseScrollRequest, token: str = Header(..., alias="X-Session-Token")):
    session = _require_session(token)
    if session._tool_collection is None:
        session._tool_collection = build_runtime_tools()
    tool_input: dict = {"action": "scroll", "scroll_direction": request.direction, "scroll_amount": request.amount}
    if request.x is not None and request.y is not None:
        tool_input["coordinate"] = [request.x, request.y]
    result = await session._tool_collection.run(name="computer", tool_input=tool_input)
    return ActionResponse(success=not bool(result.error), error=result.error)


# ---------------------------------------------------------------------------
# Drive (filesystem) endpoints
# ---------------------------------------------------------------------------

ALLOWED_DRIVE_PREFIXES: list[str] = [
    p.strip() for p in os.environ.get("DRIVE_PATHS", "/mnt/tmp").split(",") if p.strip()
]


def _validate_drive_path(path: str) -> str:
    """Validate and return the real path, raising 403 if not allowed."""
    real = os.path.realpath(path)
    for prefix in ALLOWED_DRIVE_PREFIXES:
        real_prefix = os.path.realpath(prefix)
        # Check if the path is the prefix itself or a subdirectory of it
        if real == real_prefix or real.startswith(os.path.join(real_prefix, "")):
            return real
    raise HTTPException(status_code=403, detail=f"Path '{path}' not in allowed drive paths")


@router.get("/internal/workflow/drive/files")
async def drive_files(
    path: str = Query(...),
    pattern: str = Query("*"),
    token: str = Header(..., alias="X-Session-Token"),
):
    _require_session(token)
    # Reject patterns with path separators (traversal guard)
    if "/" in pattern or "\\" in pattern or ".." in pattern:
        raise HTTPException(status_code=400, detail="Invalid pattern: must not contain path separators or '..'")
    base = _validate_drive_path(path)
    matches = glob.glob(os.path.join(base, pattern))
    files = []
    for match in matches:
        real = os.path.realpath(match)
        if os.path.isfile(real):
            stat = os.stat(real)
            files.append(
                {
                    "name": os.path.basename(real),
                    "path": path,
                    "size": stat.st_size,
                    "modified": stat.st_mtime,
                }
            )
    return {"files": files}


@router.get("/internal/workflow/drive/read")
async def drive_read(
    path: str = Query(...),
    filename: str = Query(...),
    token: str = Header(..., alias="X-Session-Token"),
):
    _require_session(token)
    if ".." in filename or "/" in filename or "\\" in filename:
        raise HTTPException(status_code=403, detail="Invalid filename: path traversal not allowed")
    base = _validate_drive_path(path)
    full_path = os.path.join(base, filename)
    if not os.path.isfile(full_path):
        raise HTTPException(status_code=404, detail=f"File '{filename}' not found")
    with open(full_path, "rb") as f:
        content = f.read()
    return Response(content=content, media_type="application/octet-stream")
