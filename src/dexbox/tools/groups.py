"""Tool groups — model-to-tool-spec mapping and runtime tool builder."""

import os
from dataclasses import dataclass

from dexbox.provider import AnthropicModel

_EXTRA_OPTIONS_BY_API_TYPE: dict[str, dict[str, object]] = {
    "computer_20251124": {"enable_zoom": True},
}


@dataclass(frozen=True)
class ModelToolSpec:
    """API-facing tool spec for a specific model version.

    Controls which ``type`` field and ``betas`` header are sent to the
    Anthropic API.  Never instantiated by user code.
    """

    api_type: str
    beta_flag: str

    def build_api_tool_params(self) -> list[dict]:
        """Build the tool-param list sent to the Anthropic API."""
        width = int(os.getenv("WIDTH") or 0)
        height = int(os.getenv("HEIGHT") or 0)
        display_num_str = os.getenv("DISPLAY_NUM")
        display_num = int(display_num_str) if display_num_str is not None else None

        computer_params: dict = {
            "type": self.api_type,
            "name": "computer",
            "display_width_px": width,
            "display_height_px": height,
            "display_number": display_num,
        }
        extras = _EXTRA_OPTIONS_BY_API_TYPE.get(self.api_type, {})
        computer_params.update(extras)

        return [computer_params]


# Model → API-facing spec mapping
MODEL_TOOL_SPECS: dict[str, ModelToolSpec] = {
    AnthropicModel.CLAUDE_SONNET_4_5.value: ModelToolSpec("computer_20250124", "computer-use-2025-01-24"),
    AnthropicModel.CLAUDE_SONNET_4.value: ModelToolSpec("computer_20250124", "computer-use-2025-01-24"),
    AnthropicModel.CLAUDE_HAIKU_4_5.value: ModelToolSpec("computer_20250124", "computer-use-2025-01-24"),
    AnthropicModel.CLAUDE_OPUS_4_6.value: ModelToolSpec("computer_20251124", "computer-use-2025-11-24"),
    AnthropicModel.CLAUDE_SONNET_4_6.value: ModelToolSpec("computer_20251124", "computer-use-2025-11-24"),
}

# Default spec — matches the DEFAULT_MODEL in config.py (claude-sonnet-4-5-20250929)
DEFAULT_MODEL_TOOL_SPEC = ModelToolSpec("computer_20250124", "computer-use-2025-01-24")


def get_model_tool_spec(model: str | None) -> ModelToolSpec:
    """Return the API-facing tool spec for a model.

    If *model* is ``None`` or empty, returns ``DEFAULT_MODEL_TOOL_SPEC``.
    Raises ``ValueError`` if *model* is set but not recognised.
    """
    if not model:
        return DEFAULT_MODEL_TOOL_SPEC
    if model in MODEL_TOOL_SPECS:
        return MODEL_TOOL_SPECS[model]

    supported = ", ".join(sorted(MODEL_TOOL_SPECS.keys()))
    raise ValueError(f"Model '{model}' is not supported. Available models: {supported}")
