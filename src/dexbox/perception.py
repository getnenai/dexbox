"""VLM perception client — takes screenshots and runs VLM matching"""

from __future__ import annotations

import asyncio
import base64
import logging
import os
import shutil
from pathlib import Path
from uuid import uuid4

import anthropic

logger = logging.getLogger("dexbox.perception")

OUTPUT_DIR = "/tmp/outputs"


async def _take_screenshot() -> str:
    """Take a screenshot and return base64-encoded PNG."""
    output_dir = Path(OUTPUT_DIR)
    output_dir.mkdir(parents=True, exist_ok=True)
    path = output_dir / f"screenshot_{uuid4().hex}.png"

    display_prefix = ""
    if (display_num := os.getenv("DISPLAY_NUM")) is not None:
        display_prefix = f"DISPLAY=:{display_num} "

    if shutil.which("gnome-screenshot"):
        cmd = f"{display_prefix}gnome-screenshot -f {path} -p"
    else:
        cmd = f"{display_prefix}scrot -p {path}"

    proc = await asyncio.create_subprocess_shell(cmd)
    await proc.communicate()

    if path.exists():
        return base64.b64encode(path.read_bytes()).decode()
    raise RuntimeError("Screenshot failed")


class UIPerceptionClient:
    """Client for VLM-driven UI validation and matching."""

    def __init__(self, api_key: str, model: str) -> None:
        self._client = anthropic.Anthropic(api_key=api_key)
        self._model = model

    async def take_screenshot(self) -> str:
        """Take a screenshot. Returns base64-encoded PNG."""
        return await _take_screenshot()

    async def is_vlm_match(
        self,
        screenshot_b64: str,
        question: str,
    ) -> dict:
        """Ask the VLM a yes/no question about the screenshot.

        Returns:
            Dict with ``is_match`` (bool) and ``match_reason`` (str).
        """
        prompt = (
            f"Look at this screenshot and answer: {question}\n\n"
            'Respond with JSON: {"is_match": true/false, "match_reason": "brief explanation"}'
        )
        response = self._client.messages.create(
            model=self._model,
            max_tokens=256,
            messages=[
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "image",
                            "source": {"type": "base64", "media_type": "image/png", "data": screenshot_b64},
                        },
                        {"type": "text", "text": prompt},
                    ],
                }
            ],
        )
        import json

        text = response.content[0].text
        try:
            return json.loads(text)
        except Exception:
            # Try to extract JSON from surrounding text
            import re

            match = re.search(r"\{[^}]+\}", text)
            if match:
                return json.loads(match.group())
            return {"is_match": False, "match_reason": text}
