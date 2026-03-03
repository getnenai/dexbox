"""Computer tool — X11/xdotool-based computer-use implementation.

Direct copy from cup with cup-specific imports replaced.
"""

import asyncio
import base64
import logging
import os
import shlex
import shutil
from abc import ABC, abstractmethod
from enum import StrEnum
from pathlib import Path
from typing import Literal, TypedDict, cast, get_args
from uuid import uuid4

from anthropic.types.beta import BetaToolUnionParam

from .base import BaseAnthropicTool, ToolError, ToolResult
from .run import run

logger = logging.getLogger("dexbox.tools")

OUTPUT_DIR = "/tmp/outputs"
TYPING_DELAY_MS = 12
TYPING_GROUP_SIZE = 50
SCREENSHOT_DELAY = float(os.getenv("SCREENSHOT_DELAY", "2.0"))

Action_20241022 = Literal[
    "key",
    "type",
    "mouse_move",
    "left_click",
    "left_click_drag",
    "right_click",
    "middle_click",
    "double_click",
    "screenshot",
    "cursor_position",
]
Action_20250124 = (
    Action_20241022
    | Literal[
        "left_mouse_down",
        "left_mouse_up",
        "scroll",
        "hold_key",
        "wait",
        "triple_click",
    ]
)
Action_20251124 = Action_20250124 | Literal["zoom"]
Action_Dexbox = Action_20251124

ScrollDirection = Literal["up", "down", "left", "right"]


class Resolution(TypedDict):
    width: int
    height: int


MAX_SCALING_TARGETS: dict[str, Resolution] = {
    "XGA": Resolution(width=1024, height=768),
    "WXGA": Resolution(width=1280, height=800),
    "FWXGA": Resolution(width=1366, height=768),
}

CLICK_BUTTONS = {
    "left_click": 1,
    "right_click": 3,
    "middle_click": 2,
    "double_click": "--repeat 2 --delay 10 1",
    "triple_click": "--repeat 3 --delay 10 1",
}


class ScalingSource(StrEnum):
    COMPUTER = "computer"
    API = "api"


class ComputerToolOptions(TypedDict):
    display_height_px: int
    display_width_px: int
    display_number: int | None


def chunks(s: str, chunk_size: int) -> list[str]:
    return [s[i : i + chunk_size] for i in range(0, len(s), chunk_size)]


class AbstractComputerTool(ABC):
    """Abstract base class for computer tool implementations."""

    name: Literal["computer"] = "computer"
    width: int
    height: int
    display_num: int | None
    _screenshot_delay = SCREENSHOT_DELAY
    _scaling_enabled = True

    @property
    def options(self) -> ComputerToolOptions:
        width, height = self.scale_coordinates(ScalingSource.COMPUTER, self.width, self.height)
        return {"display_width_px": width, "display_height_px": height, "display_number": self.display_num}

    def __init__(self):
        self.width = int(os.getenv("WIDTH") or 0)
        self.height = int(os.getenv("HEIGHT") or 0)
        assert self.width and self.height, "WIDTH, HEIGHT must be set"
        if (display_num := os.getenv("DISPLAY_NUM")) is not None:
            self.display_num = int(display_num)
        else:
            self.display_num = None

    @abstractmethod
    async def _screenshot_impl(self, path: Path) -> bool:
        pass

    async def __call__(
        self, *, action: Action_20241022, text: str | None = None, coordinate: tuple[int, int] | None = None, **kwargs
    ):
        raise NotImplementedError("Subclass must implement __call__")

    def validate_and_get_coordinates(self, coordinate: tuple[int, int] | None = None):
        if not isinstance(coordinate, list) or len(coordinate) != 2:
            raise ToolError(f"{coordinate} must be a tuple of length 2")
        if not all(isinstance(i, int) and i >= 0 for i in coordinate):
            raise ToolError(f"{coordinate} must be a tuple of non-negative ints")
        return self.scale_coordinates(ScalingSource.API, coordinate[0], coordinate[1])

    async def screenshot(self):
        output_dir = Path(OUTPUT_DIR)
        output_dir.mkdir(parents=True, exist_ok=True)
        path = output_dir / f"screenshot_{uuid4().hex}.png"
        success = await self._screenshot_impl(path)
        if success and self._scaling_enabled:
            x, y = self.scale_coordinates(ScalingSource.COMPUTER, self.width, self.height)
            await self.shell(f"convert {path} -resize {x}x{y}! {path}", take_screenshot=False)
        if path.exists():
            return ToolResult(base64_image=base64.b64encode(path.read_bytes()).decode())
        raise ToolError("Failed to take screenshot")

    async def shell(self, command: str, take_screenshot=True) -> ToolResult:
        _, stdout, stderr = await run(command)
        base64_image = None
        if take_screenshot:
            await asyncio.sleep(self._screenshot_delay)
            base64_image = (await self.screenshot()).base64_image
        return ToolResult(output=stdout, error=stderr, base64_image=base64_image)

    def scale_coordinates(self, source: ScalingSource, x: int, y: int):
        if not self._scaling_enabled:
            return x, y
        ratio = self.width / self.height
        target_dimension = None
        for dimension in MAX_SCALING_TARGETS.values():
            if abs(dimension["width"] / dimension["height"] - ratio) < 0.02:
                if dimension["width"] < self.width:
                    target_dimension = dimension
                break
        if target_dimension is None:
            return x, y
        x_scaling_factor = target_dimension["width"] / self.width
        y_scaling_factor = target_dimension["height"] / self.height
        if source == ScalingSource.API:
            if x > self.width or y > self.height:
                raise ToolError(f"Coordinates {x}, {y} are out of bounds")
            return round(x / x_scaling_factor), round(y / y_scaling_factor)
        return round(x * x_scaling_factor), round(y * y_scaling_factor)


