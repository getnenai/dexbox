"""Tests for sandbox orchestrator — ported from cup."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from dexbox.sandbox import (
    SANDBOX_RUNTIMES,
    SandboxOrchestrator,
    SandboxRuntime,
    WorkflowResult,
    get_runtime_for_extension,
)
from dexbox.session import SessionManager


class TestSandboxRuntime:
    def test_sandbox_runtime_is_frozen(self) -> None:
        runtime = SandboxRuntime(image_env_var="TEST", default_image="test:latest", command_prefix=("-c",))
        with pytest.raises(Exception):
            runtime.image_env_var = "CHANGED"  # type: ignore

    def test_sandbox_runtime_image_uses_env_var(self) -> None:
        runtime = SandboxRuntime(image_env_var="TEST_IMAGE_VAR", default_image="default:latest", command_prefix=("-c",))
        with patch.dict("os.environ", {"TEST_IMAGE_VAR": "custom:v1"}):
            assert runtime.image == "custom:v1"

    def test_sandbox_runtime_image_uses_default(self) -> None:
        runtime = SandboxRuntime(
            image_env_var="NONEXISTENT_12345", default_image="fallback:latest", command_prefix=("-c",)
        )
        assert runtime.image == "fallback:latest"

    def test_python_runtime_registered(self) -> None:
        assert ".py" in SANDBOX_RUNTIMES
        assert SANDBOX_RUNTIMES[".py"].command_prefix == ("-c",)


class TestGetRuntimeForExtension:
    def test_returns_python_runtime_for_py(self) -> None:
        assert get_runtime_for_extension(".py") is not None

    def test_returns_none_for_unknown(self) -> None:
        assert get_runtime_for_extension(".xyz") is None

    def test_case_insensitive(self) -> None:
        assert get_runtime_for_extension(".PY") is not None


class TestSandboxOrchestrator:
    @pytest.fixture
    def session_manager(self) -> SessionManager:
        return SessionManager()

    @pytest.fixture
    def orchestrator(self, session_manager: SessionManager) -> SandboxOrchestrator:
        return SandboxOrchestrator(session_manager)

    def test_terminate_no_container(self, orchestrator: SandboxOrchestrator) -> None:
        assert orchestrator.terminate_active_container() is False

    def test_terminate_kills_container(self, orchestrator: SandboxOrchestrator) -> None:
        mock = MagicMock()
        mock.short_id = "abc123"
        orchestrator._active_container = mock
        assert orchestrator.terminate_active_container() is True
        mock.kill.assert_called_once()
        mock.remove.assert_called_once()
        assert orchestrator._active_container is None

    def test_terminate_handles_kill_error(self, orchestrator: SandboxOrchestrator) -> None:
        mock = MagicMock()
        mock.short_id = "abc123"
        mock.kill.side_effect = Exception("already stopped")
        orchestrator._active_container = mock
        assert orchestrator.terminate_active_container() is False
        assert orchestrator._active_container is None

    def test_cancel_sets_flag(self, orchestrator: SandboxOrchestrator) -> None:
        assert orchestrator._cancel_requested is False
        orchestrator.cancel_sandbox()
        assert orchestrator._cancel_requested is True

    def test_terminate_also_terminates_session(
        self, orchestrator: SandboxOrchestrator, session_manager: SessionManager
    ) -> None:
        token = session_manager.create_session(api_key="sk", model="m")
        mock = MagicMock()
        mock.short_id = "abc"
        orchestrator._active_container = mock
        orchestrator._active_session_token = token
        assert session_manager.get_session(token) is not None
        orchestrator.terminate_active_container()
        assert session_manager.get_session(token) is None

    @pytest.mark.asyncio
    async def test_execute_creates_and_cleans_session(self, orchestrator: SandboxOrchestrator) -> None:
        with patch.object(orchestrator, "_run_sandbox", new_callable=AsyncMock) as mock_run:
            mock_run.return_value = WorkflowResult(success=True, exit_code=0, logs="OK")
            result = await orchestrator.execute_workflow(script="print('hi')", api_key="sk")
        assert result.success is True
        assert orchestrator.session_manager.active_session_count == 0

    @pytest.mark.asyncio
    async def test_execute_cleans_up_on_error(self, orchestrator: SandboxOrchestrator) -> None:
        with patch.object(orchestrator, "_run_sandbox", new_callable=AsyncMock) as mock_run:
            mock_run.side_effect = Exception("container failed")
            result = await orchestrator.execute_workflow(script="print('hi')", api_key="sk")
        assert result.success is False
        assert orchestrator.session_manager.active_session_count == 0


class TestWorkflowResult:
    def test_success_result(self) -> None:
        result = WorkflowResult(success=True, exit_code=0, logs="done")
        assert result.success is True
        assert result.error is None

    def test_failure_result(self) -> None:
        result = WorkflowResult(success=False, exit_code=1, logs="err", error="Script failed")
        assert result.success is False
        assert result.error == "Script failed"
