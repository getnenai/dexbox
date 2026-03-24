"""Dexbox LangChain Agent — computer-use agent loop with Claude.

Run with:
    cd langchain-agent && uv run python agent.py

Requires:
    - dexbox server running (dexbox start)
    - ANTHROPIC_API_KEY set in .env or environment
"""

from __future__ import annotations

import sys

from dotenv import load_dotenv
from langchain.agents import create_agent

from client import DEXBOX_MODEL, fetch_tool_schemas, health_check
from tools import build_tools_from_schema

load_dotenv()

SYSTEM_PROMPT = """\
You are a computer-use agent controlling a Windows VM through dexbox.

Guidelines:
- Always take a screenshot first to see the current state of the screen
- Use PowerShell commands via the bash tool (this runs PowerShell, not Unix bash)
- Use Windows-style paths (e.g., C:\\Users\\dexbox\\Desktop\\file.txt)
- After performing actions, take another screenshot to verify the result
- Be patient — VM operations can be slow; wait and retry if needed
- When clicking, target the center of UI elements
- For typing, first click on the target input field
"""


def main():
    """Interactive CLI loop."""
    print("dexbox LangChain Agent")
    print(f"   Model: {DEXBOX_MODEL}")
    print()

    # Health check
    if not health_check():
        print("ERROR: Cannot reach dexbox server. Is it running? (dexbox start)")
        sys.exit(1)
    print("dexbox server is healthy")

    # Fetch tool schemas dynamically from the server
    print("Loading tools from dexbox...")
    schemas = fetch_tool_schemas()
    all_tools = build_tools_from_schema(schemas)
    tool_names = [t.name for t in all_tools]
    print(f"Loaded {len(all_tools)} tools: {', '.join(tool_names)}")
    print()

    # Create the agent using LangChain's create_agent API
    # Use explicit max_tokens to avoid the 64K default which overflows
    # Claude's 200K context window when screenshots accumulate.
    from langchain_anthropic import ChatAnthropic

    model = ChatAnthropic(model=DEXBOX_MODEL, max_tokens=4096)
    agent = create_agent(
        model=model,
        tools=all_tools,
        system_prompt=SYSTEM_PROMPT,
    )

    print("Type your instruction (or 'quit' to exit):")
    print()

    while True:
        try:
            prompt = input("> ").strip()
        except (KeyboardInterrupt, EOFError):
            print("\nBye!")
            break

        if not prompt:
            continue
        if prompt.lower() in ("quit", "exit", "q"):
            break

        print()
        # Stream to show reasoning between tool calls
        final_text = ""
        for event in agent.stream(
            {"messages": [{"role": "user", "content": prompt}]},
            stream_mode="updates",
        ):
            for node_name, node_output in event.items():
                if node_name == "model":
                    # Model produced a response — print reasoning text
                    msgs = node_output.get("messages", [])
                    for msg in msgs:
                        content = msg.content if hasattr(msg, "content") else ""
                        if isinstance(content, str) and content.strip():
                            print(f"\n[reasoning] {content}")
                            final_text = content
                        elif isinstance(content, list):
                            for block in content:
                                if isinstance(block, dict):
                                    if block.get("type") == "text" and block.get("text", "").strip():
                                        print(f"\n[reasoning] {block['text']}")
                                        final_text = block["text"]
                                    elif block.get("type") == "tool_use":
                                        name = block.get("name", "?")
                                        inp = block.get("input", {})
                                        action = inp.get("action", "")
                                        if action:
                                            detail = f" action={action}"
                                            coord = inp.get("coordinate")
                                            if coord:
                                                detail += f" at {coord}"
                                            text = inp.get("text", "")
                                            if text:
                                                detail += f" text={text!r}"
                                            print(f"  [tool] {name}{detail}")
                                        else:
                                            print(f"  [tool] {name}({inp})")

        if final_text:
            print(f"\n{final_text}\n")
        else:
            print()


if __name__ == "__main__":
    main()
