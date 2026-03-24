package tools

import (
	"encoding/json"
	"fmt"
)

func init() {
	RegisterAdapter("gemini-", &GeminiAdapter{})
}

// GeminiAdapter handles Gemini model tool formats.
type GeminiAdapter struct{}

func (a *GeminiAdapter) ToolDefinitions(capabilities []string, display DisplayConfig) []json.RawMessage {
	capSet := toSet(capabilities)
	var defs []json.RawMessage

	if capSet["computer"] {
		def, _ := json.Marshal(map[string]any{
			"type":           "computer_use",
			"display_width":  display.Width,
			"display_height": display.Height,
			"environment":    "windows",
		})
		defs = append(defs, def)
	}

	return defs
}

func (a *GeminiAdapter) ParseToolCall(raw json.RawMessage) (*CanonicalAction, error) {
	var call map[string]any
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	toolType, _ := call["type"].(string)

	switch toolType {
	case "computer_use":
		return a.parseComputer(call)
	default:
		return nil, fmt.Errorf("unknown tool type %q", toolType)
	}
}

func (a *GeminiAdapter) parseComputer(call map[string]any) (*CanonicalAction, error) {
	action, _ := call["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("field 'action' required for computer tool")
	}

	params := map[string]any{
		"action": action,
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

func (a *GeminiAdapter) FormatResult(action *CanonicalAction, result *CanonicalResult) (json.RawMessage, error) {
	resp := map[string]any{
		"type": "computer_use",
	}

	if result.Image != nil {
		resp["base64_image"] = ImageToBase64(result.Image)
	}
	if result.Output != "" {
		resp["output"] = result.Output
	}

	return json.Marshal(resp)
}
