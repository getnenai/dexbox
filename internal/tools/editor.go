package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getnenai/dexbox/internal/vbox"
)

// EditorTool provides file viewing and editing inside a VirtualBox guest.
type EditorTool struct {
	vmName    string
	user      string
	pass      string
	sharedDir string // Host-side shared folder path
}

// NewEditorTool creates an editor tool bound to a specific VM.
func NewEditorTool(vmName, user, pass, sharedDir string) *EditorTool {
	return &EditorTool{
		vmName:    vmName,
		user:      user,
		pass:      pass,
		sharedDir: sharedDir,
	}
}

// Execute dispatches an editor action (view, create, str_replace, insert).
func (t *EditorTool) Execute(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	var p EditorParams
	if err := action.UnmarshalParams(&p); err != nil {
		return nil, fmt.Errorf("invalid editor params: %w", err)
	}

	if p.Path == "" {
		return nil, fmt.Errorf("field 'path' is required")
	}

	switch p.Command {
	case "view":
		return t.view(ctx, p.Path)
	case "create":
		return t.create(ctx, p.Path, p.FileText)
	case "str_replace":
		return t.strReplace(ctx, p.Path, p.OldStr, p.NewStr)
	case "insert":
		return t.insert(ctx, p.Path, p.InsertLine, p.NewStr)
	default:
		return nil, fmt.Errorf("unknown editor command %q", p.Command)
	}
}

func (t *EditorTool) view(ctx context.Context, path string) (*CanonicalResult, error) {
	out, err := vbox.GuestRun(ctx, t.vmName, t.user, t.pass,
		"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("Get-Content -Raw '%s'", escapePSString(path)))
	if err != nil {
		return nil, err
	}
	return &CanonicalResult{Output: out}, nil
}

func (t *EditorTool) create(ctx context.Context, path, content string) (*CanonicalResult, error) {
	tmpName := fmt.Sprintf("dexbox-tmp-%d.txt", os.Getpid())
	hostTmp := filepath.Join(t.sharedDir, tmpName)

	if err := os.MkdirAll(t.sharedDir, 0o755); err != nil {
		return nil, fmt.Errorf("create shared dir: %w", err)
	}
	if err := os.WriteFile(hostTmp, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	defer os.Remove(hostTmp)

	guestShared := fmt.Sprintf("\\\\vboxsvr\\shared\\%s", tmpName)
	_, err := vbox.GuestRun(ctx, t.vmName, t.user, t.pass,
		"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("Copy-Item '%s' '%s' -Force", escapePSString(guestShared), escapePSString(path)))
	if err != nil {
		return nil, err
	}

	return &CanonicalResult{Output: fmt.Sprintf("Created %s", path)}, nil
}

func (t *EditorTool) strReplace(ctx context.Context, path, oldStr, newStr string) (*CanonicalResult, error) {
	if oldStr == "" {
		return nil, fmt.Errorf("field 'old_str' is required for str_replace")
	}

	viewResult, err := t.view(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading file for str_replace: %w", err)
	}

	content := viewResult.Output
	count := strings.Count(content, oldStr)
	if count == 0 {
		return nil, fmt.Errorf("old_str not found in %s", path)
	}
	if count > 1 {
		return nil, fmt.Errorf("old_str found %d times in %s, must be unique", count, path)
	}

	return t.create(ctx, path, strings.Replace(content, oldStr, newStr, 1))
}

func (t *EditorTool) insert(ctx context.Context, path string, insertLine *int, newStr string) (*CanonicalResult, error) {
	if insertLine == nil {
		return nil, fmt.Errorf("field 'insert_line' is required for insert")
	}

	viewResult, err := t.view(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading file for insert: %w", err)
	}

	lines := strings.Split(viewResult.Output, "\n")
	lineIdx := *insertLine
	if lineIdx < 0 {
		lineIdx = 0
	}
	if lineIdx > len(lines) {
		lineIdx = len(lines)
	}

	newLines := strings.Split(newStr, "\n")
	result := make([]string, 0, len(lines)+len(newLines))
	result = append(result, lines[:lineIdx]...)
	result = append(result, newLines...)
	result = append(result, lines[lineIdx:]...)

	return t.create(ctx, path, strings.Join(result, "\n"))
}

// escapePSString escapes single quotes for PowerShell string literals.
func escapePSString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
