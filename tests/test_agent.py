"""Unit tests for dexbox.agent and dexbox.rpc."""

from __future__ import annotations

from unittest.mock import Mock, patch

import pytest

from dexbox import Agent
from dexbox.exceptions import RPCError

# ============================================================================
# Agent.execute() tests
# ============================================================================


def test_agent_execute_success() -> None:
    """Agent.execute() returns the response dict on success."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {
            "success": True,
            "messages": [{"role": "assistant", "content": []}],
        }
        agent = Agent()
        result = agent.execute("Click the submit button")

    assert result["success"] is True
    mock_call.assert_called_once_with(
        "/internal/workflow/execute",
        json={"instruction": "Click the submit button", "max_iterations": 10},
    )


def test_agent_execute_failure_raises() -> None:
    """Agent.execute() raises RuntimeError on failure."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": False, "error": "VLM failed"}
        agent = Agent()
        with pytest.raises(RuntimeError, match="VLM failed"):
            agent.execute("Click button")


def test_agent_execute_with_custom_max_iterations() -> None:
    """Agent.execute() forwards max_iterations."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True}
        agent = Agent()
        agent.execute("Do something", max_iterations=5)
    mock_call.assert_called_once_with(
        "/internal/workflow/execute",
        json={"instruction": "Do something", "max_iterations": 5},
    )


# ============================================================================
# Agent.verify() tests
# ============================================================================


def test_agent_verify_returns_true() -> None:
    """Agent.verify() returns True when is_valid is True."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True, "is_valid": True, "reason": "Match"}
        agent = Agent()
        result = agent.verify("Is page loaded?")
    assert result is True


def test_agent_verify_returns_false() -> None:
    """Agent.verify() returns False when is_valid is False."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True, "is_valid": False, "reason": "No match"}
        agent = Agent()
        result = agent.verify("Is error visible?")
    assert result is False


def test_agent_verify_with_custom_timeout() -> None:
    """Agent.verify() with custom timeout."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True, "is_valid": True}
        agent = Agent()
        agent.verify("Is page loaded?", timeout=30)
    mock_call.assert_called_once_with(
        "/internal/workflow/validate",
        json={"question": "Is page loaded?", "timeout": 30},
    )


def test_agent_verify_failure_raises() -> None:
    """Agent.verify() raises RuntimeError when success is False."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": False, "error": "screenshot failed"}
        agent = Agent()
        with pytest.raises(RuntimeError, match="screenshot failed"):
            agent.verify("Is page loaded?")


# ============================================================================
# Agent.extract() tests
# ============================================================================


def test_agent_extract_success() -> None:
    """Agent.extract() returns the extracted data on success."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {
            "success": True,
            "data": ["Alice", "Bob", "Carol"],
        }
        agent = Agent()
        result = agent.extract("Extract patient names", schema={"type": "array", "items": {"type": "string"}})
    assert result == ["Alice", "Bob", "Carol"]


def test_agent_extract_failure_raises() -> None:
    """Agent.extract() raises RuntimeError on failure."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": False, "error": "No data found"}
        agent = Agent()
        with pytest.raises(RuntimeError, match="No data found"):
            agent.extract("Extract data", schema={"type": "object"})


def test_agent_extract_forwards_schema() -> None:
    """Agent.extract() properly forwards the schema argument."""
    original_schema = {"type": "object", "properties": {"name": {"type": "string"}}}

    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True, "data": {"name": "Test"}}
        agent = Agent()
        agent.extract("Get name", schema=original_schema)

    args, kwargs = mock_call.call_args
    payload = kwargs.get("json") or args[1]
    assert payload.get("schema_def") == original_schema
    # Ensure the schema was not mutated
    assert original_schema == {"type": "object", "properties": {"name": {"type": "string"}}}


# ============================================================================
# Agent model override tests
# ============================================================================


def test_agent_model_override_added_to_execute_payload() -> None:
    """Agent(model=...) adds model_override key to execute payload."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True}
        agent = Agent(model="claude-sonnet-4-5-20250929")
        agent.execute("Do something")
    args, kwargs = mock_call.call_args
    payload = kwargs.get("json") or args[1]
    assert payload.get("model_override") == "claude-sonnet-4-5-20250929"


def test_agent_model_override_added_to_verify_payload() -> None:
    """Agent(model=...) adds model_override key to verify payload."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True, "is_valid": True}
        agent = Agent(model="claude-haiku-4")
        agent.verify("Is page loaded?")
    args, kwargs = mock_call.call_args
    payload = kwargs.get("json") or args[1]
    assert payload.get("model_override") == "claude-haiku-4"


def test_agent_no_model_override_omits_key() -> None:
    """Agent() without model does not include model_override in payload."""
    with patch("dexbox.agent.call") as mock_call:
        mock_call.return_value = {"success": True}
        agent = Agent()
        agent.execute("Do something")
    args, kwargs = mock_call.call_args
    payload = kwargs.get("json") or args[1]
    assert "model_override" not in payload


# ============================================================================
# RPC error handling tests
# ============================================================================


def test_rpc_error_propagates_from_call() -> None:
    """RPCError from dexbox.rpc.call propagates through Agent methods."""
    with patch("dexbox.agent.call", side_effect=RPCError("connection refused")):
        agent = Agent()
        with pytest.raises(RPCError, match="connection refused"):
            agent.execute("Do something")


def test_rpc_call_raises_on_http_error() -> None:
    """dexbox.rpc.call raises RPCError on HTTP error status codes."""
    import httpx

    with patch("dexbox.rpc.httpx.request") as mock_request:
        mock_response = Mock(spec=httpx.Response)
        mock_response.status_code = 500
        mock_response.text = "Internal Server Error"
        mock_response.raise_for_status.side_effect = httpx.HTTPStatusError(
            "500", request=Mock(), response=mock_response
        )
        mock_request.return_value = mock_response

        from dexbox.rpc import call

        with pytest.raises(RPCError):
            call("/test", json={})


def test_rpc_call_raises_on_connection_error() -> None:
    """dexbox.rpc.call raises RPCError on connection failure."""
    import httpx

    with patch("dexbox.rpc.httpx.request", side_effect=httpx.ConnectError("refused")):
        from dexbox.rpc import call

        with pytest.raises(RPCError, match="refused"):
            call("/test", json={})
