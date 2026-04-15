package vbox

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RunVBoxManage executes VBoxManage with the given arguments and returns stdout.
// Stderr is wrapped in the returned error on non-zero exit. A 30-second timeout
// is applied to prevent hanging indefinitely.
func RunVBoxManage(ctx context.Context, args ...string) (string, error) {
	return runVBoxManageTimeout(ctx, 30*time.Second, args...)
}

// runVBoxManageTimeout is the shared implementation for RunVBoxManage with a
// caller-specified timeout. Long-running operations (e.g. clonevm) use this
// directly with a larger deadline.
func runVBoxManageTimeout(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "VBoxManage", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("VBoxManage %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

// Screenshot captures a PNG screenshot of the VM display to a temp file,
// reads it, and returns the raw PNG bytes.
func Screenshot(ctx context.Context, vmName string) ([]byte, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("dexbox-screenshot-%s.png", vmName))
	defer os.Remove(tmp)

	if _, err := RunVBoxManage(ctx, "controlvm", vmName, "screenshotpng", tmp); err != nil {
		return nil, err
	}
	return os.ReadFile(tmp)
}

// SendScancodes sends PS/2 keyboard scancodes to the VM.
// Note: only works on VMs with PS/2 keyboard emulation (x86). Use
// SendKeyboardString on ARM VMs which use USB HID keyboard emulation.
func SendScancodes(ctx context.Context, vmName string, codes []string) error {
	if len(codes) == 0 {
		return nil
	}
	args := append([]string{"controlvm", vmName, "keyboardputscancode"}, codes...)
	_, err := RunVBoxManage(ctx, args...)
	return err
}

// SendKeyboardString injects a string into the VM keyboard using VirtualBox's
// high-level keyboard interface. Unlike SendScancodes, this works on ARM VMs
// which use USB HID keyboard emulation rather than PS/2.
func SendKeyboardString(ctx context.Context, vmName, text string) error {
	_, err := RunVBoxManage(ctx, "controlvm", vmName, "keyboardputstring", text)
	return err
}

// StartVM boots a VM in headless or GUI mode.
func StartVM(ctx context.Context, vmName string, headless bool) error {
	vmType := "gui"
	if headless {
		vmType = "headless"
	}
	_, err := RunVBoxManage(ctx, "startvm", vmName, "--type", vmType)
	return err
}

// ControlVM sends a control command (acpipowerbutton, pause, resume, savestate, etc.).
func ControlVM(ctx context.Context, vmName, action string) error {
	_, err := RunVBoxManage(ctx, "controlvm", vmName, action)
	return err
}

// SetVideoMode sets the VM display resolution via Guest Additions.
// Requires Guest Additions to be running (RunLevel >= 2).
func SetVideoMode(ctx context.Context, vmName string, width, height, bpp int) error {
	_, err := RunVBoxManage(ctx, "controlvm", vmName, "setvideomodehint",
		fmt.Sprintf("%d", width), fmt.Sprintf("%d", height), fmt.Sprintf("%d", bpp))
	return err
}

// VMState returns the current state of the VM (e.g. "running", "poweroff", "paused", "saved").
func VMState(ctx context.Context, vmName string) (string, error) {
	out, err := RunVBoxManage(ctx, "showvminfo", vmName, "--machinereadable")
	if err != nil {
		return "", err
	}
	return parseMachineReadable(out, "VMState"), nil
}

// VMExists returns true if the named VM is registered with VirtualBox.
func VMExists(ctx context.Context, vmName string) bool {
	_, err := RunVBoxManage(ctx, "showvminfo", vmName, "--machinereadable")
	return err == nil
}

// VMOSType returns the ostype string for the named VM, e.g. "Windows11_64"
// or "Windows11_arm64". Returns an empty string on error.
func VMOSType(ctx context.Context, vmName string) string {
	out, err := RunVBoxManage(ctx, "showvminfo", vmName, "--machinereadable")
	if err != nil {
		return ""
	}
	return parseMachineReadable(out, "ostype")
}

// GuestAdditionsReady returns true if the Guest Additions in the VM are active.
func GuestAdditionsReady(ctx context.Context, vmName string) bool {
	out, err := RunVBoxManage(ctx, "showvminfo", vmName, "--machinereadable")
	if err != nil {
		return false
	}
	// GuestAdditionsRunLevel >= 2 means fully running (level 3 = desktop integration)
	level := parseMachineReadable(out, "GuestAdditionsRunLevel")
	n, err := strconv.Atoi(level)
	return err == nil && n >= 2
}


// GuestRun executes a program inside the guest OS via Guest Additions.
func GuestRun(ctx context.Context, vmName, user, pass, exe string, args ...string) (string, error) {
	cmdArgs := []string{
		"guestcontrol", vmName, "run",
		"--username", user, "--password", pass,
		"--exe", exe,
		"--wait-stdout", "--wait-stderr",
		"--",
	}
	// The first arg after -- is argv[0] (program name), then actual arguments.
	cmdArgs = append(cmdArgs, filepath.Base(exe))
	cmdArgs = append(cmdArgs, args...)

	out, err := RunVBoxManage(ctx, cmdArgs...)
	return out, err
}

// GuestCopyToGuest copies a file from host to the guest OS.
func GuestCopyToGuest(ctx context.Context, vmName, user, pass, hostPath, guestPath string) error {
	_, err := RunVBoxManage(ctx,
		"guestcontrol", vmName, "copyto",
		"--username", user, "--password", pass,
		"--target-directory", filepath.Dir(guestPath),
		hostPath,
	)
	return err
}

// GuestCopyFromGuest copies a file from the guest OS to the host.
func GuestCopyFromGuest(ctx context.Context, vmName, user, pass, guestPath, hostPath string) error {
	_, err := RunVBoxManage(ctx,
		"guestcontrol", vmName, "copyfrom",
		"--username", user, "--password", pass,
		"--target-directory", filepath.Dir(hostPath),
		guestPath,
	)
	return err
}

// VMConfig holds VM creation parameters.
type VMConfig struct {
	CPUs     int
	MemoryMB int
	VRAMmb   int
	DiskGB   int
}

// DefaultVMConfig returns sensible defaults for a Windows 11 VM.
func DefaultVMConfig() VMConfig {
	return VMConfig{
		CPUs:     4,
		MemoryMB: 8192,
		VRAMmb:   128,
		DiskGB:   64,
	}
}

// Validate checks that the VM configuration has sane resource bounds.
func (c VMConfig) Validate() error {
	if c.CPUs < 1 {
		return fmt.Errorf("cpus must be at least 1 (got %d)", c.CPUs)
	}
	if c.MemoryMB < 2048 {
		return fmt.Errorf("memory must be at least 2048 MB (got %d MB)", c.MemoryMB)
	}
	if c.DiskGB < 32 {
		return fmt.Errorf("disk must be at least 32 GB (got %d GB)", c.DiskGB)
	}
	return nil
}

// CreateVM registers and configures a new VM. It does NOT start it.
func CreateVM(ctx context.Context, name string, cfg VMConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid VM config: %w", err)
	}

	ostype := "Windows11_64"
	if nativeArch() == "arm64" {
		ostype = "Windows11_arm64"
	}

	// Register VM
	if _, err := RunVBoxManage(ctx, "createvm",
		"--name", name, "--ostype", ostype, "--register"); err != nil {
		return fmt.Errorf("createvm: %w", err)
	}

	// Configure VM
	modifyArgs := []string{
		"--cpus", fmt.Sprintf("%d", cfg.CPUs),
		"--memory", fmt.Sprintf("%d", cfg.MemoryMB),
		"--vram", fmt.Sprintf("%d", cfg.VRAMmb),
		"--firmware", "efi",
		"--graphicscontroller", "vboxsvga",
		"--accelerate3d", "on",
		"--nic1", "nat",
		"--audio-driver", "none",
		"--tpm-type", "2.0",
		"--boot1", "dvd", "--boot2", "disk", "--boot3", "none", "--boot4", "none",
	}
	if nativeArch() == "arm64" {
		// ARM VMs default to a USB-based NIC which requires USB controller
		// setup that isn't available by default; virtio works reliably instead.
		// Additionally, ARM VMs must use USB for keyboard and mouse since PS/2 is not supported.
		modifyArgs = append(modifyArgs, "--nictype1", "virtio", "--usb-xhci", "on", "--keyboard", "usb", "--mouse", "usbtablet")
	}
	if _, err := RunVBoxManage(ctx, append([]string{"modifyvm", name}, modifyArgs...)...); err != nil {
		return fmt.Errorf("modifyvm: %w", err)
	}

	// Create storage controller (5 ports: disk, Windows ISO, autounattend ISO,
	// Guest Additions ISO, and optionally virtio-win ISO on ARM)
	if _, err := RunVBoxManage(ctx, "storagectl", name,
		"--name", "SATA", "--add", "sata", "--controller", "IntelAhci",
		"--portcount", "5"); err != nil {
		return fmt.Errorf("storagectl: %w", err)
	}

	// Create virtual disk
	diskDir, _ := RunVBoxManage(ctx, "list", "systemproperties")
	diskPath := filepath.Join(parseMachineFolder(diskDir), name, name+".vdi")
	if _, err := RunVBoxManage(ctx, "createmedium", "disk",
		"--filename", diskPath,
		"--size", fmt.Sprintf("%d", cfg.DiskGB*1024),
		"--format", "VDI"); err != nil {
		return fmt.Errorf("createmedium: %w", err)
	}

	// Attach disk
	if _, err := RunVBoxManage(ctx, "storageattach", name,
		"--storagectl", "SATA", "--port", "0", "--device", "0",
		"--type", "hdd", "--medium", diskPath); err != nil {
		return fmt.Errorf("storageattach disk: %w", err)
	}

	// On ARM, OVMF shows an interactive "press any key to boot from CD or DVD"
	// prompt for optically-discovered boot devices, and keyboard injection does
	// not work during the EFI phase. Inject a Boot0000 NVRAM entry pointing
	// directly to \EFI\BOOT\bootaa64.efi so OVMF boots the DVD via BootOrder
	// without any prompt.
	if nativeArch() == "arm64" {
		if err := setupEFIBootDVD(ctx, name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: EFI NVRAM boot setup failed (%v). You may need to focus the VM window and press Space quickly to boot from CD.\n", err)
		}
	}

	return nil
}

