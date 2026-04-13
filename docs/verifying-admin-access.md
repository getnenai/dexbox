# Verifying Admin Access on a Dexbox VM

After `dexbox create vm` completes, use these methods to confirm the `dexbox`
user has admin privileges and all hardening was applied.

---

## Method 1: Automatic Log Output (Easiest)

After provisioning, `SetupComplete.log` is automatically printed to your
terminal. Look for the **HARDENING RESULTS SUMMARY** section:

```
==========================================
=== HARDENING RESULTS SUMMARY ===
==========================================
--- Admin Group Members ---
  dexbox              <-- ✅ must appear here
--- Defender Real-time Monitoring ---
  True                <-- ✅ means monitoring is DISABLED
...
```

If the auto-retrieval failed (e.g. GA not fully ready), retrieve manually:

```bash
VBoxManage guestcontrol dexbox run --exe cmd.exe \
  --username dexbox --password dexbox123 \
  -- cmd.exe /c type C:\Windows\Setup\Scripts\SetupComplete.log
```

---

## Method 2: Run Commands via VBoxManage

Run these from your host terminal to verify each hardening step:

### Check admin group membership
```bash
VBoxManage guestcontrol dexbox run --exe cmd.exe \
  --username dexbox --password dexbox123 \
  -- cmd.exe /c "net localgroup Administrators"
```
**Expected:** `dexbox` appears in the member list.

### Check current user privileges
```bash
VBoxManage guestcontrol dexbox run --exe cmd.exe \
  --username dexbox --password dexbox123 \
  -- cmd.exe /c "whoami /groups"
```
**Expected:** `BUILTIN\Administrators` appears with `Group used for deny only`
or `Mandatory group, Enabled by default, Enabled group, Group owner`.

### Check Defender status
```bash
VBoxManage guestcontrol dexbox run --exe powershell.exe \
  --username dexbox --password dexbox123 \
  -- powershell.exe -Command "(Get-MpPreference).DisableRealtimeMonitoring"
```
**Expected:** `True`

### Check firewall rule
```bash
VBoxManage guestcontrol dexbox run --exe cmd.exe \
  --username dexbox --password dexbox123 \
  -- cmd.exe /c "netsh advfirewall firewall show rule name=dexbox-agent"
```
**Expected:** Rule exists with `Enabled: Yes`, `LocalPort: 8600`.

### Check execution policy
```bash
VBoxManage guestcontrol dexbox run --exe powershell.exe \
  --username dexbox --password dexbox123 \
  -- powershell.exe -Command "Get-ExecutionPolicy"
```
**Expected:** `Bypass`

---

## Method 3: Interactive RDP Session

Connect via RDP and verify interactively:

```bash
dexbox up dexbox
dexbox view dexbox
```

Then in the Windows session, open PowerShell and run:

```powershell
# Am I admin?
net localgroup Administrators

# Detailed group info
whoami /groups /fo list

# All hardening at once
Write-Host "=== Admin ===" ; net localgroup Administrators
Write-Host "=== Defender ===" ; (Get-MpPreference).DisableRealtimeMonitoring
Write-Host "=== Exclusions ===" ; (Get-MpPreference).ExclusionPath
Write-Host "=== Firewall ===" ; netsh advfirewall firewall show rule name=dexbox-agent
Write-Host "=== ExecPolicy ===" ; Get-ExecutionPolicy
```

---

## Quick One-Liner Verification

Run all checks in a single command from the host:

```bash
VBoxManage guestcontrol dexbox run --exe powershell.exe \
  --username dexbox --password dexbox123 \
  -- powershell.exe -Command "
    Write-Host '=== ADMIN GROUP ===';
    net localgroup Administrators;
    Write-Host '=== DEFENDER RT ===';
    (Get-MpPreference).DisableRealtimeMonitoring;
    Write-Host '=== EXCLUSIONS ===';
    (Get-MpPreference).ExclusionPath;
    Write-Host '=== FIREWALL ===';
    netsh advfirewall firewall show rule name=dexbox-agent;
    Write-Host '=== EXEC POLICY ===';
    Get-ExecutionPolicy
  "
```

---

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| `dexbox` not in Administrators | SetupComplete.cmd didn't run | Check if `C:\Windows\Setup\Scripts\SetupComplete.cmd` exists in the VM |
| `VBoxManage guestcontrol` fails | GA not installed or VM still rebooting | Wait 1-2 minutes and retry |
| Defender still enabled | ARM64 Defender may ignore Set-MpPreference | May need Group Policy or registry approach |
| Firewall rule missing | SetupComplete.cmd failed before that step | Check `SetupComplete.log` for errors |
| Log file doesn't exist | SetupComplete.cmd never ran | Verify the specialize pass copied the script: check `C:\Windows\Setup\Scripts\` |
