package tools

import (
	"encoding/json"
	"fmt"
)

func init() {
	RegisterAdapter("claude-", &AnthropicAdapter{})
}

// AnthropicAdapter handles Claude model tool formats.
type AnthropicAdapter struct{}

func (a *AnthropicAdapter) ToolDefinitions(capabilities []string, display DisplayConfig) []json.RawMessage {
	capSet := toSet(capabilities)
	var defs []json.RawMessage

	if capSet["computer"] {
		def, _ := json.Marshal(map[string]any{
			"type":              "computer_20250124",
			"name":              "computer",
			"display_width_px":  display.Width,
			"display_height_px": display.Height,
			"display_number":    1,
		})
		defs = append(defs, def)
	}

	if capSet["bash"] {
		def, _ := json.Marshal(map[string]any{
			"type": "bash_20250124",
			"name": "bash",
		})
		defs = append(defs, def)
	}



	return defs
}

func (a *AnthropicAdapter) ParseToolCall(raw json.RawMessage) (*CanonicalAction, error) {
	var call map[string]any
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	toolType, _ := call["type"].(string)

	switch toolType {
	case "computer_20250124":
		return a.parseComputer(call)
	case "bash_20250124":
		return a.parseBash(call)

	default:
		return nil, fmt.Errorf("unknown tool type %q", toolType)
	}
}

func (a *AnthropicAdapter) parseComputer(call map[string]any) (*CanonicalAction, error) {
	action, _ := call["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("field 'action' required for computer tool")
	}

	params := map[string]any{
		"action": action, // Must be in Params for ComputerTool.Execute to read it
	}
	if coord, ok := call["coordinate"].([]any); ok {
		params["coordinate"] = coord
	}
	if text, ok := call["text"].(string); ok {
		params["text"] = text
	}
	if startCoord, ok := call["start_coordinate"].([]any); ok {
		params["start_coordinate"] = startCoord
	}
	if dir, ok := call["direction"].(string); ok {
		params["direction"] = dir
	}
	if amount, ok := call["amount"].(float64); ok {
		params["amount"] = amount
	}

	return &CanonicalAction{
		Tool:   "computer",
		Action: action,
		Params: params,
	}, nil
}

func (a *AnthropicAdapter) parseBash(call map[string]any) (*CanonicalAction, error) {
	command, _ := call["command"].(string)
	return &CanonicalAction{
		Tool:   "bash",
		Action: "run",
		Params: map[string]any{"command": command},
	}, nil
}



func (a *AnthropicAdapter) FormatResult(action *CanonicalAction, result *CanonicalResult) (json.RawMessage, error) {
	resp := map[string]any{}

	switch action.Tool {
	case "computer":
		resp["type"] = "computer_20250124"
		if result.Image != nil {
			resp["base64_image"] = ImageToBase64(result.Image)
		}
		if result.Coordinate != [2]int{} && action.Action == "cursor_position" {
			resp["coordinate"] = result.Coordinate
		}
	case "bash":
		resp["type"] = "bash_20250124"
		resp["output"] = result.Output

	}

	return json.Marshal(resp)
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}
