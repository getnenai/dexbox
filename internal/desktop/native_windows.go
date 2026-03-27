//go:build windows

package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"strings"
	"time"
	"unsafe"

	"github.com/kbinani/screenshot"
	"golang.org/x/sys/windows"
)

// Win32 constants for SendInput and mouse/keyboard events.
const (
	inputMouse    = 0
	inputKeyboard = 1

	// Mouse event flags
	mouseMove       = 0x0001
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	mouseAbsolute   = 0x8000

	// Keyboard event flags
	keyDown    = 0x0000
	keyUp      = 0x0002
	keyUnicode = 0x0004
)

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procSendInput   = user32.NewProc("SendInput")
	procSetCursorPos = user32.NewProc("SetCursorPos")
)

// mouseInput matches the Win32 MOUSEINPUT struct.
type mouseInput struct {
	dx        int32
	dy        int32
	mouseData int32
	dwFlags   uint32
	time      uint32
	dwExtra   uintptr
}

// keybdInput matches the Win32 KEYBDINPUT struct.
type keybdInput struct {
	wVk       uint16
	wScan     uint16
	dwFlags   uint32
	time      uint32
	dwExtra   uintptr
}

// inputUnion is sized to hold the largest input union member (MOUSEINPUT).
// On 64-bit Windows: MOUSEINPUT = 4+4+4+4+4+8 = 28 bytes, padded to 32.
// KEYBDINPUT = 2+2+4+4+8 = 20 bytes => fits within 32.
type inputUnion [4]uint64

// winInput matches the Win32 INPUT struct.
type winInput struct {
	inputType uint32
	_         [4]byte // padding on 64-bit
	union     inputUnion
}

// NativeDesktop implements the Desktop interface using Win32 APIs directly.
// It runs on the Windows machine itself — no VBox, SOAP, or guacd.
type NativeDesktop struct {
	name string
}

// NewNative creates a NativeDesktop.
func NewNative(name string) *NativeDesktop {
	return &NativeDesktop{name: name}
}

func (n *NativeDesktop) Connect(ctx context.Context) error { return nil }
func (n *NativeDesktop) Disconnect() error                 { return nil }
func (n *NativeDesktop) Connected() bool                   { return true }

