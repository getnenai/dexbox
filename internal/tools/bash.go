package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/getnenai/dexbox/internal/vbox"
)

// maxOutputLen caps bash output to prevent excessively large responses
// (e.g. PowerShell help page dumps) from cluttering agent logs.
const maxOutputLen = 10_000

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

	return sanitizeOutput(out), nil
}

// sanitizeOutput cleans raw VBoxManage guest output for readability:
//   - normalizes \r\n → \n and strips stray \r
//   - trims trailing whitespace from each line
//   - collapses runs of 3+ blank lines into a single blank line
//   - trims leading/trailing blank lines
//   - truncates to maxOutputLen with a marker
func sanitizeOutput(raw string) string {
	// Normalize line endings.
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")

	// Trim trailing whitespace per line and collapse blank runs.
	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blanks++
			if blanks <= 1 {
				cleaned = append(cleaned, line)
			}
		} else {
			blanks = 0
			cleaned = append(cleaned, line)
		}
	}

	result := strings.TrimSpace(strings.Join(cleaned, "\n"))

	// Truncate if too long.
	runes := []rune(result)
	if len(runes) > maxOutputLen {
		half := maxOutputLen / 2
		truncated := len(runes) - maxOutputLen
		result = string(runes[:half]) +
			fmt.Sprintf("\n\n... (%d characters truncated) ...\n\n", truncated) +
			string(runes[len(runes)-half:])
	}

	return result
}
