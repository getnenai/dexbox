"""Tests for session management"""

from __future__ import annotations

import time

import pytest

from dexbox.session import SESSION_TIMEOUT_SECONDS, SessionManager, WorkflowSession


class TestWorkflowSession:
    def test_session_creation(self) -> None:
        session = WorkflowSession(token="test-token", api_key="sk-test", model="claude-sonnet-4", provider="anthropic")
        assert session.token == "test-token"
        assert session.api_key == "sk-test"
        assert not session.is_expired()

    def test_session_expiration(self) -> None:
        session = WorkflowSession(
            token="test-token",
            api_key="sk-test",
            model="claude-sonnet-4",
            created_at=time.time() - SESSION_TIMEOUT_SECONDS - 1,
        )
        assert session.is_expired()


class TestSessionManager:
    def test_create_session_returns_token(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        assert len(token) > 20
        assert manager.active_session_count == 1

    def test_get_session_returns_session(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        session = manager.get_session(token)
        assert session is not None
        assert session.api_key == "sk-test"

    def test_get_session_invalid_token_returns_none(self) -> None:
        manager = SessionManager()
        assert manager.get_session("invalid") is None

    def test_get_session_expired_returns_none(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        manager._sessions[token].created_at = time.time() - SESSION_TIMEOUT_SECONDS - 1
        assert manager.get_session(token) is None
        assert manager.active_session_count == 0

    def test_delete_session(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        assert manager.delete_session(token) is True
        assert manager.get_session(token) is None
        assert manager.delete_session(token) is False

    def test_terminate_session(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4", workflow_id="wf-123")
        assert not manager._sessions[token].terminated
        result = manager.terminate_session(token)
        assert result is True
        assert manager.get_session(token) is None
        assert manager._sessions[token].terminated is True

    def test_terminate_invalid_session(self) -> None:
        manager = SessionManager()
        assert manager.terminate_session("invalid") is False

    def test_get_session_status_valid(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        session, error = manager.get_session_status(token)
        assert session is not None
        assert error is None

    def test_get_session_status_not_found(self) -> None:
        manager = SessionManager()
        session, error = manager.get_session_status("invalid")
        assert session is None
        assert error == "not_found"

    def test_get_session_status_terminated(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        manager.terminate_session(token)
        session, error = manager.get_session_status(token)
        assert session is None
        assert error == "terminated"

    def test_get_session_status_expired(self) -> None:
        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        manager._sessions[token].created_at = time.time() - SESSION_TIMEOUT_SECONDS - 1
        session, error = manager.get_session_status(token)
        assert session is None
        assert error == "expired"

    @pytest.mark.asyncio
    async def test_terminate_session_cancels_tasks(self) -> None:
        import asyncio

        manager = SessionManager()
        token = manager.create_session(api_key="sk-test", model="claude-sonnet-4")
        session = manager.get_session(token)

        async def long_task():
            try:
                await asyncio.sleep(10)
            except asyncio.CancelledError:
                pass

        task = asyncio.create_task(long_task())
        session.register_task(task)
        assert not task.done()
        manager.terminate_session(token)
        await asyncio.sleep(0.01)
        assert task.done()
