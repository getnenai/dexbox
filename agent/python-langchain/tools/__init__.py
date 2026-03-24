"""LangChain tools built dynamically from the dexbox GET /tools schema."""

from __future__ import annotations

import base64
from typing import Any

from langchain_core.tools import StructuredTool
from pydantic import create_model

from client import call_dexbox, call_dexbox_raw

# Tools to exclude from dynamic loading
_EXCLUDE_TOOLS = {"text_editor"}

# Map from schema tool name → Anthropic wire-format type used by POST /actions
_TOOL_TYPE_MAP = {
    "computer": "computer_20250124",
    "bash": "bash_20250124",
    "text_editor": "text_editor_20250124",
}


def _json_schema_type_to_python(prop: dict) -> type:
    """Map a JSON Schema property to a Python type annotation."""
    schema_type = prop.get("type", "string")
    if schema_type == "string":
        return str
    elif schema_type == "integer":
        return int
    elif schema_type == "number":
        return float
    elif schema_type == "boolean":
        return bool
    elif schema_type == "array":
        return list
    return str


def _build_pydantic_model(name: str, parameters: dict):
    """Create a Pydantic model from a JSON Schema parameters dict."""
    properties = parameters.get("properties", {})
    required = set(parameters.get("required", []))

    fields = {}
    for field_name, prop in properties.items():
        python_type = _json_schema_type_to_python(prop)

        if field_name in required:
            # Required field — no default
            fields[field_name] = (python_type, ...)
        else:
            # Optional field — default to None
            from typing import Optional
            fields[field_name] = (Optional[python_type], None)

    return create_model(f"{name}Input", **fields)


def _make_tool_fn(tool_name: str):
    """Create the callable that executes a tool via dexbox."""
    wire_type = _TOOL_TYPE_MAP.get(tool_name, tool_name)

    def tool_fn(**kwargs) -> Any:
        # Build the body in the format POST /actions expects
        body = {"type": wire_type}

        # For computer tool, 'action' is a top-level field in the wire format
        if tool_name == "computer":
            action = kwargs.pop("action", None)
            if action:
                body["action"] = action

        # Add remaining params
        for k, v in kwargs.items():
            if v is not None:
                body[k] = v

        # Screenshot: compress to JPEG and return as content block list.
        # IMPORTANT: do NOT resize — coordinates must match the VM's native
        # resolution. Image tokens are based on dimensions (~2K), not file size.
        if tool_name == "computer" and body.get("action") == "screenshot":
            from io import BytesIO

            from PIL import Image

            png_bytes = call_dexbox_raw(body)
            img = Image.open(BytesIO(png_bytes))
            buf = BytesIO()
            img.save(buf, format="JPEG", quality=60)
            b64 = base64.b64encode(buf.getvalue()).decode("ascii")

            return [{
                "type": "image",
                "source": {
                    "type": "base64",
                    "media_type": "image/jpeg",
                    "data": b64,
                },
            }]

        result = call_dexbox(body)
        if isinstance(result, dict) and result.get("output"):
            return result["output"]
        return f"Executed {tool_name}"

    return tool_fn


def build_tools_from_schema(schemas: list[dict]) -> list[StructuredTool]:
    """Build LangChain StructuredTool instances from GET /tools response.

    Args:
        schemas: List of tool schema dicts from GET /tools.

    Returns:
        List of LangChain StructuredTool instances.
    """
    tools = []
    for schema in schemas:
        name = schema["name"]
        if name in _EXCLUDE_TOOLS:
            continue

        description = schema.get("description", "")
        parameters = schema.get("parameters", {})

        input_model = _build_pydantic_model(name, parameters)
        fn = _make_tool_fn(name)

        tool = StructuredTool.from_function(
            func=fn,
            name=name,
            description=description,
            args_schema=input_model,
        )
        tools.append(tool)

    return tools
