package vbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

)

//go:embed autounattend.xml
var autounattendXML []byte

const (
	virtioISOURL   = "https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso"
	isoCacheDir    = ".dexbox/iso"
	virtioFilename = "virtio-win.iso"
)

// Install runs the full provisioning flow: install VirtualBox, validate ISO, create VM.
// vmName is the name for the new VirtualBox VM.
// isoPath is the path to a user-supplied Windows ISO.
// user and pass are the guest OS account credentials baked into the unattended install.
func Install(ctx context.Context, vmName, isoPath, user, pass string) error {
	// Step 1: Check/install VirtualBox
	if err := ensureVirtualBox(); err != nil {
		return fmt.Errorf("VirtualBox installation: %w", err)
	}

	// Step 2: Validate Windows ISO
	isoPath, err := ensureISO(isoPath)
	if err != nil {
		return fmt.Errorf("ISO: %w", err)
	}

	// Step 3: Create VM
	fmt.Printf("Creating VM %q...\n", vmName)
	wantOSType := "Windows11_64"
	if nativeArch() == "arm64" {
		wantOSType = "Windows 11 on ARM (64-bit)"
	}
	if VMExists(ctx, vmName) {
		if got := VMOSType(ctx, vmName); got != wantOSType {
			fmt.Printf("VM %q has wrong ostype %q (need %q), deleting and recreating...\n",
				vmName, got, wantOSType)
			if err := DeleteVM(ctx, vmName); err != nil {
				return fmt.Errorf("delete wrong-arch VM: %w", err)
			}
		} else {
			fmt.Printf("VM %q already exists, skipping creation.\n", vmName)
		}
	}
	if !VMExists(ctx, vmName) {
		cfg := DefaultVMConfig()
		if err := CreateVM(ctx, vmName, cfg); err != nil {
			return fmt.Errorf("create VM: %w", err)
		}
		fmt.Println("VM created.")
	}

	// Step 4: Unattended install
	fmt.Println("Configuring unattended Windows install...")
	autounattendISO, err := unattendedInstall(ctx, vmName, isoPath, user, pass)
	if err != nil {
		return fmt.Errorf("unattended install: %w", err)
	}

	// Clean up autounattend ISO so credentials are not left on disk.
	// Deferred here so it runs on all exit paths (including StartVM or
	// waitForInstallation failures).
	defer func() {
		if err := os.Remove(autounattendISO); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: could not remove autounattend ISO: %v\n", err)
		}
	}()

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	// Step 5: Configure shared folder (must happen while VM is powered off
	// so the mapping is permanent rather than transient)
	sharedDir := filepath.Join(home, ".dexbox", "shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		return fmt.Errorf("create shared dir: %w", err)
	}
	if err := AddSharedFolder(ctx, vmName, "shared", sharedDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not add shared folder: %v\n", err)
	}

	// Step 6: Start VM and wait for Guest Additions
	fmt.Println("Starting VM...")
	if err := StartVM(ctx, vmName, true); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}

	// OVMF shows "Press any key to boot from CD or DVD" briefly before booting
	// the installer. Send repeated spacebar presses for the first ~15 seconds
	// to ensure the prompt is dismissed regardless of when it appears.
	// keyboardputstring works on both ARM (USB HID) and x86 (PS/2) VMs.
	go func() {
		for i := 0; i < 15; i++ {
			time.Sleep(time.Second)
			_ = SendKeyboardString(ctx, vmName, " ")
		}
	}()

	fmt.Println("Waiting for Windows installation to complete (this may take 15-30 minutes)...")
	if err := waitForInstallation(ctx, vmName); err != nil {
		return fmt.Errorf("waiting for installation: %w", err)
	}

	// Step 7: Done
	fmt.Println("")
	fmt.Println("Installation complete!")
	fmt.Printf("  VM name:    %s\n", vmName)
	fmt.Printf("  User:       %s\n", user)
	fmt.Println("  Password:   ***")
	fmt.Printf("  Shared dir: %s\n", sharedDir)
	fmt.Println("")
	fmt.Println("Next steps:")
	fmt.Println("  dexbox start     # Start the tool server")
	fmt.Println("  dexbox status    # Check VM state")
	return nil
}