class X11ComputerTool(AbstractComputerTool):
    """Computer tool implementation using X11/xdotool."""

    def __init__(self):
        super().__init__()
        if (display_num := os.getenv("DISPLAY_NUM")) is not None:
            self._display_prefix = f"DISPLAY=:{display_num} "
        else:
            self._display_prefix = ""
        self.xdotool = f"{self._display_prefix}xdotool"

    async def _screenshot_impl(self, path: Path) -> bool:
        if shutil.which("gnome-screenshot"):
            cmd = f"{self._display_prefix}gnome-screenshot -f {path} -p"
        else:
            cmd = f"{self._display_prefix}scrot -p {path}"
        await run(cmd)
        return path.exists()

    async def __call__(
        self, *, action: Action_20241022, text: str | None = None, coordinate: tuple[int, int] | None = None, **kwargs
    ):
        if "key" in kwargs and text is None:
            text = kwargs["key"]
        if action in ("mouse_move", "left_click_drag"):
            if coordinate is None:
                raise ToolError(f"coordinate is required for {action}")
            x, y = self.validate_and_get_coordinates(coordinate)
            if action == "mouse_move":
                return await self.shell(f"{self.xdotool} mousemove --sync {x} {y}")
            return await self.shell(f"{self.xdotool} mousedown 1 mousemove --sync {x} {y} mouseup 1")
        if action in ("key", "type"):
            if text is None:
                raise ToolError(f"text is required for {action}")
            if action == "key":
                return await self.shell(f"{self.xdotool} key -- {text}")
            results = []
            for chunk in chunks(text, TYPING_GROUP_SIZE):
                results.append(
                    await self.shell(
                        f"{self.xdotool} type --delay {TYPING_DELAY_MS} -- {shlex.quote(chunk)}", take_screenshot=False
                    )
                )
            screenshot_base64 = (await self.screenshot()).base64_image
            return ToolResult(
                output="".join(r.output or "" for r in results),
                error="".join(r.error or "" for r in results),
                base64_image=screenshot_base64,
            )
        if action in ("left_click", "right_click", "double_click", "middle_click", "screenshot", "cursor_position"):
            if action == "screenshot":
                return await self.screenshot()
            if action == "cursor_position":
                result = await self.shell(f"{self.xdotool} getmouselocation --shell", take_screenshot=False)
                output = result.output or ""
                x, y = self.scale_coordinates(
                    ScalingSource.COMPUTER,
                    int(output.split("X=")[1].split("\n")[0]),
                    int(output.split("Y=")[1].split("\n")[0]),
                )
                return result.replace(output=f"X={x},Y={y}")
            return await self.shell(f"{self.xdotool} click {CLICK_BUTTONS[action]}")
        raise ToolError(f"Invalid action: {action}")


