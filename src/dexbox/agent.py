"""Agent — high-level VLM-driven interface for workflow scripts.

The Agent class provides three primary methods:

- ``execute(instruction)`` — perform a computer-use action sequence
- ``verify(question)``     — check a visual condition (returns bool)
- ``extract(query, schema)``— extract structured data from the screen
"""

from __future__ import annotations

from typing import Any

from dexbox.rpc import call


class Agent:
    """VLM-driven agent for computer-use workflows.

    All methods communicate with the parent service via RPC — the actual
    VLM calls happen server-side, not inside the sandbox.

    Example::

        from dexbox import Agent

        agent = Agent()
        agent.execute("Click the Login button")
        if agent.verify("Is the dashboard visible?"):
            data = agent.extract(
                "Extract the username",
                {"type": "object", "properties": {"username": {"type": "string"}}},
            )
    """

    def __init__(self, *, model: str | None = None) -> None:
        self._model_override = model

    def execute(
        self,
        instruction: str,
        *,
        max_iterations: int = 25,
    ) -> dict[str, Any]:
        """Execute a computer-use action sequence.

        Args:
            instruction: Natural language description of what to do.
            max_iterations: Maximum number of tool-use iterations.

        Returns:
            Dict with ``success`` (bool) and ``messages`` (list).

        Raises:
            RuntimeError: If the execution fails.
        """
        payload: dict[str, Any] = {
            "instruction": instruction,
            "max_iterations": max_iterations,
        }
        if self._model_override:
            payload["model_override"] = self._model_override

        result = call("/internal/workflow/execute", json=payload)
        if not result.get("success"):
            raise RuntimeError(f"Agent.execute failed: {result.get('error')}")
        return result

    def verify(
        self,
        question: str,
        *,
        timeout: int = 10,
    ) -> bool:
        """Check a visual condition on the screen.

        Args:
            question: Yes/no question about the current screen state.
            timeout: Max seconds to wait for the VLM response.

        Returns:
            ``True`` if the condition is met, ``False`` otherwise.

        Raises:
            RuntimeError: If the verification fails.
        """
        payload: dict[str, Any] = {
            "question": question,
            "timeout": timeout,
        }
        if self._model_override:
            payload["model_override"] = self._model_override

        result = call("/internal/workflow/validate", json=payload)
        if not result.get("success"):
            raise RuntimeError(f"Agent.verify failed: {result.get('error')}")
        return result.get("is_valid", False)

    def extract(
        self,
        query: str,
        schema: dict[str, Any],
    ) -> Any:
        """Extract structured data from the current screen.

        Args:
            query: Description of what data to extract.
            schema: JSON Schema defining the expected output shape.

        Returns:
            Extracted data matching the provided schema.

        Raises:
            dexbox.exceptions.RPCError: If the RPC call fails.
            RuntimeError: If the extraction fails.
        """
        payload: dict[str, Any] = {
            "query": query,
            "schema_def": schema,
        }
        if self._model_override:
            payload["model_override"] = self._model_override

        result = call("/internal/workflow/extract", json=payload)
        if not result.get("success"):
            raise RuntimeError(f"Agent.extract failed: {result.get('error')}")
        return result.get("data")
