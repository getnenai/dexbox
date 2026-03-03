"""Unit tests for dexbox.computer (Computer / Drive / DriveFile SDK)."""

from __future__ import annotations

from unittest.mock import patch

import pytest

from dexbox.computer import Computer, Drive, DriveFile
from dexbox.exceptions import RPCError

# ============================================================================
# Computer tests
# ============================================================================


class TestComputer:
    """Tests for Computer class."""

    def test_drive_path_returns_drive(self) -> None:
        """drive(path=...) returns a Drive with the correct path."""
        c = Computer()
        d = c.drive(path="~/Downloads")
        assert isinstance(d, Drive)
        assert d._path == "~/Downloads"

    def test_drive_no_path_raises(self) -> None:
        """Passing no path raises TypeError (missing required keyword argument)."""
        c = Computer()
        with pytest.raises(TypeError):
            c.drive()  # type: ignore[call-arg]

    def test_type_success(self) -> None:
        """type() calls keyboard/type with text and interval."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.type("hello world")
        mock_call.assert_called_once_with(
            "/internal/workflow/keyboard/type",
            json={"text": "hello world", "interval": 0.02},
        )

    def test_type_custom_interval(self) -> None:
        """type() forwards a custom interval."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.type("slow", interval=0.1)
        mock_call.assert_called_once_with(
            "/internal/workflow/keyboard/type",
            json={"text": "slow", "interval": 0.1},
        )

    def test_type_failure_raises(self) -> None:
        """type() raises RuntimeError on failure."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": False, "error": "fail"}):
            with pytest.raises(RuntimeError, match="fail"):
                c.type("boom")

    def test_type_secure_value(self) -> None:
        """type(SecureValue(...)) sends secure_value_id instead of text."""
        from dexbox.secure_value import SecureValue

        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.type(SecureValue("login_password"))
        mock_call.assert_called_once_with(
            "/internal/workflow/keyboard/type",
            json={"secure_value_id": "login_password", "interval": 0.02},
        )

    def test_press_success(self) -> None:
        """press() calls keyboard/press with the single key."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.press("Return")
        mock_call.assert_called_once_with(
            "/internal/workflow/keyboard/press",
            json={"key": "Return"},
        )

    def test_press_failure_raises(self) -> None:
        """press() raises RuntimeError on failure."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": False, "error": "oops"}):
            with pytest.raises(RuntimeError, match="oops"):
                c.press("Return")

    def test_hotkey_combo(self) -> None:
        """hotkey() calls keyboard/hotkey with the given keys."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.hotkey("ctrl", "f")
        mock_call.assert_called_once_with(
            "/internal/workflow/keyboard/hotkey",
            json={"keys": ["ctrl", "f"]},
        )

    def test_hotkey_three_keys(self) -> None:
        """hotkey() with three keys."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.hotkey("ctrl", "shift", "s")
        mock_call.assert_called_once_with(
            "/internal/workflow/keyboard/hotkey",
            json={"keys": ["ctrl", "shift", "s"]},
        )

    def test_click_at_success(self) -> None:
        """click_at() calls mouse/click with x, y, and default button."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.click_at(100, 200)
        mock_call.assert_called_once_with(
            "/internal/workflow/mouse/click",
            json={"x": 100, "y": 200, "button": "left"},
        )

    def test_click_at_right_button(self) -> None:
        """click_at() forwards a custom button."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.click_at(50, 50, button="right")
        mock_call.assert_called_once_with(
            "/internal/workflow/mouse/click",
            json={"x": 50, "y": 50, "button": "right"},
        )

    def test_click_at_failure_raises(self) -> None:
        """click_at() raises RuntimeError on failure."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": False, "error": "no mouse"}):
            with pytest.raises(RuntimeError):
                c.click_at(0, 0)

    def test_move_success(self) -> None:
        """move() calls mouse/move with x, y."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.move(500, 300)
        mock_call.assert_called_once_with(
            "/internal/workflow/mouse/move",
            json={"x": 500, "y": 300},
        )

    def test_scroll_default(self) -> None:
        """scroll() with no args scrolls down 3 clicks."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.scroll()
        mock_call.assert_called_once_with(
            "/internal/workflow/mouse/scroll",
            json={"direction": "down", "amount": 3},
        )

    def test_scroll_up(self) -> None:
        """scroll('up', 5) sends the right direction and amount."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.scroll("up", 5)
        mock_call.assert_called_once_with(
            "/internal/workflow/mouse/scroll",
            json={"direction": "up", "amount": 5},
        )

    def test_scroll_with_coordinates(self) -> None:
        """scroll() with x/y adds coordinate keys to the payload."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": True}) as mock_call:
            c.scroll("down", 3, x=400, y=300)
        mock_call.assert_called_once_with(
            "/internal/workflow/mouse/scroll",
            json={"direction": "down", "amount": 3, "x": 400, "y": 300},
        )

    def test_scroll_failure_raises(self) -> None:
        """scroll() raises RuntimeError on failure."""
        c = Computer()
        with patch("dexbox.computer.call", return_value={"success": False, "error": "boom"}):
            with pytest.raises(RuntimeError, match="boom"):
                c.scroll()


