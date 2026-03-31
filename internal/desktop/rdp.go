package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync"
	"time"

	"github.com/deluan/bring"
)

// Type aliases so the Client interface doesn't leak bring's concrete types.
// Using aliases (not new types) means bring values satisfy them directly.
type (
	SessionState = bring.SessionState
	MouseButton  = bring.MouseButton
	KeyCode      = bring.KeyCode
)

// Client is the subset of *bring.Client that BringRDP depends on.
// Defined here to document the contract and enable passing a test double
// as a function argument where needed.
type Client interface {
	Start()
	Stop()
	State() SessionState
	// ConnectionID returns the guacd connection ID assigned during handshake.
	// Empty until the session reaches SessionActive.
	ConnectionID() string
	Screen() (image.Image, int64)
	SendMouse(p image.Point, pressedButtons ...MouseButton) error
	SendText(sequence string) error
	SendKey(key KeyCode, pressed bool) error
}

// BringRDP implements Desktop using deluan/bring to talk to a guacd daemon
// which proxies the actual RDP connection.
type BringRDP struct {
	name      string
	config    RDPConfig
	guacdAddr string

	client *bring.Client
	connID string // guacd connection ID from handshake; set after SessionActive
	state  bring.SessionState
	mu     sync.Mutex
	done   chan struct{} // closed when client.Start() returns
}

// NewBringRDP creates an RDP desktop. Call Connect to establish the session.
func NewBringRDP(name string, cfg RDPConfig, guacdAddr string) *BringRDP {
	return &BringRDP{
		name:      name,
		config:    cfg,
		guacdAddr: guacdAddr,
	}
}

func (r *BringRDP) Connect(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client != nil {
		return nil
	}

	security := r.config.Security
	if security == "" {
		security = "any"
	}
	guacConfig := map[string]string{
		"hostname":         r.config.Host,
		"port":             fmt.Sprintf("%d", r.config.Port),
		"username":         r.config.Username,
		"password":         r.config.Password,
		"width":            fmt.Sprintf("%d", r.config.Width),
		"height":           fmt.Sprintf("%d", r.config.Height),
		"security":         security,
		"disable-audio":    "true",
		"enable-wallpaper": "false",
	}
	if r.config.IgnoreCert {
		guacConfig["ignore-cert"] = "true"
	}

	client, err := bring.NewClient(r.guacdAddr, "rdp", guacConfig, &bring.DefaultLogger{Quiet: true})
	if err != nil {
		return fmt.Errorf("guacd connect: %w", err)
	}

	r.client = client
	r.done = make(chan struct{})

	// Start the client event loop in a background goroutine
	go func() {
		defer close(r.done)
		client.Start()
	}()

	// Wait for the session to become active
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for RDP session to become active")
		case <-r.done:
			return fmt.Errorf("RDP session closed during connection")
		case <-ticker.C:
			if client.State() == bring.SessionActive {
				r.connID = client.ConnectionID()
				// Give the display a moment to receive the initial frame
				time.Sleep(500 * time.Millisecond)
				return nil
			}
		}
	}
}

func (r *BringRDP) Disconnect() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return nil
	}

	// The bring client doesn't expose a Close/Disconnect method.
	// Setting client to nil and letting the GC clean up is the best we can do.
	// The underlying TCP connection will be closed when the client is GC'd.
	r.client = nil
	return nil
}

func (r *BringRDP) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client != nil && r.client.State() == bring.SessionActive
}

func (r *BringRDP) Screenshot(ctx context.Context) ([]byte, error) {
	c := r.getClient()
	if c == nil {
		return nil, fmt.Errorf("RDP session not connected")
	}
	return screenshot(ctx, c)
}

// screenshot captures a PNG screenshot from a connected Guacamole client.
// It waits up to 10 s for the first non-empty frame before returning an error.
func screenshot(ctx context.Context, c Client) ([]byte, error) {
	var img image.Image
	deadline := time.Now().Add(10 * time.Second)
	for {
		img, _ = c.Screen()
		if img != nil && img.Bounds().Dx() > 0 && img.Bounds().Dy() > 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for first RDP frame")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode screenshot: %w", err)
	}
	return buf.Bytes(), nil
}

// Mouse input — bring uses different button constants than VBox:
//
//	VBox:  1=left, 2=right, 4=middle
//	bring: MouseLeft=1, MouseMiddle=2, MouseRight=4
//
// The Desktop interface uses VBox convention, so we translate here.
func (r *BringRDP) MouseClick(x, y, buttonMask int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return mouseClick(c, x, y, buttonMask)
}

func mouseClick(c Client, x, y, buttonMask int) error {
	p := image.Pt(x, y)
	btn := translateButton(buttonMask)
	if err := c.SendMouse(p, btn); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return c.SendMouse(p)
}

func (r *BringRDP) MouseMoveAbsolute(x, y int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return c.SendMouse(image.Pt(x, y))
}

func (r *BringRDP) MouseDoubleClick(x, y, buttonMask int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	if err := mouseClick(c, x, y, buttonMask); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return mouseClick(c, x, y, buttonMask)
}

func (r *BringRDP) MouseScroll(x, y, dz int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return mouseScroll(c, x, y, dz)
}

// mouseScroll sends scroll events. Positive dz scrolls up, negative scrolls down.
func mouseScroll(c Client, x, y, dz int) error {
	p := image.Pt(x, y)
	if err := c.SendMouse(p); err != nil {
		return err
	}
	var btn MouseButton
	if dz > 0 {
		btn = bring.MouseUp
	} else {
		btn = bring.MouseDown
		dz = -dz
	}
	for i := 0; i < dz; i++ {
		if err := c.SendMouse(p, btn); err != nil {
			return err
		}
		if err := c.SendMouse(p); err != nil {
			return err
		}
	}
	return nil
}

