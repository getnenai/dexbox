"""Tool collection — manages multiple tools and dispatches calls."""

import logging
from typing import Any

from anthropic.types.beta import BetaToolUnionParam

from .base import BaseAnthropicTool, ToolError, ToolFailure, ToolResult

logger = logging.getLogger("dexbox.tools")


class ToolCollection:
    """A collection of anthropic-defined tools."""

    def __init__(self, *tools: BaseAnthropicTool):
        self.tools = tools
        self.tool_map = {tool.name: tool for tool in tools}

    def to_params(self) -> list[BetaToolUnionParam]:
        return [tool.to_params() for tool in self.tools]

    async def run(self, *, name: str, tool_input: dict[str, Any]) -> ToolResult:
        tool = self.tool_map.get(name)

        # Log the action (visible according to component filters)
        logger.info("Executed tool %s", name, extra={"tool": name, "input": tool_input})

        if not tool:
            logger.warning("Unknown tool requested: %s", name)
            return ToolFailure(error=f"Tool {name} is invalid")
        try:
            return await tool(**tool_input)
        except ToolError as e:
            logger.error("Tool %s raised ToolError: %s", name, e.message, exc_info=True)
            return ToolFailure(error=e.message)
