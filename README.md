# Dexbox

[![Discord](https://img.shields.io/badge/Discord-%235865F2.svg?logo=discord&logoColor=white)](https://discord.gg/Bga4QkvEgZ)

Run computer-use workflows locally using Docker. Workflows execute inside a sandboxed environment with access to a desktop environment.

## Requirements

- Docker Desktop (or Docker Engine on Linux)
- [Go 1.23+](https://go.dev/doc/install)
- An Anthropic API key

## Quick Start

Build the images

```bash
make build
```

Create a `.env` file with your `ANTHROPIC_API_KEY`

```bash
cp .env.example .env # add your ANTHROPIC_API_KEY to .env
```

Install the CLI

```bash
go install github.com/getnenai/dexbox/cmd/dexbox@latest
```

Start the desktop container

```bash
dexbox start
```

Run a workflow

```bash
dexbox run examples/extract-data.py
```

Stop when done

```bash
dexbox stop
```

### Build and install locally:

```bash
make install-cli
# installed to ~/.local/bin/dexbox
```

Or build only:

```bash
make build-cli
# binary is at ./bin/dexbox
```

## Build the Docker images

```bash
make build
```

This builds two images:

- `dexbox:latest` — the desktop container (X11, VNC, parent service)
- `dexbox-sandbox-python:latest` — the minimal sandbox container that runs workflow scripts

## Writing Workflows

Workflows are Python scripts with a `run(input)` function:

```python workflow.py
from dexbox import Agent, Computer

def run(input: dict) -> dict:
    agent = Agent()
    computer = Computer()

    agent.execute("Open web browser to https://news.ycombinator.com")
    stories = agent.extract(
        "Get the top 5 story titles",
        schema={"type": "array", "items": {"type": "string"}},
    )
    return {"stories": stories}
```

Run it:

```bash
dexbox run workflow.py
```

Use the `--no-browser` flag to prevent the live stream from automatically opening in your browser.

## SDK Reference

### `Agent`

```python
agent = Agent()

# Execute a multi-step computer-use task
agent.execute("Click the Login button and enter credentials")

# Verify a visual condition
if agent.verify("Is the dashboard visible?"):
    ...

# Extract structured data
data = agent.extract("Get the user's email address", schema={"type": "string"})
```

### `Computer`

```python
computer = Computer()

computer.type("Hello, world!")          # Type text
computer.press("Return")                # Press a key
computer.hotkey("ctrl", "c")            # Key combination
computer.click_at(100, 200)             # Click coordinates
computer.move(300, 400)                 # Move mouse
computer.scroll("down", 3)              # Scroll

# Access host files
drive = computer.drive("/mnt/tmp")
files = drive.files("*.pdf")
content = files[0].read_bytes()
```

### `SecureValue`

For sensitive inputs (passwords, tokens), use `SecureValue` so the secret never enters the sandbox:

```python
from dexbox import Computer, SecureValue

computer = Computer()
computer.type(SecureValue("my_password"))  # resolved server-side
```

Pass secure params when running:

```bash
dexbox run examples/login-secure.py --secure-params '{"my_password": "hunter2"}'
```

## Environment Variables

### Required

| Variable            | Description                           |
| ------------------- | ------------------------------------- |
| `ANTHROPIC_API_KEY` | **Required.** Your Anthropic API key. |

### Optional

| Variable                     | Default                        | Description                                                   |
| ---------------------------- | ------------------------------ | ------------------------------------------------------------- |
| `DEXBOX_MODEL`               | `claude-haiku-4-5-20251001`    | LLM model to use.                                             |
| `DRIVE_PATHS`                | `/mnt/tmp`                     | Comma-separated container host paths accessible to workflows. |
| `DEXBOX_SANDBOX_IMAGE`       | `dexbox-sandbox-python:latest` | Sandbox container image.                                      |
| `DEXBOX_SANDBOX_PULL_POLICY` | `never`                        | `never` or `always`.                                          |
| `DEXBOX_SANDBOX_TIMEOUT`     | `600`                          | Max workflow duration (seconds).                              |
| `DEXBOX_BACKEND`             | `linux-desktop`                | Desktop backend strategy (`linux-desktop` or `rdp`).          |
| `RDP_HOST`                   | —                              | RDP server hostname. Required if `DEXBOX_BACKEND=rdp`.        |
| `RDP_USERNAME`               | —                              | Username for RDP. Required if `DEXBOX_BACKEND=rdp`.           |
| `RDP_PASSWORD`               | —                              | Password for RDP. Required if `DEXBOX_BACKEND=rdp`.           |
| `RDP_SECURITY`               | —                              | Security protocol for RDP (e.g. `rdp`, `tls`, `nla`).         |
| `RDP_RETRY_DELAY_SECONDS`    | `60`                           | Minimum seconds between RDP reconnect attempts.               |

### Using a `.env` file

The `dexbox` CLI automatically loads environment variables from a `.env` file in the current directory if it exists.

If you want to load environment variables from a different file, use the `-e` or `--env-file` flag:

```bash
dexbox -e test.env run examples/open-browser.py
```

Alternatively, you can manually export them in your current shell session:

```bash
set -a; source test.env; set +a
dexbox run examples/open-browser.py
```

## Examples

See the [`examples/`](examples/) directory:

- [`extract-data.py`](examples/extract-data.py) — Extract structured data
- [`fill-form.py`](examples/fill-form.py) — Fill out a web form
- [`download-files.py`](examples/download-files.py) — Download files to Drive

## Testing

You can run the full test suite (both Python unit tests and Go integration tests) using `make`:

```bash
make test # requires uv
```

### Python Unit Tests

The unit tests validate the Python SDK/runtime logic and can be run independently:

```bash
make test-python
```

### Go Integration Tests

The integration tests validate the `dexbox` CLI and the orchestration of the LLM Sandbox. **You must have the desktop container running before executing the integration tests:**

```bash
dexbox start          # Or docker compose up -d
make test-integration # Run the integration tests (runs headlessly with --no-browser)
```

## Development

```bash
# Lint
make lint
```

## Architecture

```
dexbox CLI (Go)
    │
    └─► POST /run ──► dexbox container (Go Server)
                            │
                            ├─ Debian desktop (Xvfb + openbox)
                            ├─ VNC server (TigerVNC) or RDP client (xfreerdp)
                            ├─ Screen recording (FFmpeg)
                            │
                            └─► sandbox container (Docker sibling)
                                    │
                                    ├─ runs workflow.py via harness
                                    └─► RPC back to parent (keyboard, mouse, VLM)
```

## License

This project is licensed under the [Apache License 2.0](LICENSE).

For information on the licenses of third-party dependencies used in this project, please see [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
