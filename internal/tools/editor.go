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

// Execute dispatches an editor action (view, create, str_replace, insert, undo_edit).
func (t *EditorTool) Execute(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	command, _ := action.Params["command"].(string)
	path, _ := action.Params["path"].(string)

	if path == "" {
		return nil, fmt.Errorf("field 'path' is required")
	}

	switch command {
	case "view":
		return t.view(ctx, path, action)
	case "create":
		return t.create(ctx, path, action)
	case "str_replace":
		return t.strReplace(ctx, path, action)
	case "insert":
		return t.insert(ctx, path, action)
	default:
		return nil, fmt.Errorf("unknown editor command %q", command)
	}
}

func (t *EditorTool) view(ctx context.Context, path string, action *CanonicalAction) (*CanonicalResult, error) {
	out, err := vbox.GuestRun(ctx, t.vmName, t.user, t.pass,
		"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("Get-Content -Raw '%s'", escapePSString(path)))
	if err != nil {
		return nil, err
	}
	return &CanonicalResult{Output: out}, nil
}

func (t *EditorTool) create(ctx context.Context, path string, action *CanonicalAction) (*CanonicalResult, error) {
	content, _ := action.Params["file_text"].(string)

	// Write content to a temp file on the host via shared folder
	tmpName := fmt.Sprintf("dexbox-tmp-%d.txt", os.Getpid())
	hostTmp := filepath.Join(t.sharedDir, tmpName)

	if err := os.MkdirAll(t.sharedDir, 0o755); err != nil {
		return nil, fmt.Errorf("create shared dir: %w", err)
	}
	if err := os.WriteFile(hostTmp, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	defer os.Remove(hostTmp)

	// Copy from shared folder to target path in the guest
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

func (t *EditorTool) strReplace(ctx context.Context, path string, action *CanonicalAction) (*CanonicalResult, error) {
	oldStr, _ := action.Params["old_str"].(string)
	newStr, _ := action.Params["new_str"].(string)

	if oldStr == "" {
		return nil, fmt.Errorf("field 'old_str' is required for str_replace")
	}

	// Read current content
	viewResult, err := t.view(ctx, path, action)
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

	newContent := strings.Replace(content, oldStr, newStr, 1)

	// Write back via create
	createAction := &CanonicalAction{
		Tool:   "text_editor",
		Action: "create",
		Params: map[string]any{"path": path, "file_text": newContent},
	}
	return t.create(ctx, path, createAction)
}

func (t *EditorTool) insert(ctx context.Context, path string, action *CanonicalAction) (*CanonicalResult, error) {
	insertLine, ok := action.Params["insert_line"].(float64)
	if !ok {
		return nil, fmt.Errorf("field 'insert_line' is required for insert")
	}
	newStr, _ := action.Params["new_str"].(string)

	// Read current content
	viewResult, err := t.view(ctx, path, action)
	if err != nil {
		return nil, fmt.Errorf("reading file for insert: %w", err)
	}

	lines := strings.Split(viewResult.Output, "\n")
	lineIdx := int(insertLine)
	if lineIdx < 0 {
		lineIdx = 0
	}
	if lineIdx > len(lines) {
		lineIdx = len(lines)
	}

	// Insert new lines at position
	newLines := strings.Split(newStr, "\n")
	result := make([]string, 0, len(lines)+len(newLines))
	result = append(result, lines[:lineIdx]...)
	result = append(result, newLines...)
	result = append(result, lines[lineIdx:]...)

	newContent := strings.Join(result, "\n")
	createAction := &CanonicalAction{
		Tool:   "text_editor",
		Action: "create",
		Params: map[string]any{"path": path, "file_text": newContent},
	}
	return t.create(ctx, path, createAction)
}

// escapePSString escapes single quotes for PowerShell string literals.
func escapePSString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
