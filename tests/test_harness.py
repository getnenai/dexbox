"""Unit tests for the python.py sandbox harness script.

Ports the harness tests from cup/python/cup/workflow_handler/sandbox_harness/python_test.py,
adapted for the dexbox harness location at src/dexbox/harness/python.py.
"""

import json
import logging
from pathlib import Path
from unittest.mock import MagicMock, patch

import httpx
import pytest

HARNESS_PATH = Path(__file__).parent.parent / "src" / "dexbox" / "harness" / "python.py"


@pytest.fixture
def harness_code() -> str:
    """Read the harness script content."""
    return HARNESS_PATH.read_text()


@pytest.fixture(autouse=True)
def isolate_harness_globals():
    """Isolate the test process from harness global mutations.

    The harness modifies sys.stdout, sys.stderr, and logging handlers.
    We patch them so it doesn't break pytest's output capture.
    """
    with (
        patch("sys.stdout"),
        patch("sys.stderr"),
        patch.object(logging.root, "addHandler"),
        patch.object(logging.root, "setLevel"),
        patch("os.chdir"),
        patch(
            "os.environ",
            {"DEXBOX_SESSION_TOKEN": "test-tk", "DEXBOX_PARENT_URL": "http://mock"},
        ),
        patch("sys.exit") as mock_exit,
    ):
        # We must make sys.exit actually halt the exec() evaluation
        mock_exit.side_effect = SystemExit
        yield mock_exit


def _run_harness(code: str, mock_get_resp: dict) -> MagicMock:
    """Execute the harness with a mocked /load endpoint and return the mocked POST /output."""
    mock_get = MagicMock()
    mock_resp = MagicMock(spec=httpx.Response)
    mock_resp.json.return_value = mock_get_resp
    mock_resp.raise_for_status.return_value = None
    mock_get.return_value = mock_resp

    mock_post = MagicMock()
    mock_post_resp = MagicMock(spec=httpx.Response)
    mock_post_resp.raise_for_status.return_value = None
    mock_post.return_value = mock_post_resp

    # Disable file archiving by mocking os.listdir to return empty list
    with (
        patch("httpx.get", mock_get),
        patch("httpx.post", mock_post),
        patch("os.listdir", return_value=[]),
    ):
        namespace = {}
        try:
            exec(code, namespace)  # noqa: S102
        except SystemExit:
            pass  # Expected when harness calls sys.exit()

    return mock_post


def test_successful_execution(harness_code: str) -> None:
    """Test standard successful workflow execution and output submission."""
    user_code = """
from pydantic import BaseModel

class Input(BaseModel):
    name: str

class Output(BaseModel):
    greeting: str

def run(input: Input) -> Output:
    return Output(greeting=f"Hello {input.name}")
"""
    mock_post = _run_harness(harness_code, {"data": {"name": "World"}, "code": user_code})

    assert mock_post.call_count >= 1
    post_args, post_kwargs = mock_post.call_args_list[0]
    assert post_args[0] == "http://mock/internal/workflow/output"

    payload = json.loads(post_kwargs["content"])
    assert payload["success"] is True
    assert payload["result"] == {"greeting": "Hello World"}


def test_validation_error(harness_code: str, isolate_harness_globals: MagicMock) -> None:
    """Test that invalid input data produces a structured validation error."""
    user_code = """
from pydantic import BaseModel
class Input(BaseModel):
    age: int
class Output(BaseModel):
    val: str
def run(input: Input) -> Output:
    return Output(val="ok")
"""
    mock_post = _run_harness(
        harness_code,
        {
            "data": {"age": "not-a-number"},  # Invalid int
            "code": user_code,
        },
    )

    # Harness should exit with 1 on validation failure
    isolate_harness_globals.assert_called_with(1)

    payload = json.loads(mock_post.call_args_list[0][1]["content"])
    assert payload["success"] is False
    assert "ValidationError" in payload["error"]
    assert "age" in payload["error"]


def test_user_runtime_error(harness_code: str, isolate_harness_globals: MagicMock) -> None:
    """Test that an unhandled exception in run() is properly caught and reported."""
    user_code = """
from pydantic import BaseModel
class Input(BaseModel): pass
class Output(BaseModel): pass
def run(input: Input) -> Output:
    raise ValueError("Oops, user code failed")
"""
    mock_post = _run_harness(harness_code, {"data": {}, "code": user_code})

    isolate_harness_globals.assert_called_with(1)

    payload = json.loads(mock_post.call_args_list[0][1]["content"])
    assert payload["success"] is False
    assert "Oops, user code failed" in payload["error"]
    assert "ValueError" in payload["error"]


def test_syntax_error_in_user_code(harness_code: str, isolate_harness_globals: MagicMock) -> None:
    """Test that a syntax error during dynamic compile/exec is caught and reported."""
    user_code = """
from pydantic import BaseModel
class Input(BaseModel):
    name: str

class Output(BaseModel)  # Missing colon
    pass

def run(input: Input) -> Output:
    pass
"""
    mock_post = _run_harness(harness_code, {"data": {}, "code": user_code})

    isolate_harness_globals.assert_called_with(1)

    payload = json.loads(mock_post.call_args_list[0][1]["content"])
    assert payload["success"] is False
    assert "SyntaxError" in payload["error"]


def test_missing_run_function(harness_code: str, isolate_harness_globals: MagicMock) -> None:
    """Test that the harness rejects scripts missing a run() function."""
    user_code = """
from pydantic import BaseModel
# Forgot to define run()
"""
    mock_post = _run_harness(harness_code, {"data": {}, "code": user_code})

    isolate_harness_globals.assert_called_with(1)

    payload = json.loads(mock_post.call_args_list[0][1]["content"])
    assert payload["success"] is False
    assert "RuntimeError" in payload["error"]
    assert "run" in payload["error"].lower()