func (n *NativeDesktop) Screenshot(ctx context.Context) ([]byte, error) {
	img, err := screenshot.CaptureDisplay(0)
	if err != nil {
		return nil, fmt.Errorf("native screenshot: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

func (n *NativeDesktop) MouseClick(x, y, buttonMask int) error {
	setCursorPos(x, y)
	down, up := mouseButtonFlags(buttonMask)
	sendMouseEvent(down, 0)
	time.Sleep(30 * time.Millisecond)
	sendMouseEvent(up, 0)
	return nil
}

func (n *NativeDesktop) MouseMoveAbsolute(x, y int) error {
	setCursorPos(x, y)
	return nil
}

func (n *NativeDesktop) MouseDoubleClick(x, y, buttonMask int) error {
	if err := n.MouseClick(x, y, buttonMask); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return n.MouseClick(x, y, buttonMask)
}

func (n *NativeDesktop) MouseScroll(x, y, dz int) error {
	setCursorPos(x, y)
	// dz is in notches; Win32 expects WHEEL_DELTA (120) per notch.
	delta := int32(dz * 120)
	sendMouseEvent(mouseWheel, delta)
	return nil
}

func (n *NativeDesktop) MouseDown(x, y, buttonMask int) error {
	setCursorPos(x, y)
	down, _ := mouseButtonFlags(buttonMask)
	sendMouseEvent(down, 0)
	return nil
}

func (n *NativeDesktop) MouseUp(x, y int) error {
	setCursorPos(x, y)
	sendMouseEvent(mouseLeftUp, 0)
	return nil
}

func (n *NativeDesktop) TypeText(ctx context.Context, text string) error {
	for _, r := range text {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := sendUnicodeChar(r); err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func (n *NativeDesktop) KeyPress(ctx context.Context, spec string) error {
	parts := strings.Split(spec, "+")
	codes := make([]uint16, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		vk, ok := resolveVK(part)
		if !ok {
			return fmt.Errorf("unknown key %q", part)
		}
		codes = append(codes, vk)
	}

	// Press all keys
	for _, vk := range codes {
		sendKeyEvent(vk, keyDown)
	}
	time.Sleep(30 * time.Millisecond)
	// Release in reverse order
	for i := len(codes) - 1; i >= 0; i-- {
		sendKeyEvent(codes[i], keyUp)
	}
	return nil
}

func (n *NativeDesktop) Name() string { return n.name }
func (n *NativeDesktop) Type() string { return "native" }

// --- Win32 wrappers ---

func setCursorPos(x, y int) {
	procSetCursorPos.Call(uintptr(x), uintptr(y))
}

func sendMouseEvent(flags uint32, data int32) {
	var input winInput
	input.inputType = inputMouse
	mi := mouseInput{
		dwFlags:   flags,
		mouseData: data,
	}
	*(*mouseInput)(unsafe.Pointer(&input.union)) = mi
	procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
}

func sendKeyEvent(vk uint16, flags uint32) {
	var input winInput
	input.inputType = inputKeyboard
	ki := keybdInput{
		wVk:     vk,
		dwFlags: flags,
	}
	*(*keybdInput)(unsafe.Pointer(&input.union)) = ki
	procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
}

func sendUnicodeChar(r rune) error {
	// Key down
	var down winInput
	down.inputType = inputKeyboard
	ki := keybdInput{
		wScan:   uint16(r),
		dwFlags: keyUnicode,
	}
	*(*keybdInput)(unsafe.Pointer(&down.union)) = ki

	// Key up
	var up winInput
	up.inputType = inputKeyboard
	kiUp := keybdInput{
		wScan:   uint16(r),
		dwFlags: keyUnicode | keyUp,
	}
	*(*keybdInput)(unsafe.Pointer(&up.union)) = kiUp

	ret, _, _ := procSendInput.Call(2, uintptr(unsafe.Pointer(&[2]winInput{down, up})), unsafe.Sizeof(down))
	if ret == 0 {
		return fmt.Errorf("SendInput failed for rune %q", r)
	}
	return nil
}

// mouseButtonFlags maps VBox button mask (1=left, 2=right, 4=middle) to
// Win32 mouse event flags.
func mouseButtonFlags(buttonMask int) (down, up uint32) {
	switch buttonMask {
	case 2:
		return mouseRightDown, mouseRightUp
	case 4:
		return mouseMiddleDown, mouseMiddleUp
	default:
		return mouseLeftDown, mouseLeftUp
	}
}

// --- Virtual key code mapping ---

// Windows Virtual-Key codes
const (
	vkBack      uint16 = 0x08
	vkTab       uint16 = 0x09
	vkReturn    uint16 = 0x0D
	vkShift     uint16 = 0x10
	vkControl   uint16 = 0x11
	vkAlt       uint16 = 0x12 // VK_MENU
	vkPause     uint16 = 0x13
	vkCapital   uint16 = 0x14
	vkEscape    uint16 = 0x1B
	vkSpace     uint16 = 0x20
	vkPageUp    uint16 = 0x21
	vkPageDown  uint16 = 0x22
	vkEnd       uint16 = 0x23
	vkHome      uint16 = 0x24
	vkLeft      uint16 = 0x25
	vkUp        uint16 = 0x26
	vkRight     uint16 = 0x27
	vkDown      uint16 = 0x28
	vkPrint     uint16 = 0x2C
	vkInsert    uint16 = 0x2D
	vkDelete    uint16 = 0x2E
	vkLWin      uint16 = 0x5B
	vkRWin      uint16 = 0x5C
	vkApps      uint16 = 0x5D
	vkNumLock   uint16 = 0x90
	vkScrollLk  uint16 = 0x91
	vkLShift    uint16 = 0xA0
	vkRShift    uint16 = 0xA1
	vkLControl  uint16 = 0xA2
	vkRControl  uint16 = 0xA3
	vkLAlt      uint16 = 0xA4
	vkRAlt      uint16 = 0xA5
	vkF1        uint16 = 0x70
	vkF2        uint16 = 0x71
	vkF3        uint16 = 0x72
	vkF4        uint16 = 0x73
	vkF5        uint16 = 0x74
	vkF6        uint16 = 0x75
	vkF7        uint16 = 0x76
	vkF8        uint16 = 0x77
	vkF9        uint16 = 0x78
	vkF10       uint16 = 0x79
	vkF11       uint16 = 0x7A
	vkF12       uint16 = 0x7B
)

// resolveVK maps key names (matching the aliases from rdp.go) to Windows VK codes.
func resolveVK(name string) (uint16, bool) {
	lower := strings.ToLower(name)

	// Apply aliases first (same as rdp.go keyAliases)
	if canonical, ok := vkAliases[lower]; ok {
		lower = canonical
	}

	if code, ok := vkMap[lower]; ok {
		return code, true
	}

	// Single ASCII character → uppercase VK code
	if len(name) == 1 {
		ch := name[0]
		if ch >= 'a' && ch <= 'z' {
			return uint16(ch - 32), true // VK_A..VK_Z = 0x41..0x5A
		}
		if ch >= 'A' && ch <= 'Z' {
			return uint16(ch), true
		}
		if ch >= '0' && ch <= '9' {
			return uint16(ch), true // VK_0..VK_9 = 0x30..0x39
		}
	}

	return 0, false
}

var vkAliases = map[string]string{
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

var vkMap = map[string]uint16{
	"return":      vkReturn,
	"escape":      vkEscape,
	"backspace":   vkBack,
	"tab":         vkTab,
	"space":       vkSpace,
	"delete":      vkDelete,
	"insert":      vkInsert,
	"home":        vkHome,
	"end":         vkEnd,
	"pageup":      vkPageUp,
	"pagedown":    vkPageDown,
	"up":          vkUp,
	"down":        vkDown,
	"left":        vkLeft,
	"right":       vkRight,
	"shift":       vkShift,
	"shift_r":     vkRShift,
	"ctrl":        vkControl,
	"ctrl_r":      vkRControl,
	"alt":         vkAlt,
	"alt_r":       vkRAlt,
	"super":       vkLWin,
	"super_r":     vkRWin,
	"capslock":    vkCapital,
	"numlock":     vkNumLock,
	"scrolllock":  vkScrollLk,
	"f1":          vkF1,
	"f2":          vkF2,
	"f3":          vkF3,
	"f4":          vkF4,
	"f5":          vkF5,
	"f6":          vkF6,
	"f7":          vkF7,
	"f8":          vkF8,
	"f9":          vkF9,
	"f10":         vkF10,
	"f11":         vkF11,
	"f12":         vkF12,
	"menu":        vkApps,
	"pause":       vkPause,
	"printscreen": vkPrint,
}