# ============================================================================
# Drive tests
# ============================================================================


class TestDrive:
    """Tests for Drive class."""

    def test_files_success(self) -> None:
        """files() calls /drive/files and returns DriveFile objects."""
        drive = Drive(path="/mnt/tmp")
        with patch("dexbox.computer.call") as mock_call:
            mock_call.return_value = {
                "files": [
                    {"name": "a.pdf", "path": "/mnt/tmp", "size": 1024, "modified": 1700000000.0},
                    {"name": "b.pdf", "path": "/mnt/tmp", "size": 2048, "modified": 1700000001.0},
                ]
            }
            files = drive.files()

        assert len(files) == 2
        assert files[0].name == "a.pdf"
        assert files[0].size == 1024
        assert files[1].name == "b.pdf"
        assert files[1].size == 2048
        mock_call.assert_called_once_with(
            "/internal/workflow/drive/files",
            method="GET",
            params={"path": "/mnt/tmp", "pattern": "*"},
        )

    def test_files_with_pattern(self) -> None:
        """files('*.pdf') passes the pattern to the RPC call."""
        drive = Drive(path="/mnt/tmp")
        with patch("dexbox.computer.call", return_value={"files": []}) as mock_call:
            drive.files("*.pdf")
        mock_call.assert_called_once()
        call_kwargs = mock_call.call_args
        assert call_kwargs.kwargs["params"]["pattern"] == "*.pdf"

    def test_files_empty(self) -> None:
        """files() returns empty list when no files match."""
        drive = Drive(path="/mnt/tmp")
        with patch("dexbox.computer.call", return_value={"files": []}):
            files = drive.files()
        assert files == []


# ============================================================================
# DriveFile tests
# ============================================================================


class TestDriveFile:
    """Tests for DriveFile class."""

    def test_read_bytes_success(self) -> None:
        """read_bytes() returns raw bytes from the parent."""
        f = DriveFile(name="report.pdf", path="/mnt/tmp", size=17, modified=1700000000.0)
        with patch("dexbox.computer.call_bytes", return_value=b"PDF content bytes") as mock_bytes:
            data = f.read_bytes()
        assert data == b"PDF content bytes"
        mock_bytes.assert_called_once_with(
            "/internal/workflow/drive/read",
            params={"path": "/mnt/tmp", "filename": "report.pdf"},
        )

    def test_read_bytes_rpc_error_propagates(self) -> None:
        """read_bytes() propagates RPCError from call_bytes."""
        f = DriveFile(name="missing.pdf", path="/mnt/tmp", size=0, modified=0)
        with patch("dexbox.computer.call_bytes", side_effect=RPCError("not found")):
            with pytest.raises(RPCError, match="not found"):
                f.read_bytes()

    def test_read_text_success(self) -> None:
        """read_text() returns file content decoded as a UTF-8 string."""
        f = DriveFile(name="notes.txt", path="/mnt/tmp", size=11, modified=1700000000.0)
        with patch("dexbox.computer.call_bytes", return_value=b"hello world"):
            text = f.read_text()
        assert text == "hello world"
        assert isinstance(text, str)

    def test_read_text_custom_encoding(self) -> None:
        """read_text(encoding=...) decodes using the given encoding."""
        content = "café".encode("latin-1")
        f = DriveFile(name="notes.txt", path="/mnt/tmp", size=len(content), modified=1700000000.0)
        with patch("dexbox.computer.call_bytes", return_value=content):
            text = f.read_text(encoding="latin-1")
        assert text == "café"

    def test_read_text_delegates_to_read_bytes(self) -> None:
        """read_text() fetches bytes via read_bytes() and decodes them."""
        f = DriveFile(name="data.csv", path="/mnt/tmp", size=5, modified=1700000000.0)
        with patch.object(f, "read_bytes", return_value=b"a,b,c") as mock_rb:
            result = f.read_text()
        mock_rb.assert_called_once_with()
        assert result == "a,b,c"