class ComputerTool20250124_X11(X11ComputerTool, BaseAnthropicTool):
    """X11 computer tool for API version 2025-01-24."""

    api_type: Literal["computer_20250124"] = "computer_20250124"

    def to_params(self):
        return cast(BetaToolUnionParam, {"name": self.name, "type": self.api_type, **self.options})

    async def __call__(
        self,
        *,
        action: Action_20250124,
        text: str | None = None,
        coordinate: tuple[int, int] | None = None,
        scroll_direction: ScrollDirection | None = None,
        scroll_amount: int | None = None,
        duration: int | float | None = None,
        key: str | None = None,
        **kwargs,
    ):
        if action == "key" and key is not None and text is None:
            text = key
        if action in ("left_mouse_down", "left_mouse_up"):
            return await self.shell(f"{self.xdotool} {'mousedown' if action == 'left_mouse_down' else 'mouseup'} 1")
        if action == "scroll":
            if scroll_direction not in get_args(ScrollDirection):
                raise ToolError("scroll_direction must be up/down/left/right")
            mouse_move = ""
            if coordinate is not None:
                x, y = self.validate_and_get_coordinates(coordinate)
                mouse_move = f"mousemove --sync {x} {y}"
            btn = {"up": 4, "down": 5, "left": 6, "right": 7}[scroll_direction]
            return await self.shell(f"{self.xdotool} {mouse_move} click --repeat {scroll_amount or 3} {btn}")
        if action in ("hold_key", "wait"):
            if not isinstance(duration, (int, float)) or duration < 0 or duration > 100:
                raise ToolError("duration must be 0–100")
            if action == "hold_key":
                if text is None:
                    raise ToolError("text required for hold_key")
                return await self.shell(
                    f"{self.xdotool} keydown {shlex.quote(text)} sleep {duration} keyup {shlex.quote(text)}"
                )
            await asyncio.sleep(duration)
            return await self.screenshot()
        if action in ("left_click", "right_click", "double_click", "triple_click", "middle_click"):
            mouse_move = ""
            if coordinate is not None:
                x, y = self.validate_and_get_coordinates(coordinate)
                mouse_move = f"mousemove --sync {x} {y}"
            cmd_parts = [self.xdotool, mouse_move]
            if key:
                cmd_parts.append(f"keydown {key}")
            cmd_parts.append(f"click {CLICK_BUTTONS[action]}")
            if key:
                cmd_parts.append(f"keyup {key}")
            return await self.shell(" ".join(p for p in cmd_parts if p))
        return await super().__call__(action=action, text=text, coordinate=coordinate, key=key, **kwargs)


class ComputerTool20251124_X11(ComputerTool20250124_X11):
    """X11 computer tool for API version 2025-11-24 with zoom."""

    api_type: Literal["computer_20251124"] = "computer_20251124"

    @property
    def options(self):
        return {**super().options, "enable_zoom": True}

    async def __call__(self, *, action: Action_20251124, region: tuple[int, int, int, int] | None = None, **kwargs):
        if action == "zoom":
            if not region or len(region) != 4:
                raise ToolError("region must be (x0, y0, x1, y1)")
            x0, y0 = self.scale_coordinates(ScalingSource.API, region[0], region[1])
            x1, y1 = self.scale_coordinates(ScalingSource.API, region[2], region[3])
            shot = await self.screenshot()
            if not shot.base64_image:
                raise ToolError("Failed to take screenshot for zoom")
            out = Path(OUTPUT_DIR)
            tmp = out / f"screenshot_{uuid4().hex}.png"
            cropped = out / f"zoomed_{uuid4().hex}.png"
            tmp.write_bytes(base64.b64decode(shot.base64_image))
            await run(f"convert {tmp} -crop {x1 - x0}x{y1 - y0}+{x0}+{y0} +repage {cropped}")
            if cropped.exists():
                data = base64.b64encode(cropped.read_bytes()).decode()
                tmp.unlink(missing_ok=True)
                cropped.unlink(missing_ok=True)
                return ToolResult(base64_image=data)
            raise ToolError("Failed to crop screenshot for zoom")
        return await super().__call__(action=action, **kwargs)


# Aliases
ComputerToolDexbox_X11 = ComputerTool20251124_X11
ComputerToolDexbox = ComputerToolDexbox_X11