// Clone creates a new VM by cloning an existing one. The source VM must be
// powered off. This takes seconds instead of the 15-30 minute Install() flow.
func Clone(ctx context.Context, srcVM, dstVM string) error {
	// Check/install VirtualBox
	if err := ensureVirtualBox(); err != nil {
		return fmt.Errorf("VirtualBox installation: %w", err)
	}

	// Validate source VM exists and is powered off
	if !VMExists(ctx, srcVM) {
		return fmt.Errorf("source VM %q does not exist", srcVM)
	}
	state, err := VMState(ctx, srcVM)
	if err != nil {
		return fmt.Errorf("cannot determine state of %q: %w", srcVM, err)
	}
	if state != "poweroff" && state != "aborted" {
		return fmt.Errorf("source VM %q must be powered off (current state: %s); run 'dexbox vm stop %s' first", srcVM, state, srcVM)
	}

	// Validate destination does not exist
	if VMExists(ctx, dstVM) {
		return fmt.Errorf("VM %q already exists", dstVM)
	}

	// Clone
	fmt.Printf("Cloning VM %q → %q...\n", srcVM, dstVM)
	if err := CloneVM(ctx, srcVM, dstVM); err != nil {
		return fmt.Errorf("clone VM: %w", err)
	}
	fmt.Println("Clone complete.")

	// Re-add shared folder (the clone inherits the source's mapping;
	// remove and re-add to ensure the path matches the current host config)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}
	sharedDir := filepath.Join(home, ".dexbox", "shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		return fmt.Errorf("create shared dir: %w", err)
	}
	_, _ = RunVBoxManage(ctx, "sharedfolder", "remove", dstVM, "--name", "shared")
	if err := AddSharedFolder(ctx, dstVM, "shared", sharedDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not add shared folder: %v\n", err)
	}

	// Done
	fmt.Println("")
	fmt.Printf("VM %q cloned from %q successfully!\n", dstVM, srcVM)
	fmt.Printf("  Credentials: [same as %q]\n", srcVM)
	fmt.Printf("  Shared dir:  %s\n", sharedDir)
	fmt.Println("")
	fmt.Println("Next steps:")
	fmt.Println("  dexbox start     # Start the tool server")
	fmt.Println("  dexbox status    # Check VM state")
	return nil
}

func ensureVirtualBox() error {
	if _, err := exec.LookPath("VBoxManage"); err == nil {
		out, err := exec.Command("VBoxManage", "--version").Output()
		if err == nil {
			fmt.Printf("VirtualBox %s already installed.\n", string(out[:len(out)-1]))
			return nil
		}
	}

	fmt.Println("Installing VirtualBox...")
	switch runtime.GOOS {
	case "darwin":
		return runCmd("brew", "install", "--cask", "virtualbox")
	case "linux":
		// Try apt first, then dnf
		if _, err := exec.LookPath("apt"); err == nil {
			return runCmd("sudo", "apt", "install", "-y", "virtualbox")
		}
		if _, err := exec.LookPath("dnf"); err == nil {
			return runCmd("sudo", "dnf", "install", "-y", "VirtualBox")
		}
		return fmt.Errorf("unsupported Linux distro: install VirtualBox manually")
	case "windows":
		return runCmd("winget", "install", "Oracle.VirtualBox")
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

var downloadClient = &http.Client{Timeout: 30 * time.Minute}

// downloadFile downloads url to destPath with progress output and SHA256 logging.
// It writes to a temporary file first, then atomically renames on success.
func downloadFile(ctx context.Context, url, destPath, displayName string) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	fmt.Printf("Downloading %s...\n", displayName)
	fmt.Printf("  URL: %s\n", url)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, displayName)
	}

	tmpPath := destPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmpPath)
	}()

	total := resp.ContentLength
	hash := sha256.New()
	writer := io.MultiWriter(f, hash)

	var written int64
	buf := make([]byte, 32*1024)
	lastReport := time.Now()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := writer.Write(buf[:n]); wErr != nil {
				return wErr
			}
			written += int64(n)

			if time.Since(lastReport) > 2*time.Second {
				if total > 0 {
					pct := float64(written) / float64(total) * 100
					fmt.Printf("  %.1f%% (%d / %d MB)\n", pct, written/(1024*1024), total/(1024*1024))
				} else {
					fmt.Printf("  %d MB downloaded\n", written/(1024*1024))
				}
				lastReport = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	f.Close()

	fmt.Printf("  SHA256: %s\n", hex.EncodeToString(hash.Sum(nil)))

	if err := os.Rename(tmpPath, destPath); err != nil {
		return err
	}

	fmt.Printf("  Saved to %s\n", destPath)
	return nil
}

