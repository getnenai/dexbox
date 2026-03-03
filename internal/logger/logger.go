package logger

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Styling
// ---------------------------------------------------------------------------

var (
	// Base styles
	styleTime     = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // faint gray
	styleCompBase = lipgloss.NewStyle().Width(20).Align(lipgloss.Right)
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)                                  // red
	styleWarning  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)                                  // yellow
	styleInfo     = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)                                   // blue
	styleDebug    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Bold(true)                                  // gray
	styleFatal    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("196")).Bold(true) // white on red
	styleMetaKey  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleMetaVal  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleFallback = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // faint

	// Component colors (mapped dynamically)
	compColors = map[string]lipgloss.Color{
		"sandbox": lipgloss.Color("86"),  // cyan
		"python":  lipgloss.Color("118"), // green
		"server":  lipgloss.Color("75"),  // blue
		// fallback will use 246 (light gray)
	}
)

func RenderLogLine(raw string) string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		// Fallback for unstructured text (like stack traces)
		return styleFallback.Render(raw)
	}

	// Extract core fields
	lvl := ""
	if str, ok := data["level"].(string); ok {
		lvl = strings.ToUpper(str)
	}
	name := ""
	if str, ok := data["name"].(string); ok && str != "" {
		name = str
	} else if str, ok := data["logger"].(string); ok && str != "" {
		name = str
	}
	msg := ""
	if str, ok := data["message"].(string); ok {
		msg = str
	}
	tStr := ""
	if str, ok := data["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
			tStr = t.Format("15:04:05.000")
		} else {
			tStr = str
			if len(tStr) >= 23 && tStr[10] == 'T' {
				tStr = tStr[11:23] // naive fallback
			}
		}
	} else {
		tStr = time.Now().Format("15:04:05.000") // fallback if no time
	}

	// Apply workflow filters (skip spammy internal components)
	if strings.HasPrefix(name, "dexbox.recorder") || strings.HasPrefix(name, "dexbox.session") {
		return "" // Silently drop these
	}

	// Remove "dexbox." prefix for cleaner display
	shortName := strings.TrimPrefix(name, "dexbox.")

	// Determine component color
	compColor, exists := compColors[shortName]
	if !exists {
		// Handle sub-components like "python:browser"
		baseName := strings.Split(shortName, ":")[0]
		if c, ok := compColors[baseName]; ok {
			compColor = c
		} else {
			compColor = lipgloss.Color("246") // generic gray
		}
	}

	// Render parts
	ts := styleTime.Render(tStr)
	comp := styleCompBase.Copy().Foreground(compColor).Render("[" + shortName + "]")

	// Message and badge logic
	msgPart := msg
	switch lvl {
	case "ERROR":
		msgPart = styleError.Render("ERROR") + " " + msg
	case "WARN", "WARNING":
		msgPart = styleWarning.Render("WARN") + " " + msg
	case "INFO":
		msgPart = styleInfo.Render("INFO") + " " + msg
	case "DEBUG":
		msgPart = styleDebug.Render("DEBUG") + " " + msg
	case "FATAL", "CRITICAL", "PANIC":
		msgPart = styleFatal.Render(" "+lvl+" ") + " " + msg
	}

	// Build metadata pairs
	delete(data, "level")
	delete(data, "name")
	delete(data, "message")
	delete(data, "time")
	delete(data, "timestamp")

	// Fields that should NOT be truncated and instead rendered as multi-line blocks
	blobFields := map[string]bool{
		"exception":  true,
		"traceback":  true,
		"stack_info": true,
	}

	var metaParts []string
	var blobs []string

	// Sort keys to ensure consistent order
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}

	for _, k := range keys {
		v := data[k]
		if v == nil {
			continue
		}

		if blobFields[k] {
			// Save for multi-line rendering at the end
			blobs = append(blobs, fmt.Sprintf("%v", v))
			continue
		}

		// Regular metadata field (subject to truncation)
		valStr := fmt.Sprintf("%v", v)
		if len(valStr) > 64 {
			valStr = valStr[:61] + "..."
		}

		if !strings.Contains(valStr, " ") {
			metaParts = append(metaParts, fmt.Sprintf("%s=%v", styleMetaKey.Render(k), styleMetaVal.Render(valStr)))
		} else {
			metaParts = append(metaParts, fmt.Sprintf("%s=%q", styleMetaKey.Render(k), styleMetaVal.Render(valStr)))
		}
	}

	metaStr := ""
	if len(metaParts) > 0 {
		metaStr = " " + strings.Join(metaParts, " ")
	}

	result := fmt.Sprintf("%s  %s  %s%s", ts, comp, msgPart, metaStr)

	// Append blobs as multi-line blocks
	if len(blobs) > 0 {
		for _, blob := range blobs {
			// Handle literal "\n" strings (often found in JSON) and raw newlines
			content := strings.ReplaceAll(blob, "\\n", "\n")
			content = strings.TrimSpace(content)

			// Indent multi-line blobs for readability
			indented := "    " + strings.ReplaceAll(content, "\n", "\n    ")
			result += "\n" + styleFallback.Render(indented)
		}
	}

	return result
}