// CloneVM clones an existing VM to create a new one. The source must be
// powered off or aborted. VBoxManage handles disk cloning, UUID regeneration,
// and MAC address regeneration automatically. A 5-minute timeout is used
// because cloning a large VDI can exceed the default 30 seconds.
func CloneVM(ctx context.Context, src, dst string) error {
	_, err := runVBoxManageTimeout(ctx, 5*time.Minute, "clonevm", src, "--name", dst, "--register")
	return err
}

// setupEFIBootDVD initialises the OVMF NVRAM variable store and injects
// Boot0000/BootOrder/Timeout variables directly into the NVRAM file. This
// causes OVMF to boot the DVD installer via a BootOrder entry rather than
// through its removable-media fallback scan, bypassing the interactive
// "press any key to boot from CD or DVD" prompt entirely.
func setupEFIBootDVD(ctx context.Context, vmName string) error {
	if _, err := RunVBoxManage(ctx, "modifynvram", vmName, "inituefivarstore"); err != nil {
		return fmt.Errorf("inituefivarstore: %w", err)
	}
	sysProps, _ := RunVBoxManage(ctx, "list", "systemproperties")
	nvramPath := filepath.Join(parseMachineFolder(sysProps), vmName, vmName+".nvram")
	if err := patchNVRAMForDVDBoot(nvramPath); err != nil {
		return err
	}
	// Verify the variables were written by listing them.
	out, err := RunVBoxManage(ctx, "modifynvram", vmName, "listvars")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not verify NVRAM variables: %v\n", err)
	} else {
		fmt.Printf("NVRAM boot variables after patching:\n%s\n", out)
	}
	return nil
}

