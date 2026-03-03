"""API provider definitions and model configuration.

Defines supported LLM/VLM providers and model-specific tool specs
for the Anthropic computer-use API.
"""

from __future__ import annotations

from enum import StrEnum


class APIProvider(StrEnum):
    """Supported API providers."""

    ANTHROPIC = "anthropic"


class AnthropicModel(StrEnum):
    """Centrally-defined Anthropic model identifiers."""

    CLAUDE_OPUS_4_6 = "claude-opus-4-6"
    CLAUDE_SONNET_4_6 = "claude-sonnet-4-6"
    CLAUDE_SONNET_4_5 = "claude-sonnet-4-5-20250929"
    CLAUDE_SONNET_4 = "claude-sonnet-4-20250514"
    CLAUDE_HAIKU_4_5 = "claude-haiku-4-5-20251001"


# Exact model identifiers that support Anthropic's structured outputs feature.
STRUCTURED_OUTPUT_MODELS: set[AnthropicModel] = {
    AnthropicModel.CLAUDE_SONNET_4_6,
    AnthropicModel.CLAUDE_SONNET_4_5,
    AnthropicModel.CLAUDE_OPUS_4_6,
    AnthropicModel.CLAUDE_HAIKU_4_5,
}