// ensureISO validates that the user-provided Windows ISO path is a regular file.
func ensureISO(providedPath string) (string, error) {
	info, err := os.Stat(providedPath)
	if err != nil {
		return "", fmt.Errorf("ISO not found: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("ISO path is not a regular file: %s", providedPath)
	}
	fmt.Printf("Using ISO: %s\n", providedPath)
	return providedPath, nil
}

// ensureVirtioISO downloads the virtio-win ISO (for ARM VMs that need the
// NetKVM virtio network driver) and caches it in ~/.dexbox/iso/.
func ensureVirtioISO(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	isoPath := filepath.Join(home, isoCacheDir, virtioFilename)
	if _, err := os.Stat(isoPath); err == nil {
		fmt.Printf("virtio-win ISO already cached at %s\n", isoPath)
		return isoPath, nil
	}

	if err := downloadFile(ctx, virtioISOURL, isoPath, "virtio-win ISO (ARM network drivers)"); err != nil {
		return "", err
	}
	return isoPath, nil
}

func unattendedInstall(ctx context.Context, vmName, isoPath, user, pass string) (string, error) {
	// Build a small ISO containing autounattend.xml and attach it alongside the
	// Windows ISO. Windows Setup scans all attached removable media for
	// autounattend.xml, so this sidesteps the brittle "VBoxManage unattended
	// install" command which fails on Windows 11 Enterprise Eval ISOs.

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Write autounattend.xml into a staging directory.
	stageDir, err := os.MkdirTemp("", "dexbox-autounattend-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stageDir)

	xmlData := append([]byte(nil), autounattendXML...)
	if nativeArch() == "arm64" {
		xmlData = bytes.ReplaceAll(xmlData,
			[]byte(`processorArchitecture="amd64"`),
			[]byte(`processorArchitecture="arm64"`),
		)
		// Inject virtio-win driver paths so Windows PE loads the NetKVM
		// network driver during setup. Multiple drive letters are listed
		// because the virtio ISO letter is assigned dynamically.
		virtioComponent := `
        <component name="Microsoft-Windows-PnpCustomizationsWinPE" processorArchitecture="arm64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
            <DriverPaths>
                <PathAndCredentials wcm:action="add" wcm:keyValue="1">
                    <Path>D:\NetKVM\w11\ARM64</Path>
                </PathAndCredentials>
                <PathAndCredentials wcm:action="add" wcm:keyValue="2">
                    <Path>E:\NetKVM\w11\ARM64</Path>
                </PathAndCredentials>
                <PathAndCredentials wcm:action="add" wcm:keyValue="3">
                    <Path>F:\NetKVM\w11\ARM64</Path>
                </PathAndCredentials>
                <PathAndCredentials wcm:action="add" wcm:keyValue="4">
                    <Path>G:\NetKVM\w11\ARM64</Path>
                </PathAndCredentials>
                <PathAndCredentials wcm:action="add" wcm:keyValue="5">
                    <Path>H:\NetKVM\w11\ARM64</Path>
                </PathAndCredentials>
            </DriverPaths>
        </component>`
		xmlData = bytes.Replace(xmlData,
			[]byte(`    </settings>`),
			[]byte(virtioComponent+"\n    </settings>"),
			1, // only the first </settings> = end of windowsPE pass
		)
	}

	maxCount, _ := getMaxWimImageCount(isoPath)
	if maxCount < 1 {
		maxCount = 1
	}
	xmlData = bytes.ReplaceAll(xmlData,
		[]byte(`__IMAGE_INDEX__`),
		[]byte(fmt.Sprintf("%d", maxCount)),
	)
	xmlData = bytes.ReplaceAll(xmlData, []byte(`__VM_USER__`), []byte(xmlEscape(user)))
	xmlData = bytes.ReplaceAll(xmlData, []byte(`__VM_PASS__`), []byte(xmlEscape(pass)))
	if err := os.WriteFile(filepath.Join(stageDir, "autounattend.xml"), xmlData, 0o644); err != nil {
		return "", err
	}

	// Generate SetupComplete.cmd — Windows runs this as SYSTEM after setup
	// completes but before the first user logon. Much more reliable than
	// FirstLogonCommands on Windows 11 ARM, where a race condition with
	// driver initialization causes FirstLogonCommands to silently fail.
	certTool := `for %%d in (D E F G H) do if exist %%d:\cert\vbox-sha1.cer %%d:\cert\VBoxCertUtil.exe add-trusted-publisher %%d:\cert\vbox-sha1.cer %%d:\cert\vbox-sha256.cer`
	gaExe := "VBoxWindowsAdditions.exe"
	if nativeArch() == "arm64" {
		// Native certutil instead of x86 VBoxCertUtil; arm64 installer name.
		certTool = `for %%d in (D E F G H) do if exist %%d:\cert\vbox-sha1.cer certutil.exe -addstore TrustedPublisher %%d:\cert\vbox-sha1.cer
for %%d in (D E F G H) do if exist %%d:\cert\vbox-sha256.cer certutil.exe -addstore TrustedPublisher %%d:\cert\vbox-sha256.cer`
		gaExe = "VBoxWindowsAdditions-arm64.exe"
	}
	logLine := "echo [%%DATE%% %%TIME%%]"
	setupScript := fmt.Sprintf("@echo off\r\n"+
		"set LOG=C:\\Windows\\Setup\\Scripts\\SetupComplete.log\r\n"+
		"%[4]s SetupComplete.cmd starting >> %%LOG%%\r\n"+
		"REM === Create user and grant admin FIRST (GA install may reboot) ===\r\n"+
		"%[4]s Creating user >> %%LOG%%\r\n"+
		"net user dexbox dexbox123 /add >> %%LOG%% 2>&1\r\n"+
		"%[4]s Adding to Administrators >> %%LOG%%\r\n"+
		"net localgroup Administrators dexbox /add >> %%LOG%% 2>&1\r\n"+
		"REM === Security hardening ===\r\n"+
		"%[4]s Disabling Defender >> %%LOG%%\r\n"+
		"powershell -Command Set-MpPreference -DisableRealtimeMonitoring $true -ErrorAction SilentlyContinue\r\n"+
		"%[4]s Adding Defender exclusions >> %%LOG%%\r\n"+
		"powershell -Command \"Add-MpPreference -ExclusionPath '\\\\vboxsvr\\shared','C:\\dexbox','C:\\Users\\dexbox' -ErrorAction SilentlyContinue\"\r\n"+
		"%[4]s Opening firewall port 8600 >> %%LOG%%\r\n"+
		"netsh advfirewall firewall add rule name=dexbox-agent dir=in action=allow protocol=TCP localport=8600 >> %%LOG%% 2>&1\r\n"+
		"%[4]s Setting execution policy >> %%LOG%%\r\n"+
		"powershell -Command Set-ExecutionPolicy Bypass -Scope LocalMachine -Force\r\n"+
		"%[4]s Hardening complete >> %%LOG%%\r\n"+
		"REM === Guest Additions (may trigger reboot, must be last) ===\r\n"+
		"%[4]s Installing certificates >> %%LOG%%\r\n"+
		"%[1]s\r\n"+
		"%[4]s Waiting for drivers >> %%LOG%%\r\n"+
		"timeout /t 10 /nobreak\r\n"+
		"%[4]s Launching GA installer >> %%LOG%%\r\n"+
		"for %%d in (D E F G H) do if exist %%d:\\%[2]s %%d:\\%[2]s /S\r\n"+
		"%[4]s Scheduling reboot >> %%LOG%%\r\n"+
		"shutdown /r /t 30\r\n",
		certTool, gaExe, gaExe, logLine)
	_ = logLine
	if err := os.WriteFile(filepath.Join(stageDir, "SetupComplete.cmd"), []byte(setupScript), 0o644); err != nil {
		return "", err
	}

	// On ARM, add a startup.nsh EFI shell script as a fallback. If the
	// NVRAM boot entry is missing or invalid, OVMF drops to the EFI shell
	// which auto-runs startup.nsh after a short timeout. The script scans
	// all attached filesystems for the ARM64 boot loader.
	if nativeArch() == "arm64" {
		startupNsh := "echo Searching for Windows installer...\r\n"
		for i := 0; i <= 5; i++ {
			startupNsh += fmt.Sprintf("if exist fs%d:\\EFI\\BOOT\\bootaa64.efi then\r\n  fs%d:\\EFI\\BOOT\\bootaa64.efi\r\nendif\r\n", i, i)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "startup.nsh"), []byte(startupNsh), 0o644); err != nil {
			return "", err
		}
	}

	// Create a per-install ISO in the cache directory. The vmName is
	// included in the filename so parallel provisioning runs don't
	// clobber each other's answer files.
	isoDir := filepath.Join(home, isoCacheDir)
	if err := os.MkdirAll(isoDir, 0o700); err != nil {
		return "", fmt.Errorf("create iso cache dir: %w", err)
	}
	autounattendISO := filepath.Join(isoDir, fmt.Sprintf("autounattend-%s-%d.iso", filepath.Base(vmName), time.Now().UnixNano()))
	if err := createISO(stageDir, autounattendISO); err != nil {
		return "", fmt.Errorf("create autounattend ISO: %w", err)
	}
	if err := os.Chmod(autounattendISO, 0o600); err != nil {
		return "", fmt.Errorf("chmod autounattend ISO: %w", err)
	}

	// Attach the Windows installer ISO to SATA port 1.
	if err := AttachISO(ctx, vmName, isoPath); err != nil {
		return "", fmt.Errorf("attach Windows ISO: %w", err)
	}

	// Attach the autounattend ISO to SATA port 2.
	if _, err := RunVBoxManage(ctx, "storageattach", vmName,
		"--storagectl", "SATA", "--port", "2", "--device", "0",
		"--type", "dvddrive", "--medium", autounattendISO); err != nil {
		return "", fmt.Errorf("attach autounattend ISO: %w", err)
	}

	// Attach Guest Additions ISO to SATA port 3 so SetupComplete.cmd
	// can silently install them after Windows setup finishes.
	// Defensively ensure port count is at least 5 (ports 0-4) — VMs
	// created with older code may have fewer ports configured.
	if _, err := RunVBoxManage(ctx, "storagectl", vmName,
		"--name", "SATA", "--portcount", "5"); err != nil {
		return "", fmt.Errorf("set SATA port count: %w", err)
	}
	gaISO, err := GuestAdditionsISOPath()
	if err != nil {
		return "", fmt.Errorf("guest additions: %w", err)
	}
	if _, err := RunVBoxManage(ctx, "storageattach", vmName,
		"--storagectl", "SATA", "--port", "3", "--device", "0",
		"--type", "dvddrive", "--medium", gaISO); err != nil {
		return "", fmt.Errorf("attach Guest Additions ISO: %w", err)
	}

	// On ARM, attach the virtio-win ISO to SATA port 4 so Windows PE can
	// load the NetKVM network driver during installation.
	if nativeArch() == "arm64" {
		virtioISO, err := ensureVirtioISO(ctx)
		if err != nil {
			return "", fmt.Errorf("virtio-win ISO: %w", err)
		}
		if _, err := RunVBoxManage(ctx, "storageattach", vmName,
			"--storagectl", "SATA", "--port", "4", "--device", "0",
			"--type", "dvddrive", "--medium", virtioISO); err != nil {
			return "", fmt.Errorf("attach virtio-win ISO: %w", err)
		}
	}

	return autounattendISO, nil
}

// createISO wraps platform-native tools to produce an ISO 9660 image from srcDir.
func createISO(srcDir, destPath string) error {
	// Remove any stale output file; hdiutil and genisoimage both refuse to
	// overwrite an existing file.
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale ISO: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("hdiutil", "makehybrid",
			"-iso", "-joliet",
			"-o", destPath,
			srcDir,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("hdiutil: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	default:
		// Try genisoimage first (Debian/Ubuntu), then mkisofs (RHEL/Fedora/openSUSE).
		for _, tool := range []string{"genisoimage", "mkisofs"} {
			if _, lookErr := exec.LookPath(tool); lookErr != nil {
				continue
			}
			out, err := exec.Command(tool, "-o", destPath, srcDir).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s: %s: %w", tool, strings.TrimSpace(string(out)), err)
			}
			return nil
		}
		return fmt.Errorf("neither genisoimage nor mkisofs found; install one with your package manager")
	}
}

func waitForInstallation(ctx context.Context, vmName string) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeout := time.After(45 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf(
				"timeout waiting for Guest Additions to become active.\n"+
					"Troubleshooting:\n"+
					"  1. Verify the GA ISO is attached: VBoxManage showvminfo %s --machinereadable | grep SATA-3\n"+
					"  2. Check the VM log: ~/VirtualBox VMs/%s/Logs/VBox.log\n"+
					"  3. Try a manual GA install inside the VM",
				vmName, vmName)
		case <-ticker.C:
			if GuestAdditionsReady(ctx, vmName) {
				fmt.Println("Guest Additions active - installation complete!")
				return nil
			}
			state, _ := VMState(ctx, vmName)
			fmt.Printf("  VM state: %s, GA not ready, waiting...\n", state)
		}
	}
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// nativeArch returns the host CPU architecture, bypassing Rosetta 2.
// On Darwin it queries sysctl hw.machine; elsewhere it falls back to
// runtime.GOARCH (which reflects compile-time arch, not the host).
func nativeArch() string {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.machine").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return runtime.GOARCH
}

