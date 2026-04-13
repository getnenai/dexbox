# NEN-692: Admin Permissions Hardening — Test Progress

## Overview

This document tracks iterative VM testing for NEN-692 (harden VM provisioning for 
agent operations). Each test cycle requires a fresh VM provision (~15-30 min).

**Branch:** `cheungbrenden/nen-692-harden-vm-provisioning-for-agent-operations`  
**PR:** [#21](https://github.com/getnenai/dexbox/pull/21)

---

## Architecture

All admin and security hardening is done via **SetupComplete.cmd** (runs as SYSTEM
after Windows setup, before first user logon). This is the single source of truth.

Previous approaches that were **removed** (broken on ARM64):
- `<Group>Administrators</Group>` in autounattend.xml `UserAccounts` — silently ignored
- `FirstLogonCommands` — race condition with driver initialization causes silent failure

## What SetupComplete.cmd Does

1. Creates `dexbox` user with password `dexbox123`
2. Adds `dexbox` to `Administrators` group
3. Disables Windows Defender real-time monitoring
4. Adds Defender exclusion paths (`\\vboxsvr\shared`, `C:\dexbox`, `C:\Users\dexbox`)
5. Opens firewall port 8600 for dexbox-agent
6. Sets PowerShell execution policy to Bypass
7. Installs VirtualBox Guest Additions certificates
8. Launches Guest Additions installer
9. Schedules reboot

## Log Files

After each provision, check these log files inside the VM:

| Log File | Written By | Content |
|----------|-----------|---------|
| `C:\Windows\Setup\Scripts\SetupComplete.log` | `SetupComplete.cmd` | Timestamped step-by-step with `[VERIFY]` checks and results summary |

### How to Retrieve Logs

**Automatic:** After `dexbox create` completes, `SetupComplete.log` is auto-printed to stdout.

**Manual:** If automatic retrieval fails:
```bash
VBoxManage guestcontrol <vm-name> run \
  --exe cmd.exe \
  --username dexbox --password dexbox123 \
  -- cmd.exe /c type C:\Windows\Setup\Scripts\SetupComplete.log
```

**Via RDP:** Connect and run:
```powershell
Get-Content C:\Windows\Setup\Scripts\SetupComplete.log
```

## What to Look For in Logs

| Check | Expected | How to Verify |
|-------|----------|--------------|
| Admin membership | `dexbox` listed under `Administrators` | `[VERIFY] Admin group members:` section |
| Defender disabled | `True` | `[VERIFY] Defender RT monitoring disabled:` section |
| Exclusion paths | 3 paths listed | `[VERIFY] Defender exclusion paths:` section |
| Firewall rule | `Enabled: Yes`, `LocalPort: 8600` | `[VERIFY] Firewall rule:` section |
| Execution policy | `Bypass` | `[VERIFY] Execution policy:` section |

## Quick Debugging

If a step shows an error in the log:

- **"The specified account name is already a member"** → User already in Administrators, this is OK
- **"System error 1378"** → Same as above, benign
- **"The command completed successfully"** → net commands succeeded
- **Defender commands return nothing** → Check if Defender service is running at all (ARM issue)
- **Firewall "Ok."** → Rule added successfully

---

## Test Log

### Test 1 — [DATE]

**Commit:** `<sha>`
**Result:** _pending_

**Log output:**
```
<paste SetupComplete.log here>
```

**Observations:**
- 

**Next steps:**
- 

---

### Template for New Tests

Copy this block for each new test iteration:

```markdown
### Test N — [DATE]

**Commit:** `<sha>`  
**Result:** ✅ PASS / ❌ FAIL / ⚠️ PARTIAL

**Log output:**
\```
<paste SetupComplete.log here>
\```

**Observations:**
- 

**Next steps:**
- 
```
