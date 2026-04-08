package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"
)

// editorTimeout is the default timeout for individual PowerShell commands
// issued by the editor tool.
const editorTimeout = 30 * time.Second

// PsQuote wraps s in PowerShell single-quotes, doubling any embedded single
// quotes.  Single-quoted strings in PowerShell are fully literal — no $variable
// expansion, no backtick interpretation — so this is the safe way to embed
// arbitrary paths and search terms in a -Command argument.
func PsQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psBase64Expr returns a PowerShell expression that decodes a base64-encoded
// UTF-8 string at runtime.  This is the safest way to pass arbitrary multiline
// content (file text, search/replace strings) into PowerShell without worrying
// about quoting issues.
func psBase64Expr(content string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf(
		"[System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))",
		encoded,
	)
}

// EditorTool provides file viewing and editing on a Windows guest VM via
// PowerShell.  It implements the Anthropic text_editor_20250124 commands:
// view, create, str_replace, insert, and undo_edit.
type EditorTool struct {
	bash   *BashTool
	undoMu sync.Mutex
	// undoMap stores the previous file content keyed by path, enabling
	// single-level undo.  Only mutating operations populate this map.
	undoMap map[string]string
}

// NewEditorTool creates an editor tool that delegates PowerShell execution
// to the given BashTool.
func NewEditorTool(bash *BashTool) *EditorTool {
	return &EditorTool{
		bash:    bash,
		undoMap: make(map[string]string),
	}
}

// Execute dispatches a text_editor action.
func (t *EditorTool) Execute(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	var p EditorParams
	if err := action.UnmarshalParams(&p); err != nil {
		return nil, fmt.Errorf("invalid editor params: %w", err)
	}
	if p.Path == "" {
		return nil, fmt.Errorf("field 'path' is required")
	}

	switch action.Action {
	case "view":
		return t.view(ctx, p)
	case "create":
		return t.create(ctx, p)
	case "str_replace":
		return t.strReplace(ctx, p)
	case "insert":
		return t.insert(ctx, p)
	case "undo_edit":
		return t.undoEdit(ctx, p)
	default:
		return nil, fmt.Errorf("unknown editor command %q", action.Action)
	}
}

// view reads a file and returns its content with line numbers.
func (t *EditorTool) view(ctx context.Context, p EditorParams) (*CanonicalResult, error) {
	path := PsQuote(p.Path)

	var cmd string
	if p.ViewRange != nil {
		start := p.ViewRange[0]
		end := p.ViewRange[1]
		if start < 1 {
			return nil, fmt.Errorf("view_range start must be >= 1, got %d", start)
		}
		if end < start {
			return nil, fmt.Errorf("view_range end (%d) must be >= start (%d)", end, start)
		}
		// Read lines, select the range (1-based), and format with line numbers.
		cmd = fmt.Sprintf(
			`$lines = Get-Content -Path %s -Encoding UTF8; `+
				`$start = %d; $end = [Math]::Min(%d, $lines.Count); `+
				`for ($i = $start; $i -le $end; $i++) { '{0,6}`+"\t"+`{1}' -f $i, $lines[$i-1] }`,
			path, start, end,
		)
	} else {
		// Read all lines and format with line numbers.
		cmd = fmt.Sprintf(
			`$lines = Get-Content -Path %s -Encoding UTF8; `+
				`for ($i = 0; $i -lt $lines.Count; $i++) { '{0,6}`+"\t"+`{1}' -f ($i+1), $lines[$i] }`,
			path,
		)
	}

	out, err := t.bash.Execute(ctx, cmd, editorTimeout)
	if err != nil {
		return nil, err
	}
	return &CanonicalResult{Output: out}, nil
}

// create writes content to a new file, creating parent directories as needed.
func (t *EditorTool) create(ctx context.Context, p EditorParams) (*CanonicalResult, error) {
	if p.FileText == "" {
		return nil, fmt.Errorf("field 'file_text' is required for create command")
	}

	path := PsQuote(p.Path)
	content := psBase64Expr(p.FileText)

	cmd := fmt.Sprintf(
		`$p = %s; `+
			`$dir = Split-Path -Parent $p; `+
			`if ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }; `+
			`%s | Set-Content -Path $p -NoNewline -Encoding UTF8; `+
			`Write-Output "File created: $p"`,
		path, content,
	)

	out, err := t.bash.Execute(ctx, cmd, editorTimeout)
	if err != nil {
		return nil, err
	}
	return &CanonicalResult{Output: out}, nil
}