// getMaxWimImageCount scans an ISO for WIM headers (magic MSWIM\0\0\0 + cbSize 208)
// and returns the maximum ImageCount found in any embedded WIM. This efficiently
// finds how many OS editions are packed in the ISO so we can auto-select the last one.
func getMaxWimImageCount(isoPath string) (int, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// 32MB chunks to read the file sequentially
	buf := make([]byte, 32*1024*1024)
	magic := []byte("MSWIM\000\000\000\xD0\x00\x00\x00")
	var maxCount int

	var overflow []byte
	for {
		n, err := f.Read(buf[len(overflow):])
		if n == 0 && err != nil {
			break
		}
		chunk := buf[:len(overflow)+n]

		idx := 0
		for {
			pos := bytes.Index(chunk[idx:], magic)
			if pos == -1 {
				break
			}
			pos += idx

			// WIM header has ImageCount (32-bit LE) at offset 44
			if pos+48 > len(chunk) {
				break
			}

			count := int(binary.LittleEndian.Uint32(chunk[pos+44 : pos+48]))
			if count > maxCount {
				maxCount = count
			}
			idx = pos + 12
		}

		if len(chunk) > 48 {
			overflow = append([]byte(nil), chunk[len(chunk)-48:]...)
			copy(buf, overflow)
		} else {
			overflow = append([]byte(nil), chunk...)
			copy(buf, overflow)
		}
	}

	return maxCount, nil
}
