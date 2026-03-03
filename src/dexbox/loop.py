"""Agentic sampling loop — drives the VLM computer-use interaction.

Handles the Anthropic API interaction, tool execution, and iteration control.
"""

from __future__ import annotations

import logging
from collections.abc import Awaitable, Callable
from typing import Any

import anthropic
from anthropic.types.beta import BetaContentBlockParam, BetaMessageParam, BetaToolResultBlockParam

from dexbox.tools import ToolCollection
from dexbox.tools.groups import get_model_tool_spec

logger = logging.getLogger("dexbox.agent")

MAX_TOKENS = 4096
MAX_IMAGES_IN_CONTEXT = 10  # Keep only the most recent N screenshots


def _maybe_filter_images(messages: list[BetaMessageParam], max_images: int = MAX_IMAGES_IN_CONTEXT) -> None:
    """Remove older screenshots from context to manage token budget."""
    image_count = 0
    for message in reversed(messages):
        content = message.get("content", [])
        if isinstance(content, list):
            for block in reversed(content):
                if isinstance(block, dict) and block.get("type") == "tool_result":
                    block_content = block.get("content", [])
                    # Collect indices to remove (iterate over a copy to avoid mutation during iteration)
                    indices_to_remove = []
                    for idx, inner in enumerate(list(block_content)):
                        if isinstance(inner, dict) and inner.get("type") == "image":
                            image_count += 1
                            if image_count > max_images:
                                indices_to_remove.append(idx)
                    # Remove in reverse order so earlier indices stay valid
                    for idx in reversed(indices_to_remove):
                        del block_content[idx]


def _make_tool_result(result: Any, tool_use_id: str) -> BetaToolResultBlockParam:
    """Convert a ToolResult to an Anthropic tool_result block."""
    content: list[dict] = []
    is_error = bool(result.error)

    if result.output:
        content.append({"type": "text", "text": result.output})
    if result.base64_image:
        content.append(
            {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": result.base64_image}}
        )
    if result.error:
        content.append({"type": "text", "text": result.error})

    return {
        "type": "tool_result",
        "tool_use_id": tool_use_id,
        "content": content,
        "is_error": is_error,
    }


async def sampling_loop(
    *,
    model: str,
    api_key: str,
    base_url: str | None = None,
    tool_collection: ToolCollection,
    messages: list[BetaMessageParam],
    system_prompt: str = "",
    max_iterations: int = 10,
    on_output: Callable[[str, Any], Awaitable[None]] | None = None,
) -> tuple[list[BetaMessageParam], Any]:
    """Drive the agentic VLM sampling loop.

    Args:
        model: Anthropic model name.
        api_key: Anthropic API key.
        tool_collection: Runtime tool collection (handles tool calls locally).
        messages: Conversation history (modified in-place).
        system_prompt: Optional system prompt prefix.
        max_iterations: Maximum tool-use iterations.
        on_output: Optional async callback(event_type, data) for streaming output.

    Returns:
        (updated_messages, final_stop_reason)
    """
    client = anthropic.AsyncAnthropic(api_key=api_key, base_url=base_url)
    model_tool_spec = get_model_tool_spec(model)
    api_tool_params = model_tool_spec.build_api_tool_params()

    betas = [model_tool_spec.beta_flag, "prompt-caching-2024-07-31"]

    system: list[BetaContentBlockParam] = []
    if system_prompt:
        system = [{"type": "text", "text": system_prompt, "cache_control": {"type": "ephemeral"}}]

    stop_reason = None

    for iteration in range(max_iterations):
        _maybe_filter_images(messages)

        response = await client.beta.messages.create(
            model=model,
            max_tokens=MAX_TOKENS,
            messages=messages,
            tools=api_tool_params,
            system=system,
            betas=betas,
        )

        stop_reason = response.stop_reason
        content = response.content

        if on_output:
            await on_output("llm_response", {"stop_reason": stop_reason, "iteration": iteration})

        # Convert response content to message format
        response_content: list[BetaContentBlockParam] = []
        tool_uses = []

        for block in content:
            if block.type == "text":
                response_content.append({"type": "text", "text": block.text})
                text_content = block.text.strip()
                if text_content:
                    logger.info("Agent thought: %s", text_content)
                if on_output:
                    await on_output("text", block.text)
            elif block.type == "tool_use":
                response_content.append(
                    {
                        "type": "tool_use",
                        "id": block.id,
                        "name": block.name,
                        "input": block.input,
                    }
                )
                tool_uses.append(block)

        messages.append({"role": "assistant", "content": response_content})

        if stop_reason == "end_turn" or not tool_uses:
            break

        # Execute tool calls
        tool_results: list[BetaToolResultBlockParam] = []
        for tool_use in tool_uses:
            logger.info("Executing tool: %s", tool_use.name, extra={"tool": tool_use.name, "input": tool_use.input})
            if on_output:
                await on_output("tool_call", {"name": tool_use.name, "input": tool_use.input})

            result = await tool_collection.run(name=tool_use.name, tool_input=tool_use.input)
            tool_results.append(_make_tool_result(result, tool_use.id))

            if result.error:
                logger.warning("Tool %s failed: %s", tool_use.name, result.error, extra={"tool": tool_use.name})
            else:
                logger.info("Tool %s succeeded", tool_use.name, extra={"tool": tool_use.name})

            if on_output:
                await on_output("tool_result", {"name": tool_use.name, "error": result.error})

        messages.append({"role": "user", "content": tool_results})

    return messages, stop_reason
