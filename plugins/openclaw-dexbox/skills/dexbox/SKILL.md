---
name: dexbox
description: Control Windows VMs and RDP desktops via dexbox
metadata:
  {
    "openclaw":
      {
        "emoji": "",
        "requires": { "bins": ["dexbox"] },
        "install":
          [
            {
              "id": "dexbox-go",
              "kind": "go",
              "package": "github.com/getnenai/dexbox/cmd/dexbox@latest",
              "bins": ["dexbox"],
              "label": "Install dexbox via go install",
            },
          ],
      },
  }
---

# Dexbox Desktop Control

Dexbox lets you control Windows VMs and RDP desktops programmatically. You have 12 tools available split into two categories.

## Prerequisites

Before using dexbox tools, the dexbox server must be running with at least one desktop connected:

```bash
dexbox start          # Start the dexbox server
dexbox up desktop-1   # Connect a desktop
```

If dexbox is not installed, install it with:

```bash
go install github.com/getnenai/dexbox/cmd/dexbox@latest
```

Or clone the repo and run `make install`.

## Workflow

1. **Discover desktops**: Call `list_desktops` to see available desktops and their states
2. **Start if needed**: Call `start_desktop` to bring a desktop online
3. **Screenshot first**: Always call `screenshot` before interacting to see the current screen
4. **Interact**: Use `click`, `type_text`, `key_press`, `scroll` to control the GUI, or `bash` for PowerShell commands
5. **Verify**: Take another `screenshot` after actions to confirm the result
6. **Prefer PowerShell**: Use `bash` for tasks that can be done programmatically — it's faster and more reliable than GUI interaction

## Desktop routing

All action tools accept an optional `desktop` parameter:
- **One desktop connected**: Omit the parameter — the server auto-resolves
- **Multiple desktops**: Pass the `name` from `list_desktops` to target a specific desktop

## Tool reference

### Lifecycle tools

| Tool | Description |
|---|---|
| `list_desktops` | List all desktops with state and connection info |
| `create_desktop` | Register a new RDP desktop connection |
| `destroy_desktop` | Delete a VM or unregister an RDP connection |
| `start_desktop` | Boot a VM or connect an RDP session |
| `stop_desktop` | Shut down the VM guest OS or disconnect RDP |
| `get_desktop` | Get status of a single desktop |

### Action tools

| Tool | Description |
|---|---|
| `screenshot` | Take a screenshot (returns PNG image) |
| `click` | Click at x,y coordinates (left/right/middle/double) |
| `type_text` | Type a text string (click the input field first) |
| `key_press` | Press a key or combo (e.g. `enter`, `ctrl+c`, `alt+tab`) |
| `scroll` | Scroll at x,y with direction (up/down) and amount |
| `bash` | Run a PowerShell command in the guest VM |

## Tips

- This is a Windows VM — use Windows-style paths (e.g. `C:\Users\dexbox\Desktop\file.txt`)
- The `bash` tool runs PowerShell, not Unix bash
- When clicking, target the center of UI elements from the screenshot
- If an action doesn't produce the expected result, take a screenshot and try an alternative approach
- For typing, always click the target input field first
