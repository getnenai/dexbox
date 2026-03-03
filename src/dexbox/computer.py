"""Computer — host environment interaction from within the sandbox.

Provides keyboard input, mouse control, and filesystem (Drive) access.
All operations are proxied through RPC to the parent service which
executes them on the desktop container.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any

from dexbox.rpc import call, call_bytes
from dexbox.secure_value import SecureValue


@dataclass
class DriveFile:
    """A file accessible through the Drive interface."""

    name: str
    path: str
    size: int
    modified: float

    def read_bytes(self) -> bytes:
        """Read the file contents as bytes."""
        return call_bytes(
            "/internal/workflow/drive/read",
            params={"path": self.path, "filename": self.name},
        )

    def read_text(self, encoding: str = "utf-8", errors: str = "strict", newline: str | None = None) -> str:
        """Read the file contents as text.

        Args:
            encoding: Text encoding to use (default: utf-8).
            errors: Error handling mode (default: strict).
            newline: Newline translation mode (default: None — universal newlines).
        """
        raw = self.read_bytes()
        text = raw.decode(encoding, errors=errors)
        if newline is not None:
            text = text.replace("\r\n", "\n").replace("\r", "\n")
            if newline != "\n":
                text = text.replace("\n", newline)
        return text


class Drive:
    """Filesystem access to host-mounted paths.

    Example::

        drive = computer.drive("/mnt/tmp")
        files = drive.files("*.pdf")
        for f in files:
            content = f.read_bytes()
    """

    def __init__(self, path: str) -> None:
        self._path = path

    def files(self, pattern: str = "*") -> list[DriveFile]:
        """List files matching a glob pattern.

        Args:
            pattern: Glob pattern (e.g. ``*.pdf``, ``report_*``).

        Returns:
            List of DriveFile instances.
        """
        result = call(
            "/internal/workflow/drive/files",
            method="GET",
            params={"path": self._path, "pattern": pattern},
        )
        return [
            DriveFile(
                name=f["name"],
                path=f["path"],
                size=f["size"],
                modified=f["modified"],
            )
            for f in result.get("files", [])
        ]


class Computer:
    """Interface for host environment interaction.

    Provides keyboard, mouse, and filesystem operations. All operations
    are proxied through RPC to the parent service.

    Example::

        from dexbox import Computer

        computer = Computer()
        computer.type("Hello, world!")
        computer.press("Return")
        computer.click_at(100, 200)
    """

    def __init__(self) -> None:
        self._parent_url = os.environ.get("DEXBOX_PARENT_URL", "http://172.17.0.1:8600")

    # ----- Keyboard -----

    def type(self, text: str | SecureValue, *, interval: float = 0.02) -> None:
        """Type text character by character.

        Args:
            text: Text to type, or a SecureValue reference for sensitive input.
            interval: Delay between keystrokes in seconds.
        """
        if isinstance(text, SecureValue):
            payload: dict[str, Any] = {
                "secure_value_id": text.identifier,
                "interval": interval,
            }
        else:
            payload = {"text": text, "interval": interval}

        result = call("/internal/workflow/keyboard/type", json=payload)
        if not result.get("success"):
            raise RuntimeError(f"type() failed: {result.get('error', 'unknown error')}")

    def press(self, key: str) -> None:
        """Press a single key.

        Args:
            key: Key name (e.g. ``Return``, ``Tab``, ``Escape``).
        """
        result = call("/internal/workflow/keyboard/press", json={"key": key})
        if not result.get("success"):
            raise RuntimeError(f"press() failed: {result.get('error', 'unknown error')}")

    def hotkey(self, *keys: str) -> None:
        """Press a key combination.

        Args:
            keys: Key names to press simultaneously (e.g. ``"ctrl", "c"``).
        """
        result = call("/internal/workflow/keyboard/hotkey", json={"keys": list(keys)})
        if not result.get("success"):
            raise RuntimeError(f"hotkey() failed: {result.get('error', 'unknown error')}")

    # ----- Mouse -----

    def click_at(self, x: int, y: int, *, button: str = "left") -> None:
        """Click at screen coordinates.

        Args:
            x: X coordinate.
            y: Y coordinate.
            button: Mouse button (``left``, ``right``, ``middle``).
        """
        result = call(
            "/internal/workflow/mouse/click",
            json={"x": x, "y": y, "button": button},
        )
        if not result.get("success"):
            raise RuntimeError(f"click_at() failed: {result.get('error', 'unknown error')}")

    def move(self, x: int, y: int) -> None:
        """Move the mouse cursor.

        Args:
            x: Target X coordinate.
            y: Target Y coordinate.
        """
        result = call("/internal/workflow/mouse/move", json={"x": x, "y": y})
        if not result.get("success"):
            raise RuntimeError(f"move() failed: {result.get('error', 'unknown error')}")

    def scroll(
        self,
        direction: str = "down",
        amount: int = 3,
        *,
        x: int | None = None,
        y: int | None = None,
    ) -> None:
        """Scroll the mouse wheel.

        Args:
            direction: Scroll direction (``up``, ``down``, ``left``, ``right``).
            amount: Number of scroll increments.
            x: Optional X coordinate to scroll at.
            y: Optional Y coordinate to scroll at.
        """
        payload: dict[str, Any] = {"direction": direction, "amount": amount}
        if x is not None:
            payload["x"] = x
        if y is not None:
            payload["y"] = y

        result = call("/internal/workflow/mouse/scroll", json=payload)
        if not result.get("success"):
            raise RuntimeError(f"scroll() failed: {result.get('error', 'unknown error')}")

    # ----- Drive -----

    def drive(self, path: str) -> Drive:
        """Access a host-mounted filesystem path.

        Args:
            path: Host path (must be in the allowed drive paths list).

        Returns:
            Drive instance for file operations.
        """
        return Drive(path)
