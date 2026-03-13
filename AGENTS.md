# Project Rules

- **Git Commits**:
  - **No Conventional Style**: NEVER use conventional commit prefixes (e.g., `feat:`, `fix:`, `chore:`).
  - **Atomic & Descriptive**: Each commit must be a single, non-breaking, semantically sensible change.
  - **Subject Line**:
    - Limit to 50 characters, capitalize the first letter, and DO NOT end with a period.
    - Use the **imperative mood** (e.g., "Refactor X" instead of "Refactored X"). A subject should complete: "If applied, this commit will [subject line]".
  - **Message Body**:
    - Separate from the subject with a blank line. Wrap text at 72 characters.
    - Focus on **what** and **why** (rationale) rather than **how** (the code itself shows how).

## Workflow Authoring

Workflows are Python scripts using the `dexbox` SDK (`Agent`, `Computer`, Pydantic
`Params`/`Result` models). Detailed guidance lives in `.agents/rules/`:

- `workflow-core.mdc` — core principles, SDK quick reference (start here)
- `workflow-creation-process.mdc` — step-by-step authoring process
- `workflow-python-sdk.mdc` — execution environment and best practices
- `workflow-guide-comprehensive.mdc` — advanced patterns and architecture
- `workflow-reference-detailed.mdc` — complete API reference with examples
- `workflow-error-handling.mdc` — error handling patterns
- `workflow-secure-params.mdc` — handling secrets/secure parameters
- `mcp-platform-tools.mdc` — Nen MCP tool usage (deploy, run, debug)
