package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// CanonicalAction is the internal representation of any tool call.
type CanonicalAction struct {
	Tool   string         `json:"tool"`   // "computer", "bash"
	Action string         `json:"action"` // "left_click", "screenshot", "view", etc.
	Params map[string]any `json:"params"` // coordinate, text, command, path, etc.
}

// CanonicalResult is the internal representation of a tool call result.
type CanonicalResult struct {
	Output     string `json:"output,omitempty"`     // text output (bash)
	Image      []byte `json:"-"`                    // screenshot PNG bytes
	Coordinate [2]int `json:"coordinate,omitempty"` // cursor position
}

// UnmarshalParams converts the generic Params map into a typed struct.
func (a *CanonicalAction) UnmarshalParams(dest any) error {
	data, err := json.Marshal(a.Params)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

// DisplayConfig describes the VM display dimensions.
type DisplayConfig struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ModelAdapter translates between a model's native tool format and canonical types.
type ModelAdapter interface {
	// ToolDefinitions returns tool definitions in the model's native format.
	ToolDefinitions(capabilities []string, display DisplayConfig) []json.RawMessage
	// ParseToolCall extracts a canonical action from the model's native format.
	ParseToolCall(raw json.RawMessage) (*CanonicalAction, error)
	// FormatResult converts a canonical result to the model's native format.
	FormatResult(action *CanonicalAction, result *CanonicalResult) (json.RawMessage, error)
}

var (
	adaptersMu sync.RWMutex
	adapters   = map[string]ModelAdapter{} // prefix → adapter
)

// RegisterAdapter registers an adapter for a model ID prefix.
func RegisterAdapter(prefix string, a ModelAdapter) {
	adaptersMu.Lock()
	defer adaptersMu.Unlock()
	adapters[prefix] = a
}

// AdapterForModel returns the adapter matching the model ID by prefix.
func AdapterForModel(modelID string) (ModelAdapter, error) {
	adaptersMu.RLock()
	defer adaptersMu.RUnlock()

	for prefix, adapter := range adapters {
		if strings.HasPrefix(modelID, prefix) {
			return adapter, nil
		}
	}

	prefixes := make([]string, 0, len(adapters))
	for p := range adapters {
		prefixes = append(prefixes, p+"-*")
	}
	return nil, fmt.Errorf("unknown model %q, supported prefixes: %v", modelID, prefixes)
}

// SupportedPrefixes returns the list of registered model prefixes.
func SupportedPrefixes() []string {
	adaptersMu.RLock()
	defer adaptersMu.RUnlock()

	prefixes := make([]string, 0, len(adapters))
	for p := range adapters {
		prefixes = append(prefixes, p)
	}
	return prefixes
}
