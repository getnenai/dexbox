package cua

// ActionType defines the normalized set of supported computer actions.
type ActionType string

const (
	ActionClick          ActionType = "click"
	ActionRightClick     ActionType = "right_click"
	ActionDoubleClick    ActionType = "double_click"
	ActionTripleClick    ActionType = "triple_click"
	ActionMiddleClick    ActionType = "middle_click"
	ActionDrag           ActionType = "drag"
	ActionMouseMove      ActionType = "mouse_move"
	ActionTypeString     ActionType = "type"
	ActionKey            ActionType = "key"
	ActionScroll         ActionType = "scroll"
	ActionScreenshot     ActionType = "screenshot"
	ActionWait           ActionType = "wait"
	ActionHoldKey        ActionType = "hold_key"
	ActionZoom           ActionType = "zoom"
	ActionCursorPosition ActionType = "cursor_position"
	ActionLeftMouseDown  ActionType = "left_mouse_down"
	ActionLeftMouseUp    ActionType = "left_mouse_up"
	ActionLeftClickDrag  ActionType = "left_click_drag"
)

type Action struct {
	Type          ActionType `json:"action"`
	ToolCallID    string     `json:"tool_call_id,omitempty"`
	X             *int       `json:"absolute_x,omitempty"`
	Y             *int       `json:"absolute_y,omitempty"`
	HasCoordinate bool       `json:"-"`
	Text          string     `json:"text,omitempty"`
	ScrollDeltaX  int        `json:"scroll_delta_x,omitempty"`
	ScrollDeltaY  int        `json:"scroll_delta_y,omitempty"`
	Duration      float64    `json:"duration,omitempty"`
	ZoomRegion    []int      `json:"zoom_region,omitempty"`
}

// DisplayConfig standardizes the screen boundary for all models.
type DisplayConfig struct {
	WidthPX  int
	HeightPX int
}

// VisualState represents the current screen capture to be fed to the models.
type VisualState struct {
	// Base64 encoded screenshot data
	ImageBase64 string
	// Format (e.g., "png", "jpeg")
	Format string
}
