"""Tests for internal RPC routes — ported from cup."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from dexbox.routes import router, set_session_manager
from dexbox.session import SessionManager, WorkflowSession


def _build_session(tool_collection=None) -> WorkflowSession:
    session = WorkflowSession(token="test-token", api_key="test-key", model="claude-sonnet-4", provider="anthropic")
    if tool_collection is not None:
        session._tool_collection = tool_collection
    return session


@pytest.fixture
def mock_manager() -> MagicMock:
    manager = MagicMock(spec=SessionManager)
    tc = MagicMock()
    tc.run = AsyncMock(return_value=MagicMock(error=None))
    session = _build_session(tool_collection=tc)
    manager.get_session.return_value = session
    manager.get_session_status.return_value = (session, None)
    return manager


@pytest.fixture
def client(mock_manager: MagicMock) -> TestClient:
    app = FastAPI()
    app.include_router(router)
    set_session_manager(mock_manager)
    return TestClient(app)


def test_execute_requires_token(client: TestClient) -> None:
    response = client.post("/internal/workflow/execute", json={"instruction": "test"})
    assert response.status_code == 422


def test_execute_success(client: TestClient) -> None:
    with patch(
        "dexbox.routes.sampling_loop",
        new=AsyncMock(return_value=([{"role": "assistant", "content": []}], None)),
    ):
        response = client.post(
            "/internal/workflow/execute",
            json={"instruction": "click the button", "max_iterations": 2},
            headers={"X-Session-Token": "test-token"},
        )
    assert response.status_code == 200
    assert response.json()["success"] is True


def test_load_endpoint(client: TestClient) -> None:
    response = client.get("/internal/workflow/load", headers={"X-Session-Token": "test-token"})
    assert response.status_code == 200
    assert response.json() == {"data": {}, "code": None}


def test_set_output_endpoint(client: TestClient) -> None:
    response = client.post(
        "/internal/workflow/output",
        json={"success": True, "result": {"key": "value"}},
        headers={"X-Session-Token": "test-token"},
    )
    assert response.status_code == 200
    assert response.json() == {"ok": True}


@pytest.mark.parametrize(
    "path,payload",
    [
        ("/internal/workflow/keyboard/type", {"text": "hello"}),
        ("/internal/workflow/keyboard/press", {"key": "Return"}),
        ("/internal/workflow/keyboard/hotkey", {"keys": ["ctrl", "c"]}),
    ],
)
def test_keyboard_endpoints(client: TestClient, path: str, payload: dict) -> None:
    response = client.post(path, json=payload, headers={"X-Session-Token": "test-token"})
    assert response.status_code == 200
    assert response.json()["success"] is True


def test_terminated_session_rejects_rpc() -> None:
    manager = SessionManager()
    token = manager.create_session(api_key="sk", model="m")
    app = FastAPI()
    app.include_router(router)
    set_session_manager(manager)
    c = TestClient(app)
    manager.terminate_session(token)
    response = c.post("/internal/workflow/keyboard/press", json={"key": "a"}, headers={"X-Session-Token": token})
    assert response.status_code == 401
    assert "terminated" in response.json()["detail"].lower()


def test_expired_session_rejects_rpc() -> None:
    import time

    from dexbox.session import SESSION_TIMEOUT_SECONDS

    manager = SessionManager()
    token = manager.create_session(api_key="sk", model="m")
    manager._sessions[token].created_at = time.time() - SESSION_TIMEOUT_SECONDS - 1
    app = FastAPI()
    app.include_router(router)
    set_session_manager(manager)
    c = TestClient(app)
    response = c.post("/internal/workflow/keyboard/press", json={"key": "a"}, headers={"X-Session-Token": token})
    assert response.status_code == 401
    assert "expired" in response.json()["detail"].lower()


def test_drive_files_rejects_disallowed_path(client: TestClient) -> None:
    with patch("dexbox.routes.os.path.realpath", side_effect=lambda p: p):
        response = client.get(
            "/internal/workflow/drive/files",
            params={"path": "/etc/passwd"},
            headers={"X-Session-Token": "test-token"},
        )
    assert response.status_code == 403


def test_drive_files_rejects_traversal_pattern(client: TestClient) -> None:
    with patch("dexbox.routes._validate_drive_path", return_value="/mnt/tmp"):
        for bad in ["../../etc/shadow", "../foo", "sub/bar.pdf"]:
            response = client.get(
                "/internal/workflow/drive/files",
                params={"path": "/mnt/tmp", "pattern": bad},
                headers={"X-Session-Token": "test-token"},
            )
            assert response.status_code == 400


def test_secure_value_keyboard_type() -> None:
    manager = MagicMock(spec=SessionManager)
    tc = MagicMock()
    tc.run = AsyncMock(return_value=MagicMock(error=None))
    session = WorkflowSession(token="t", api_key="k", model="m", secure_params={"pwd": "s3cr3t"})
    session._tool_collection = tc
    manager.get_session.return_value = session
    manager.get_session_status.return_value = (session, None)
    app = FastAPI()
    app.include_router(router)
    set_session_manager(manager)
    c = TestClient(app)
    response = c.post(
        "/internal/workflow/keyboard/type", json={"secure_value_id": "pwd"}, headers={"X-Session-Token": "t"}
    )
    assert response.status_code == 200
    assert response.json()["success"] is True
    tc.run.assert_called_once()
    call_kwargs = tc.run.call_args
    assert call_kwargs.kwargs["tool_input"]["text"] == "s3cr3t"


def test_set_assets_endpoint(client: TestClient) -> None:
    payload = b"test-tar-content"
    response = client.post(
        "/internal/workflow/assets",
        content=payload,
        headers={"X-Session-Token": "test-token", "Content-Type": "application/octet-stream"},
    )
    assert response.status_code == 200
    assert response.status_code == 200
    assert response.json() == {"ok": True}


def test_validate_success(client: TestClient) -> None:
    with patch("dexbox.routes.UIPerceptionClient") as mock_client_cls:
        mock_client = mock_client_cls.return_value
        mock_client.take_screenshot = AsyncMock(return_value="fake-b64")
        mock_client.is_vlm_match = AsyncMock(return_value={"is_match": True, "match_reason": "Looks good"})

        response = client.post(
            "/internal/workflow/validate",
            json={"question": "Is it blue?"},
            headers={"X-Session-Token": "test-token"},
        )
    assert response.status_code == 200
    data = response.json()
    assert data["success"] is True
    assert data["is_valid"] is True
    assert data["reason"] == "Looks good"


def test_extract_success(client: TestClient) -> None:
    # Need to mock both UIPerceptionClient and Anthropic client
    with patch("dexbox.routes.UIPerceptionClient") as mock_perc_cls, patch("anthropic.Anthropic") as mock_anth_cls:
        mock_perc = mock_perc_cls.return_value
        mock_perc.take_screenshot = AsyncMock(return_value="fake-b64")

        mock_anth = mock_anth_cls.return_value
        mock_msg = MagicMock()
        mock_msg.content = [MagicMock(text='{"result": "extracted-data"}')]
        mock_anth.beta.messages.create.return_value = mock_msg

        response = client.post(
            "/internal/workflow/extract",
            json={"query": "Get data", "schema_def": {"type": "object"}},
            headers={"X-Session-Token": "test-token"},
        )
    assert response.status_code == 200
    data = response.json()
    assert data["success"] is True
    assert data["data"] == {"result": "extracted-data"}


def test_validate_failure(client: TestClient) -> None:
    with patch("dexbox.routes.UIPerceptionClient") as mock_client_cls:
        mock_client = mock_client_cls.return_value
        mock_client.take_screenshot = AsyncMock(side_effect=Exception("Screenshot failed"))

        response = client.post(
            "/internal/workflow/validate",
            json={"question": "Is it blue?"},
            headers={"X-Session-Token": "test-token"},
        )
    assert response.status_code == 200
    data = response.json()
    assert data["success"] is False
    assert "Screenshot failed" in data["error"]


def test_extract_failure(client: TestClient) -> None:
    with patch("dexbox.routes.UIPerceptionClient") as mock_perc_cls:
        mock_perc = mock_perc_cls.return_value
        mock_perc.take_screenshot = AsyncMock(side_effect=Exception("API error"))

        response = client.post(
            "/internal/workflow/extract",
            json={"query": "Get data", "schema_def": {"type": "object"}},
            headers={"X-Session-Token": "test-token"},
        )
    assert response.status_code == 200
    data = response.json()
    assert data["success"] is False
    assert "API error" in data["error"]