func (r *BringRDP) MouseDown(x, y, buttonMask int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return c.SendMouse(image.Pt(x, y), translateButton(buttonMask))
}

func (r *BringRDP) MouseUp(x, y int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return c.SendMouse(image.Pt(x, y)) // no buttons = release
}

func (r *BringRDP) TypeText(ctx context.Context, text string) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return c.SendText(text)
}

func (r *BringRDP) KeyPress(ctx context.Context, spec string) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return keyPress(c, spec)
}

// keyPress parses a key spec (e.g. "ctrl+c", "shift+F4") and sends the
// press/release sequence to the client.
func keyPress(c Client, spec string) error {
	parts := strings.Split(spec, "+")
	keys := make([]KeyCode, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key, ok := resolveKeyCode(part)
		if !ok {
			return fmt.Errorf("unknown key %q", part)
		}
		keys = append(keys, key)
	}
	for _, key := range keys {
		if err := c.SendKey(key, true); err != nil {
			return err
		}
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if err := c.SendKey(keys[i], false); err != nil {
			return err
		}
	}
	return nil
}

func (r *BringRDP) Name() string { return r.name }
func (r *BringRDP) Type() string { return "rdp" }

// GuacdConnectionID returns the guacd connection ID assigned during handshake.
// Returns an empty string if the session is not yet active.
func (r *BringRDP) GuacdConnectionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connID
}

func (r *BringRDP) getClient() *bring.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// translateButton converts VBox button mask to bring MouseButton.
// VBox: 1=left, 2=right, 4=middle
// bring: MouseLeft=1, MouseMiddle=2, MouseRight=4
func translateButton(vboxMask int) bring.MouseButton {
	switch vboxMask {
	case 1:
		return bring.MouseLeft
	case 2:
		return bring.MouseRight
	case 4:
		return bring.MouseMiddle
	default:
		return bring.MouseLeft
	}
}

// resolveKeyCode maps a key name (as used by model adapters and the existing
// VBox scancode tables) to a bring.KeyCode.
func resolveKeyCode(name string) (bring.KeyCode, bool) {
	lower := strings.ToLower(name)

	// Check alias map first
	if canonical, ok := keyAliases[lower]; ok {
		lower = canonical
	}

	// Check special key map
	if code, ok := keyMap[lower]; ok {
		return code, true
	}

	// Single ASCII character
	if len(name) == 1 {
		ch := rune(name[0])
		if ch >= 32 && ch < 127 {
			return bring.KeyCode(ch), true
		}
	}

	return 0, false
}

// keyAliases maps alternate key names to canonical names used in keyMap.
// Mirrors the aliases in vbox/scancodes.go for consistency.
var keyAliases = map[string]string{
	"enter":      "return",
	"esc":        "escape",
	"lshift":     "shift",
	"rshift":     "shift_r",
	"lctrl":      "ctrl",
	"rctrl":      "ctrl_r",
	"lalt":       "alt",
	"ralt":       "alt_r",
	"lsuper":     "super",
	"rsuper":     "super_r",
	"win":        "super",
	"windows":    "super",
	"meta":       "super",
	"cmd":        "super",
	"del":        "delete",
	"ins":        "insert",
	"pgup":       "pageup",
	"pgdn":       "pagedown",
	"page_up":    "pageup",
	"page_down":  "pagedown",
	"arrowup":    "up",
	"arrowdown":  "down",
	"arrowleft":  "left",
	"arrowright": "right",
	"bksp":       "backspace",
}

// keyMap maps canonical key names to bring.KeyCode constants.
var keyMap = map[string]bring.KeyCode{
	"return":      bring.KeyEnter,
	"escape":      bring.KeyEscape,
	"backspace":   bring.KeyBackspace,
	"tab":         bring.KeyTab,
	"space":       bring.KeyCode(' '),
	"delete":      bring.KeyDelete,
	"insert":      bring.KeyInsert,
	"home":        bring.KeyHome,
	"end":         bring.KeyEnd,
	"pageup":      bring.KeyPageUp,
	"pagedown":    bring.KeyPageDown,
	"up":          bring.KeyArrowUp,
	"down":        bring.KeyArrowDown,
	"left":        bring.KeyArrowLeft,
	"right":       bring.KeyArrowRight,
	"shift":       bring.KeyLeftShift,
	"shift_r":     bring.KeyRightShift,
	"ctrl":        bring.KeyLeftControl,
	"ctrl_r":      bring.KeyRightControl,
	"alt":         bring.KeyLeftAlt,
	"alt_r":       bring.KeyRightAlt,
	"super":       bring.KeySuper,
	"super_r":     bring.KeySuper,
	"capslock":    bring.KeyCapsLock,
	"numlock":     bring.KeyNumLock,
	"scrolllock":  bring.KeyScroll,
	"f1":          bring.KeyF1,
	"f2":          bring.KeyF2,
	"f3":          bring.KeyF3,
	"f4":          bring.KeyF4,
	"f5":          bring.KeyF5,
	"f6":          bring.KeyF6,
	"f7":          bring.KeyF7,
	"f8":          bring.KeyF8,
	"f9":          bring.KeyF9,
	"f10":         bring.KeyF10,
	"f11":         bring.KeyF11,
	"f12":         bring.KeyF12,
	"menu":        bring.KeyContextMenu,
	"pause":       bring.KeyPause,
	"printscreen": bring.KeyPrintScreen,
}
