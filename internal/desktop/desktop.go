// Package desktop defines the Desktop interface for abstracting over
// different remote desktop backends (VirtualBox, RDP, etc.).
package desktop

import "context"

// Desktop is the core abstraction over VBox and RDP backends.
// It provides screen capture, mouse input, and keyboard input.
type Desktop interface {
	// Connect establishes the session (SOAP for VBox, guacd for RDP).
	Connect(ctx context.Context) error
	// Disconnect tears down the session.
	Disconnect() error
	// Connected reports whether the session is active.
	Connected() bool

	// Screenshot captures the current screen as raw PNG bytes.
	Screenshot(ctx context.Context) ([]byte, error)

	// Mouse input
	MouseClick(x, y, buttonMask int) error
	MouseMoveAbsolute(x, y int) error
	MouseDoubleClick(x, y, buttonMask int) error
	MouseScroll(x, y, dz int) error
	MouseDown(x, y, buttonMask int) error
	MouseUp(x, y int) error

	// TypeText types a string of characters (handles shift for uppercase/symbols).
	TypeText(ctx context.Context, text string) error
	// KeyPress sends a key combination like "ctrl+a", "Return", or "shift+F5".
	KeyPress(ctx context.Context, spec string) error

	// Name returns the desktop's unique name.
	Name() string
	// Type returns "vm" or "rdp".
	Type() string
}
