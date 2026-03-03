"""Python sandbox harness — self-contained workflow runner.

Fetches the user's workflow code and input data via HTTP, loads the code
into a proper Python module with types.ModuleType, validates Pydantic
types, executes run(), and submits the result back via HTTP.

A top-level try/except catches *all* exceptions — including syntax errors,
import errors, and unhandled runtime errors — ensuring structured error
reporting regardless of where the failure occurs.

This file is read as text by get_harness() and passed to the sandbox
container via ``python -c <harness>``.
"""

from __future__ import annotations

import json
import logging
import os
import sys
import time
import traceback
import types
from datetime import datetime
from typing import get_type_hints

import httpx
from pydantic import BaseModel
from pydantic import ValidationError as PydanticValidationError

from dexbox.models import ErrorResponse, SuccessResponse

ENV_SESSION_TOKEN = "DEXBOX_SESSION_TOKEN"
ENV_PARENT_URL = "DEXBOX_PARENT_URL"
STDOUT_MARKER = "[STDOUT]"

_workflow_start_time = time.time()
_original_stdout = sys.stdout
_original_stderr = sys.stderr


class _JSONLFormatter(logging.Formatter):
    """JSONL formatter for structured logging output."""

    def format(self, record: logging.LogRecord) -> str:
        """Format a log record as a JSON line."""
        log_data = {
            "timestamp": datetime.fromtimestamp(record.created).isoformat(),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
        }
        if record.exc_info:
            log_data["exception"] = self.formatException(record.exc_info)
        for key, value in record.__dict__.items():
            if key not in [
                "name",
                "msg",
                "args",
                "created",
                "levelname",
                "levelno",
                "pathname",
                "filename",
                "module",
                "exc_info",
                "exc_text",
                "stack_info",
                "lineno",
                "funcName",
                "processName",
                "process",
                "threadName",
                "thread",
                "relativeCreated",
                "msecs",
                "getMessage",
                "message",
                "asctime",
                "taskName",
            ]:
                log_data[key] = value
        return json.dumps(log_data, default=str)


# Setup logging -> stdout (captured as workflow.jsonl)
_log_handler = logging.StreamHandler(_original_stdout)
_log_handler.setFormatter(_JSONLFormatter())
logging.root.addHandler(_log_handler)
logging.root.setLevel(logging.INFO)
logging.captureWarnings(True)


class _PrintStream:
    """Redirect print() to stderr with a prefix for separation."""

    def __init__(self) -> None:
        self._buffer = ""

    def write(self, message: str) -> None:
        """Buffer and flush complete lines with prefix."""
        self._buffer += message
        while "\n" in self._buffer:
            line, self._buffer = self._buffer.split("\n", 1)
            if line:
                _original_stderr.write(f"{STDOUT_MARKER}{line}\n")

    def flush(self) -> None:
        """Flush any remaining buffered content."""
        if self._buffer:
            _original_stderr.write(f"{STDOUT_MARKER}{self._buffer}\n")
            self._buffer = ""


sys.stdout = _PrintStream()

# Read environment
_session_token = os.environ.get(ENV_SESSION_TOKEN)
_parent_url = os.environ.get(ENV_PARENT_URL, "http://172.17.0.1:8600")

if not _session_token:
    raise RuntimeError("DEXBOX_SESSION_TOKEN environment variable not set")


def _submit_error(error_msg: str) -> None:
    """Best-effort submission of an error to the orchestrator."""
    try:
        _output_json = json.dumps({"success": False, "error": error_msg})
        httpx.post(
            f"{_parent_url}/internal/workflow/output",
            headers={"X-Session-Token": _session_token, "Content-Type": "application/json"},
            content=_output_json,
            timeout=5.0,
        )
    except Exception:
        pass