// strReplace finds and replaces a unique string in a file.
func (t *EditorTool) strReplace(ctx context.Context, p EditorParams) (*CanonicalResult, error) {
	if p.OldStr == "" {
		return nil, fmt.Errorf("field 'old_str' is required for str_replace command")
	}

	// Snapshot current content for undo.
	snapshot, err := t.readFile(ctx, p.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file for str_replace: %w", err)
	}

	path := PsQuote(p.Path)
	oldExpr := psBase64Expr(p.OldStr)
	newExpr := psBase64Expr(p.NewStr)

	cmd := fmt.Sprintf(
		`$p = %s; `+
			`$content = Get-Content -Path $p -Raw -Encoding UTF8; `+
			`$old = %s; `+
			`$new = %s; `+
			`$count = [regex]::Matches($content, [regex]::Escape($old)).Count; `+
			`if ($count -eq 0) { throw "old_str not found in file" }; `+
			`if ($count -gt 1) { throw "old_str found $count times; must be unique (appear exactly once)" }; `+
			`$result = $content.Replace($old, $new); `+
			`[System.IO.File]::WriteAllText($p, $result); `+
			`Write-Output "Replacement applied successfully."`,
		path, oldExpr, newExpr,
	)

	out, err := t.bash.Execute(ctx, cmd, editorTimeout)
	if err != nil {
		return nil, err
	}

	// Store snapshot for undo only after successful replacement.
	t.undoMu.Lock()
	t.undoMap[p.Path] = snapshot
	t.undoMu.Unlock()

	return &CanonicalResult{Output: out}, nil
}

// insert inserts text at a specific line number.
func (t *EditorTool) insert(ctx context.Context, p EditorParams) (*CanonicalResult, error) {
	if p.NewStr == "" {
		return nil, fmt.Errorf("field 'new_str' is required for insert command")
	}
	if p.InsertLine < 0 {
		return nil, fmt.Errorf("field 'insert_line' must be >= 0, got %d", p.InsertLine)
	}

	// Snapshot current content for undo.
	snapshot, err := t.readFile(ctx, p.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file for insert: %w", err)
	}

	path := PsQuote(p.Path)
	newExpr := psBase64Expr(p.NewStr)

	cmd := fmt.Sprintf(
		`$p = %s; `+
			`$lines = @(Get-Content -Path $p -Encoding UTF8); `+
			`$newText = %s; `+
			`$newLines = $newText -split "`+"`n"+`" | ForEach-Object { $_ -replace "`+"`r"+`$" }; `+
			`$insertAt = %d; `+
			`if ($insertAt -gt $lines.Count) { $insertAt = $lines.Count }; `+
			`$before = @(); if ($insertAt -gt 0) { $before = $lines[0..($insertAt-1)] }; `+
			`$after = @(); if ($insertAt -lt $lines.Count) { $after = $lines[$insertAt..($lines.Count-1)] }; `+
			`$result = @($before) + @($newLines) + @($after); `+
			`$result | Set-Content -Path $p -Encoding UTF8; `+
			`Write-Output "Inserted text at line $insertAt."`,
		path, newExpr, p.InsertLine,
	)

	out, err := t.bash.Execute(ctx, cmd, editorTimeout)
	if err != nil {
		return nil, err
	}

	// Store snapshot for undo only after successful insertion.
	t.undoMu.Lock()
	t.undoMap[p.Path] = snapshot
	t.undoMu.Unlock()

	return &CanonicalResult{Output: out}, nil
}

// undoEdit restores the previous version of a file.
func (t *EditorTool) undoEdit(ctx context.Context, p EditorParams) (*CanonicalResult, error) {
	t.undoMu.Lock()
	snapshot, ok := t.undoMap[p.Path]
	if ok {
		delete(t.undoMap, p.Path)
	}
	t.undoMu.Unlock()

	if !ok {
		return nil, fmt.Errorf("no edit history for %s", p.Path)
	}

	path := PsQuote(p.Path)
	content := psBase64Expr(snapshot)

	cmd := fmt.Sprintf(
		`%s | Set-Content -Path %s -NoNewline -Encoding UTF8; `+
			`Write-Output "Undo applied successfully."`,
		content, path,
	)

	out, err := t.bash.Execute(ctx, cmd, editorTimeout)
	if err != nil {
		return nil, err
	}
	return &CanonicalResult{Output: out}, nil
}

// readFile reads the raw content of a file from the guest VM.
func (t *EditorTool) readFile(ctx context.Context, filePath string) (string, error) {
	cmd := fmt.Sprintf(`Get-Content -Path %s -Raw -Encoding UTF8`, PsQuote(filePath))
	return t.bash.Execute(ctx, cmd, editorTimeout)
}
