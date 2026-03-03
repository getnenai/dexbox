"""JSONL logging configuration for sandbox workflow execution."""

from __future__ import annotations

import asyncio
import json
import logging
from contextlib import contextmanager
from contextvars import ContextVar
from datetime import datetime
from pathlib import Path
from typing import Any, Generator

# Context variable for structured log tags
_log_tags: ContextVar[dict[str, Any]] = ContextVar("log_tags", default={})

COMPONENT_LOGGERS = ["server", "agent", "sandbox", "session", "perception", "tools", "recorder"]


class WorkflowViewFilter(logging.Filter):
    """Filters log records for the workflow developer view."""

    def filter(self, record: logging.LogRecord) -> bool:
        if record.name in ("dexbox.session", "dexbox.recorder", "dexbox.server"):
            return False
        if record.name == "dexbox.sandbox" and record.levelno < logging.WARNING:
            return False
        return True


class TagInjectionFilter(logging.Filter):
    """Injects context tags into log records."""

    def filter(self, record: logging.LogRecord) -> bool:
        tags = _log_tags.get({})
        if tags:
            record.tags = tags.copy()
        return True


class JSONLFormatter(logging.Formatter):
    """Format log records as JSON Lines."""

    _STANDARD_ATTRS = {
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
    }

    def format(self, record: logging.LogRecord) -> str:
        log_data = {
            "timestamp": datetime.fromtimestamp(record.created).isoformat(),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
        }
        if record.exc_info:
            log_data["exception"] = self.formatException(record.exc_info)
        for key, value in record.__dict__.items():
            if key not in self._STANDARD_ATTRS:
                log_data[key] = value
        return json.dumps(log_data, default=str)


def setup_sandbox_logging(artifacts_dir: Path) -> tuple[logging.FileHandler, logging.FileHandler, logging.FileHandler]:
    """Set up triple logging: workflow.log, workflow-log.jsonl, system-log.jsonl."""
    artifacts_dir.mkdir(parents=True, exist_ok=True)
    tag_filter = TagInjectionFilter()
    workflow_filter = WorkflowViewFilter()

    workflow_log_handler = logging.FileHandler(artifacts_dir / "workflow.log")
    workflow_log_handler.setFormatter(JSONLFormatter())
    workflow_log_handler.setLevel(logging.INFO)
    workflow_log_handler.addFilter(tag_filter)
    workflow_log_handler.addFilter(workflow_filter)

    workflow_jsonl_handler = logging.FileHandler(artifacts_dir / "workflow-log.jsonl")
    workflow_jsonl_handler.setFormatter(JSONLFormatter())
    workflow_jsonl_handler.setLevel(logging.INFO)
    workflow_jsonl_handler.addFilter(tag_filter)
    workflow_jsonl_handler.addFilter(workflow_filter)

    system_handler = logging.FileHandler(artifacts_dir / "system-log.jsonl")
    system_handler.setFormatter(JSONLFormatter())
    system_handler.setLevel(logging.DEBUG)
    system_handler.addFilter(tag_filter)

    for logger_name in COMPONENT_LOGGERS:
        lg = logging.getLogger(f"dexbox.{logger_name}")
        lg.setLevel(logging.DEBUG)
        lg.addHandler(workflow_log_handler)
        lg.addHandler(workflow_jsonl_handler)
        lg.addHandler(system_handler)

    return workflow_log_handler, workflow_jsonl_handler, system_handler


def cleanup_sandbox_logging(
    workflow_log_handler: logging.FileHandler,
    workflow_jsonl_handler: logging.FileHandler,
    system_handler: logging.FileHandler,
) -> None:
    """Remove handlers and close log files."""
    for logger_name in COMPONENT_LOGGERS:
        lg = logging.getLogger(f"dexbox.{logger_name}")
        lg.removeHandler(workflow_log_handler)
        lg.removeHandler(workflow_jsonl_handler)
        lg.removeHandler(system_handler)
    workflow_log_handler.close()
    workflow_jsonl_handler.close()
    system_handler.close()


@contextmanager
def log_context(**tags: Any) -> Generator[None, None, None]:
    """Context manager for temporarily setting structured log tags."""
    old_tags = _log_tags.get({}).copy()
    try:
        _log_tags.set({**old_tags, **tags})
        yield
    finally:
        _log_tags.set(old_tags)


class QueueHandler(logging.Handler):
    """Pipes log records into an asyncio queue (as JSON log events)."""

    def __init__(self, queue: asyncio.Queue, loop: asyncio.AbstractEventLoop | None = None) -> None:
        super().__init__()
        self.queue = queue
        self.loop = loop or asyncio.get_event_loop()
        self.setFormatter(JSONLFormatter())

    def emit(self, record: logging.LogRecord) -> None:
        try:
            msg = self.format(record)
            self.loop.call_soon_threadsafe(self.queue.put_nowait, {"type": "log", "data": msg})
        except Exception:
            self.handleError(record)
