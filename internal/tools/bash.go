package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/getnenai/dexbox/internal/vbox"
)

// BashTool executes PowerShell commands inside a VirtualBox guest.
type BashTool struct {
	vmName string
	user   string
	pass   string
}

// NewBashTool creates a bash tool bound to a specific VM.
func NewBashTool(vmName, user, pass string) *BashTool {
	return &BashTool{vmName: vmName, user: user, pass: pass}
}

// Execute runs a PowerShell command in the guest and returns its output.
func (t *BashTool) Execute(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if command == "" {
		return "", fmt.Errorf("field 'command' is required")
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	out, err := vbox.GuestRun(ctx, t.vmName, t.user, t.pass,
		"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", command)
	if err != nil {
		return "", err
	}

	return out, nil
}
