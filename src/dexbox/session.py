"""Session manager — tracks active workflow sessions"""

from __future__ import annotations

import asyncio
import logging
import secrets
import time
from dataclasses import dataclass, field
from typing import Any

logger = logging.getLogger("dexbox.session")

SESSION_TIMEOUT_SECONDS = 3600  # 1 hour


@dataclass
class WorkflowSession:
    """State for a single workflow execution."""

    token: str
    api_key: str
    model: str
    provider: str = "anthropic"
    anthropic_base_url: str | None = None
    workflow_id: str = ""
    variables: dict[str, Any] = field(default_factory=dict)
    secure_params: dict[str, str] = field(default_factory=dict)
    artifacts_dir: str | None = None
    created_at: float = field(default_factory=time.time)
    terminated: bool = False

    # Set by orchestrator during execution
    input_data: dict[str, Any] | None = None
    code: str | None = None
    output_data: dict[str, Any] | None = None
    assets_archive: bytes | None = None

    # Tool collection is set lazily by internal_routes
    _tool_collection: Any = field(default=None, repr=False)

    # Active async tasks for cancellation
    _active_tasks: set[asyncio.Task] = field(default_factory=set, repr=False)

    # Event queue for real-time progress streaming
    event_queue: asyncio.Queue = field(default_factory=asyncio.Queue, repr=False)

    def is_expired(self) -> bool:
        return time.time() - self.created_at > SESSION_TIMEOUT_SECONDS

    def register_task(self, task: asyncio.Task) -> None:
        """Register an async task for lifecycle management."""
        self._active_tasks.add(task)
        task.add_done_callback(self._active_tasks.discard)

    def cancel_all_tasks(self) -> int:
        """Cancel all active tasks. Returns number cancelled."""
        tasks = list(self._active_tasks)
        for task in tasks:
            task.cancel()
        return len(tasks)


class SessionManager:
    """Manages active workflow sessions."""

    def __init__(self) -> None:
        self._sessions: dict[str, WorkflowSession] = {}

    @property
    def active_session_count(self) -> int:
        return sum(1 for s in self._sessions.values() if not s.is_expired() and not s.terminated)

    def create_session(
        self,
        *,
        api_key: str,
        model: str,
        provider: str = "anthropic",
        anthropic_base_url: str | None = None,
        workflow_id: str = "",
        variables: dict[str, Any] | None = None,
        secure_params: dict[str, str] | None = None,
        artifacts_dir: str | None = None,
    ) -> str:
        """Create a session and return its token."""
        token = secrets.token_urlsafe(32)
        session = WorkflowSession(
            token=token,
            api_key=api_key,
            model=model,
            provider=provider,
            anthropic_base_url=anthropic_base_url,
            workflow_id=workflow_id,
            variables=variables or {},
            secure_params=secure_params or {},
            artifacts_dir=artifacts_dir,
        )
        self._sessions[token] = session
        logger.info("Created session %s for workflow %s", token[:8], workflow_id)
        self._cleanup_expired()
        return token

    def get_session(self, token: str) -> WorkflowSession | None:
        """Return session if valid (not expired/terminated), else None."""
        session = self._sessions.get(token)
        if session is None:
            return None
        if session.terminated:
            return None
        if session.is_expired():
            del self._sessions[token]
            return None
        return session

    def get_session_status(self, token: str) -> tuple[WorkflowSession | None, str | None]:
        """Return (session, error) where error is 'not_found'|'terminated'|'expired'|None."""
        session = self._sessions.get(token)
        if session is None:
            return None, "not_found"
        if session.terminated:
            return None, "terminated"
        if session.is_expired():
            return None, "expired"
        return session, None

    def delete_session(self, token: str) -> bool:
        """Delete session. Returns True if it existed."""
        if token in self._sessions:
            del self._sessions[token]
            return True
        return False

    def terminate_session(self, token: str) -> bool:
        """Mark session as terminated and cancel all tasks."""
        session = self._sessions.get(token)
        if session is None:
            return False
        session.terminated = True
        session.cancel_all_tasks()
        logger.info("Terminated session %s", token[:8])
        return True

    def _cleanup_expired(self) -> None:
        expired = [t for t, s in self._sessions.items() if s.is_expired()]
        for token in expired:
            del self._sessions[token]
        if expired:
            logger.debug("Cleaned up %d expired sessions", len(expired))