// AttachISO attaches an ISO to the VM's SATA controller.
func AttachISO(ctx context.Context, vmName, isoPath string) error {
	_, err := RunVBoxManage(ctx, "storageattach", vmName,
		"--storagectl", "SATA", "--port", "1", "--device", "0",
		"--type", "dvddrive", "--medium", isoPath)
	return err
}

// DeleteVM unregisters and deletes a VM and its associated files.
// If the VM is running or saved it is powered off first.
func DeleteVM(ctx context.Context, name string) error {
	if !VMExists(ctx, name) {
		return fmt.Errorf("VM %q does not exist", name)
	}

	// Power off if locked (running, paused, or saved).
	if out, _ := RunVBoxManage(ctx, "showvminfo", name, "--machinereadable"); out != "" {
		state := parseMachineReadable(out, "VMState")
		if state != "poweroff" && state != "aborted" && state != "" {
			_, _ = RunVBoxManage(ctx, "controlvm", name, "poweroff")
			// Poll until the VM reaches "poweroff" state.
			for i := 0; i < 15; i++ {
				time.Sleep(time.Second)
				s, _ := VMState(ctx, name)
				if s == "poweroff" {
					break
				}
			}
		}
	}
	_, err := RunVBoxManage(ctx, "unregistervm", name, "--delete")
	return err
}

// ListVMs returns a list of all VMs registered with VirtualBox.
func ListVMs(ctx context.Context) ([]string, error) {
	out, err := RunVBoxManage(ctx, "list", "vms")
	if err != nil {
		return nil, err
	}
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "VM Name" {uuid}
		if idx := strings.Index(line, "\""); idx >= 0 {
			end := strings.Index(line[idx+1:], "\"")
			if end >= 0 {
				names = append(names, line[idx+1:idx+1+end])
			}
		}
	}
	return names, nil
}

// AddSharedFolder adds a shared folder mapping to the VM.
func AddSharedFolder(ctx context.Context, vmName, shareName, hostPath string) error {
	_, err := RunVBoxManage(ctx, "sharedfolder", "add", vmName,
		"--name", shareName, "--hostpath", hostPath, "--automount")
	return err
}

// GuestAdditionsISOPath returns the path to the VBoxGuestAdditions.iso bundled
// with the VirtualBox installation.
func GuestAdditionsISOPath() (string, error) {
	candidates := []string{
		// macOS
		"/Applications/VirtualBox.app/Contents/MacOS/VBoxGuestAdditions.iso",
		// Linux (Debian/Ubuntu)
		"/usr/lib/virtualbox/additions/VBoxGuestAdditions.iso",
		// Linux (Fedora/RHEL/openSUSE)
		"/usr/share/virtualbox/VBoxGuestAdditions.iso",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("VBoxGuestAdditions.iso not found; ensure VirtualBox is installed")
}

// parseMachineReadable extracts a value from VBoxManage --machinereadable output.
func parseMachineReadable(output, key string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	prefix := key + "="
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, prefix) {
			val := line[len(prefix):]
			return strings.Trim(val, "\"")
		}
	}
	return ""
}

// parseMachineFolder extracts the default machine folder from "list systemproperties" output.
func parseMachineFolder(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Default machine folder:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Default machine folder:"))
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "VirtualBox VMs")
}