# ============================================================================
# Top-level try/except — catches everything including syntax/import errors
# in the user's workflow code.
# ============================================================================
try:
    import inspect

    # Fetch workflow code + input data from orchestrator
    _resp = httpx.get(
        f"{_parent_url}/internal/workflow/load",
        headers={"X-Session-Token": _session_token},
        timeout=10.0,
    )
    _resp.raise_for_status()
    _payload = _resp.json()
    _input = _payload.get("data", {})
    _code = _payload["code"]

    # Change to /assets directory (tmpfs mount — only writable location in container)
    os.chdir("/assets")

    # Load user's workflow code into a proper Python module
    _workflow = types.ModuleType("workflow")
    _workflow.__file__ = "workflow.py"
    sys.modules["workflow"] = _workflow
    exec(compile(_code, "workflow.py", "exec"), _workflow.__dict__)  # noqa: S102

    # Extract run() function
    if not hasattr(_workflow, "run") or not callable(_workflow.run):
        raise RuntimeError(
            "Workflow script must define a run(input) function.\n"
            "Example:\n\n"
            "class Input(BaseModel):\n"
            "    name: str\n\n"
            "class Output(BaseModel):\n"
            "    greeting: str\n\n"
            "def run(input: Input) -> Output:\n"
            '    return Output(greeting=f"Hello {input.name}")'
        )

    run = _workflow.run

    # Get type hints for Pydantic validation
    try:
        _hints = get_type_hints(run)
    except Exception as _e:
        raise RuntimeError(f"Failed to get type hints for run(): {_e}") from _e

    # Inspect run() signature for input parameter type
    _sig = inspect.signature(run)
    _params = list(_sig.parameters.values())
    if not _params:
        raise RuntimeError("run() must have at least one parameter")

    _input_type = _hints.get(_params[0].name)
    _secure_input_type = _hints.get(_params[1].name) if len(_params) > 1 else None
    _return_type = _hints.get("return")

    # Verify types are Pydantic BaseModel subclasses (required)
    try:
        if not _input_type or not isinstance(_input_type, type) or not issubclass(_input_type, BaseModel):
            raise RuntimeError(
                f"[RUNTIME VALIDATION] run() input parameter must be a Pydantic BaseModel subclass, "
                f"got: {_input_type}\n"
                f"Note: The class definition passed static analysis but failed runtime checks.\n"
                f"Ensure '{_input_type}' directly or indirectly inherits from pydantic.BaseModel"
            )
    except (TypeError, AttributeError) as _e:
        raise RuntimeError(
            f"Failed to validate input type annotation. Ensure it's a valid Pydantic BaseModel: {_e}"
        ) from _e

    try:
        if not _return_type or not isinstance(_return_type, type) or not issubclass(_return_type, BaseModel):
            raise RuntimeError(
                f"[RUNTIME VALIDATION] run() return type must be a Pydantic BaseModel subclass, got: {_return_type}\n"
                f"Note: The class definition passed static analysis but failed runtime checks.\n"
                f"Ensure '{_return_type}' directly or indirectly inherits from pydantic.BaseModel"
            )
    except (TypeError, AttributeError) as _e:
        raise RuntimeError(
            f"Failed to validate return type annotation. Ensure it's a valid Pydantic BaseModel: {_e}"
        ) from _e

    try:
        # Validate input data against the Pydantic model
        _validated_input = _input_type.model_validate(_input)

        _args = [_validated_input]
        if len(_params) > 1 and _secure_input_type:
            from dexbox import SecureValue

            _secure_kwargs = {}
            for _field_name in _secure_input_type.model_fields.keys():
                _secure_kwargs[_field_name] = SecureValue(_field_name)
            _secure_input = _secure_input_type.model_construct(**_secure_kwargs)
            _args.append(_secure_input)

        # Execute the workflow
        _result = run(*_args)

        # Validate and convert output
        if isinstance(_result, _return_type):
            # Workaround to deeply re-validate: if the workflow used model_construct(),
            # _result may contain unvalidated nested dicts. We dump to a python dict
            # (suppressing warnings) and re-validate to enforce full schema checks.
            _result_dict = _result.model_dump(mode="python", warnings=False)
            _result = _return_type.model_validate(_result_dict).model_dump(mode="json", exclude_none=True)
        elif isinstance(_result, dict):
            _result = _return_type.model_validate(_result).model_dump(mode="json", exclude_none=True)
        else:
            raise RuntimeError(f"run() must return {_return_type.__name__} or dict, got: {type(_result).__name__}")

        _success = True
        _error = None
    except PydanticValidationError as _ve:
        _result = None
        _success = False
        _validation_errors = _ve.errors()

        _error_summaries = []
        for _field_error in _validation_errors:
            _field_path = ".".join(str(_loc) for _loc in _field_error.get("loc", ()))
            _error_msg = _field_error.get("msg", "validation error")
            _error_summaries.append(f"{_field_path}: {_error_msg}")

        _error = f"ValidationError: {'; '.join(_error_summaries)}"

        logging.error(
            f"Input validation failed - {'; '.join(_error_summaries)}",
            extra={
                "event_type": "validation_error",
                "validation_errors": _validation_errors,
            },
        )
    except Exception as _e:
        _result = None
        _success = False
        _error = f"{type(_e).__name__}: {str(_e)}"
        logging.exception("Workflow run failed")

    # Submit result via HTTP POST
    if _success:
        _output_response = SuccessResponse(success=True, result=_result)
    else:
        _output_response = ErrorResponse(success=False, error=_error)

    _output_json = _output_response.model_dump_json()

    # Retry configuration for output submission
    _max_retries = 3
    _retry_delay = 1.0
    _output_submitted = False

    for _attempt in range(1, _max_retries + 1):
        try:
            _output_resp = httpx.post(
                f"{_parent_url}/internal/workflow/output",
                headers={"X-Session-Token": _session_token, "Content-Type": "application/json"},
                content=_output_json,
                timeout=10.0,
            )
            _output_resp.raise_for_status()
            _output_submitted = True
            logging.info(
                "Output successfully submitted to parent service",
                extra={"attempt": _attempt, "event_type": "output_submitted"},
            )
            break
        except httpx.HTTPStatusError as _http_err:
            logging.error(
                f"Failed to submit output (attempt {_attempt}/{_max_retries}): HTTP {_http_err.response.status_code}",
                extra={
                    "event_type": "output_submission_failed",
                    "attempt": _attempt,
                    "status_code": _http_err.response.status_code,
                    "response_body": _http_err.response.text,
                    "error_type": "http_status_error",
                },
            )
            if _attempt < _max_retries:
                time.sleep(_retry_delay)
        except httpx.RequestError as _req_err:
            logging.error(
                f"Failed to submit output (attempt {_attempt}/{_max_retries}): {type(_req_err).__name__} - {_req_err}",
                extra={
                    "event_type": "output_submission_failed",
                    "attempt": _attempt,
                    "error_type": type(_req_err).__name__,
                    "error_message": str(_req_err),
                },
            )
            if _attempt < _max_retries:
                time.sleep(_retry_delay)
        except Exception as _unexpected_err:
            logging.error(
                f"Unexpected error submitting output (attempt {_attempt}/{_max_retries}): "
                f"{type(_unexpected_err).__name__} - {_unexpected_err}",
                extra={
                    "event_type": "output_submission_failed",
                    "attempt": _attempt,
                    "error_type": type(_unexpected_err).__name__,
                    "error_message": str(_unexpected_err),
                    "traceback": traceback.format_exc(),
                },
            )
            if _attempt < _max_retries:
                time.sleep(_retry_delay)

    if not _output_submitted:
        logging.critical(
            "CRITICAL: Failed to submit workflow output after all retry attempts. "
            "Parent service may not receive results.",
            extra={
                "event_type": "output_submission_critical_failure",
                "max_retries": _max_retries,
                "workflow_success": _success,
            },
        )
        sys.exit(2)

    # Archive /assets and upload to parent service
    _ws_entries = []
    try:
        _ws_entries = [e for e in os.listdir("/assets") if e != ".dexbox"]
    except (FileNotFoundError, NotADirectoryError):
        _ws_entries = []

    if not _ws_entries:
        logging.info(
            "Assets archive skipped: /assets is empty (no files written by workflow)",
            extra={"event_type": "assets_archive_skipped", "reason": "empty_assets"},
        )
    else:
        logging.info(
            "Archiving /assets (%d top-level entries)",
            len(_ws_entries),
            extra={
                "event_type": "assets_archive_start",
                "entry_count": len(_ws_entries),
                "entries": _ws_entries[:20],
            },
        )
        try:
            import io
            import tarfile as _tarfile
            from pathlib import Path as _ArchivePath

            _root = _ArchivePath("/assets")
            _internal_dir = _root / ".dexbox"
            _tar_buf = io.BytesIO()
            _archived_files = []
            with _tarfile.open(fileobj=_tar_buf, mode="w") as _tf:
                for _p in sorted(_root.rglob("*")):
                    try:
                        if _internal_dir in _p.parents or _p == _internal_dir:
                            continue
                        if _p.is_symlink():
                            continue
                        if not _p.is_file():
                            continue
                        _arcname = str(_ArchivePath("assets") / _p.relative_to(_root))
                        _tf.add(str(_p), arcname=_arcname)
                        _archived_files.append(_arcname)
                    except Exception as _add_err:
                        logging.warning(
                            "Skipping assets file %s: %s",
                            _p,
                            _add_err,
                            extra={"event_type": "assets_archive_file_skip"},
                        )
                        continue

            _tar_bytes = _tar_buf.getvalue()
            if not _archived_files:
                logging.warning(
                    "Assets archive skipped: no files archived despite %d top-level entries",
                    len(_ws_entries),
                    extra={"event_type": "assets_archive_skipped", "reason": "no_files_archived"},
                )
            else:
                logging.info(
                    "Uploading assets archive: %d file(s), %d bytes",
                    len(_archived_files),
                    len(_tar_bytes),
                    extra={
                        "event_type": "assets_archive_upload",
                        "file_count": len(_archived_files),
                        "tar_size_bytes": len(_tar_bytes),
                        "files": _archived_files[:50],
                    },
                )
                _archive_resp = httpx.post(
                    f"{_parent_url}/internal/workflow/assets",
                    headers={"X-Session-Token": _session_token},
                    content=_tar_bytes,
                    timeout=30.0,
                )
                _archive_resp.raise_for_status()
                logging.info(
                    "Assets archive uploaded successfully (%d bytes, %d files)",
                    len(_tar_bytes),
                    len(_archived_files),
                    extra={"event_type": "assets_archived", "file_count": len(_archived_files)},
                )
        except Exception as _archive_err:
            logging.warning(
                "Failed to upload assets archive: %s",
                _archive_err,
                extra={"event_type": "assets_archive_failed", "error": str(_archive_err)},
            )

    # Log completion
    _duration_ms = int((time.time() - _workflow_start_time) * 1000)
    logging.info(
        "Workflow completed",
        extra={"duration_ms": _duration_ms, "event_type": "workflow_complete"},
    )

    # Exit with error code if run failed
    if not _success:
        sys.exit(1)

except Exception as _fatal_exc:
    # Catch-all for any exception that escaped the above — syntax errors in
    # user code, import errors, HTTP fetch failures, etc.
    logging.critical("Unhandled exception in workflow", exc_info=True)
    _submit_error(f"{type(_fatal_exc).__name__}: {_fatal_exc}")
    sys.exit(1)
