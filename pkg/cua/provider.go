package cua

import "context"

// Provider defines the interface for interacting with different CUA models.
type Provider interface {
	// Setup initializes any necessary provider-specific configuration
	// (e.g., setting Anthropic display_width_px or compiling Gemini schemas).
	Setup(config DisplayConfig) error

	// PredictAction takes the current conversation history and the latest
	// visual state, returning the next predicted unified Action and the assistant's thought text.
	PredictAction(ctx context.Context, history []Message, state VisualState) (*Action, string, error)

	// NeedsVisuals returns true if the provider requires a fresh visual state
	// (screenshot) for the next PredictAction call.
	NeedsVisuals() bool

	// FormatHistory handles translating the internal interaction history
	// into the specific message schemas required by the provider
	// (e.g., converting Actions into Anthropic tool_results or Gemini function_calls).
	FormatHistory(history []Message) (any, error)
}

// Message is a generic container for interaction history.
type Message struct {
	Role       string // "user", "assistant", "system"
	Content    string
	Action     *Action // Populated if the message involved a UI action
	Result     string  // Populated if the message is a tool result
	ToolCallID string  // Corresponds to the ID of the tool call this message relates to
}
