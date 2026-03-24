# dexbox LangChain Agent

Python LangChain agent that wraps the [dexbox](../README.md) HTTP API into
LangChain tools and runs a Claude computer-use loop.

## Architecture

```
agent.py            ← Interactive CLI: ChatAnthropic + tool loop
client.py           ← HTTP client: GET /tools, POST /actions
tools/
└── __init__.py     ← Dynamically builds tools from GET /tools schema
```

Tools are loaded dynamically at startup from the dexbox `GET /tools` endpoint.
The server returns JSON Schema for each tool; the agent converts these into
LangChain `StructuredTool` instances automatically.

## Setup

```bash
cd langchain-agent
uv sync
cp .env.example .env
# Edit .env and set your ANTHROPIC_API_KEY
```

## Usage

1. Start dexbox with a running VM:
   ```bash
   dexbox start
   ```

2. Run the agent:
   ```bash
   cd langchain-agent
   uv run python agent.py
   ```

3. Type instructions:
   ```
   → Take a screenshot to see what's on the screen
   → Open Notepad and type "Hello World"
   → List files on the Desktop using PowerShell
   ```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | — | Anthropic API key (required) |
| `DEXBOX_URL` | `http://localhost:8600` | Dexbox server address |
| `DEXBOX_MODEL` | `claude-sonnet-4-20250514` | Claude model to use |
