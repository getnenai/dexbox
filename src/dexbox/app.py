"""FastAPI application — the parent service running inside the desktop container.

Exposes:
  POST /run           – run a workflow script (streaming NDJSON)
  GET  /health        – readiness probe
  POST /cancel        – cancel the running workflow
  GET  /status        – current workflow status
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import time
from contextlib import asynccontextmanager

import uvicorn
from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel

from dexbox.config import ARTIFACTS_DIR, DEFAULT_MODEL, DEFAULT_PROVIDER, SERVER_HOST, SERVER_PORT
from dexbox.routes import router as internal_router
from dexbox.routes import set_session_manager
from dexbox.sandbox import SandboxOrchestrator, get_runtime_for_extension
from dexbox.session import SessionManager

logger = logging.getLogger("dexbox.server")

# ---------------------------------------------------------------------------
# Globals
# ---------------------------------------------------------------------------
_session_manager: SessionManager = SessionManager()
_orchestrator: SandboxOrchestrator = SandboxOrchestrator(_session_manager)
_run_lock = asyncio.Lock()
_current_workflow_id: str | None = None
_start_time: float = time.time()


class HealthEndpointFilter(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        if "GET /health" in record.getMessage():
            record.levelno = logging.DEBUG
            record.levelname = "DEBUG"
        return True


# ---------------------------------------------------------------------------
# Lifespan
# ---------------------------------------------------------------------------
@asynccontextmanager
async def lifespan(app: FastAPI):
    # Downgrade healthcheck access logs to DEBUG
    logging.getLogger("uvicorn.access").addFilter(HealthEndpointFilter())

    set_session_manager(_session_manager)
    ARTIFACTS_DIR.mkdir(parents=True, exist_ok=True)
    logger.info("dexbox service ready on %s:%d", SERVER_HOST, SERVER_PORT)
    yield
    logger.info("dexbox service shutting down")


app = FastAPI(title="dexbox", lifespan=lifespan)
app.include_router(internal_router)


# ---------------------------------------------------------------------------
# Request / response models
# ---------------------------------------------------------------------------


class RunRequest(BaseModel):
    script: str
    api_key: str
    model: str = DEFAULT_MODEL
    provider: str = DEFAULT_PROVIDER
    anthropic_base_url: str | None = None
    variables: dict | None = None
    secure_params: dict[str, str] | None = None
    workflow_id: str = ""
    runtime_extension: str = ".py"


class StatusResponse(BaseModel):
    running: bool
    workflow_id: str | None
    uptime_seconds: float


# ---------------------------------------------------------------------------
# Routes
# ---------------------------------------------------------------------------


@app.get("/health")
async def health():
    return {"status": "ok", "uptime_seconds": time.time() - _start_time}


@app.get("/status", response_model=StatusResponse)
async def status():
    return StatusResponse(
        running=_run_lock.locked(),
        workflow_id=_current_workflow_id,
        uptime_seconds=time.time() - _start_time,
    )


@app.post("/cancel")
async def cancel():
    """Cancel the currently-running workflow."""
    if not _orchestrator._active_container:
        raise HTTPException(status_code=404, detail="No workflow running")
    await _orchestrator.cancel_sandbox()
    return {"ok": True}


@app.post("/run")
async def run_workflow(request: RunRequest):
    """Execute a workflow script. Returns NDJSON stream of log lines + final result."""
    global _current_workflow_id

    if _run_lock.locked():
        raise HTTPException(status_code=429, detail="A workflow is already running")

    runtime = get_runtime_for_extension(request.runtime_extension)
    if runtime is None:
        raise HTTPException(status_code=400, detail=f"Unsupported runtime: {request.runtime_extension}")

    artifacts_dir = ARTIFACTS_DIR / (request.workflow_id or f"run-{int(time.time())}")

    async def _stream():
        global _current_workflow_id
        async with _run_lock:
            _current_workflow_id = request.workflow_id or None

            def _log_line(event_type: str, data) -> str:
                return json.dumps({"type": event_type, "data": data, "ts": time.time()}) + "\n"

            yield _log_line("started", {"workflow_id": request.workflow_id})

            try:
                # Create session manually so we can monitor its event queue
                session_token = _session_manager.create_session(
                    api_key=request.api_key,
                    model=request.model,
                    provider=request.provider,
                    anthropic_base_url=request.anthropic_base_url,
                    workflow_id=request.workflow_id,
                    variables=request.variables,
                    secure_params=request.secure_params,
                    artifacts_dir=str(artifacts_dir),
                )
                session = _session_manager.get_session(session_token)

                # Start execution as a task
                execution_task = asyncio.create_task(
                    _orchestrator.execute_workflow(
                        script=request.script,
                        api_key=request.api_key,
                        model=request.model,
                        provider=request.provider,
                        workflow_id=request.workflow_id,
                        variables=request.variables,
                        secure_params=request.secure_params,
                        runtime=runtime,
                        artifacts_dir=artifacts_dir,
                        session_token=session_token,
                    )
                )

                # Drain the event queue while execution is in progress
                while not execution_task.done():
                    try:
                        # Short timeout to poll for task completion
                        event = await asyncio.wait_for(session.event_queue.get(), timeout=0.1)
                        yield _log_line("progress", event)
                    except (asyncio.TimeoutError, asyncio.CancelledError):
                        continue

                result = await execution_task

                # Final flush of the event queue to catch remaining logs/assets
                while not session.event_queue.empty():
                    try:
                        event = session.event_queue.get_nowait()
                        yield _log_line("progress", event)
                    except asyncio.QueueEmpty:
                        break
            except Exception as exc:
                logger.exception("run_workflow failed: %s", exc)
                yield _log_line("error", {"error": str(exc)})
                _current_workflow_id = None
                return

            _current_workflow_id = None
            yield _log_line(
                "result",
                {
                    "success": result.success,
                    "exit_code": result.exit_code,
                    "error": result.error,
                    "result": result.result,
                    "duration_ms": result.duration_ms,
                },
            )

    return StreamingResponse(_stream(), media_type="application/x-ndjson")


# ---------------------------------------------------------------------------
# Entrypoint
# ---------------------------------------------------------------------------
if __name__ == "__main__":
    log_level_str = os.environ.get("DEXBOX_LOG_LEVEL", "INFO").upper()
    log_level = getattr(logging, log_level_str, logging.INFO)

    handler = logging.StreamHandler()
    handler.setLevel(log_level)
    logging.basicConfig(level=logging.NOTSET, handlers=[handler])

    uvicorn.run("dexbox.app:app", host=SERVER_HOST, port=SERVER_PORT, reload=False, log_level=log_level_str.lower())
