package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"time"

	"github.com/getnenai/dexbox/internal/vbox"
	"golang.org/x/image/draw"
)

// ComputerTool executes computer-use actions (screenshot, keyboard, mouse)
// against a VirtualBox VM.
type ComputerTool struct {
	vmName  string
	width   int
	height  int
	soap    *vbox.SOAPClient
	cursorX int
	cursorY int
}

// NewComputerTool creates a computer tool bound to a specific VM.
func NewComputerTool(vmName string, width, height int, soap *vbox.SOAPClient) *ComputerTool {
	return &ComputerTool{
		vmName: vmName,
		width:  width,
		height: height,
		soap:   soap,
	}
}

// Execute dispatches a canonical computer action.
func (t *ComputerTool) Execute(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	switch action.Action {
	case "screenshot":
		return t.screenshot(ctx)
	case "left_click":
		return t.click(ctx, action, 1)
	case "right_click":
		return t.click(ctx, action, 2)
	case "middle_click":
		return t.click(ctx, action, 4)
	case "double_click":
		return t.doubleClick(ctx, action)
	case "type":
		return t.typeText(ctx, action)
	case "key":
		return t.key(ctx, action)
	case "mouse_move":
		return t.mouseMove(ctx, action)
	case "scroll":
		return t.scroll(ctx, action)
	case "left_click_drag":
		return t.drag(ctx, action)
	case "cursor_position":
		return &CanonicalResult{Coordinate: [2]int{t.cursorX, t.cursorY}}, nil
	default:
		return nil, fmt.Errorf("unknown computer action %q", action.Action)
	}
}

func (t *ComputerTool) screenshot(ctx context.Context) (*CanonicalResult, error) {
	raw, err := vbox.Screenshot(ctx, t.vmName)
	if err != nil {
		return nil, err
	}

	resized, err := resizePNG(raw, t.width, t.height)
	if err != nil {
		// If resize fails, return original
		resized = raw
	}

	return &CanonicalResult{Image: resized}, nil
}

func (t *ComputerTool) click(ctx context.Context, action *CanonicalAction, buttonMask int) (*CanonicalResult, error) {
	x, y, err := extractCoordinate(action)
	if err != nil {
		return nil, err
	}
	t.cursorX, t.cursorY = x, y

	if err := t.soap.MouseClick(x, y, buttonMask); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) doubleClick(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	x, y, err := extractCoordinate(action)
	if err != nil {
		return nil, err
	}
	t.cursorX, t.cursorY = x, y

	if err := t.soap.MouseDoubleClick(x, y, 1); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) typeText(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	text, _ := action.Params["text"].(string)
	if text == "" {
		return nil, fmt.Errorf("field 'text' required for action 'type'")
	}

	codes := vbox.TextToScancodes(text)
	// Send in batches to avoid oversized VBoxManage argument lists.
	const batchSize = 60
	for i := 0; i < len(codes); i += batchSize {
		end := i + batchSize
		if end > len(codes) {
			end = len(codes)
		}
		if err := vbox.SendScancodes(ctx, t.vmName, codes[i:end]); err != nil {
			return nil, err
		}
		if end < len(codes) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) key(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	text, _ := action.Params["text"].(string)
	if text == "" {
		return nil, fmt.Errorf("field 'text' required for action 'key'")
	}

	codes := vbox.KeyToScancodes(text)
	if err := vbox.SendScancodes(ctx, t.vmName, codes); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) mouseMove(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	x, y, err := extractCoordinate(action)
	if err != nil {
		return nil, err
	}
	t.cursorX, t.cursorY = x, y

	if err := t.soap.MouseMoveAbsolute(x, y); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) scroll(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	x, y, err := extractCoordinate(action)
	if err != nil {
		return nil, err
	}
	t.cursorX, t.cursorY = x, y

	// Default scroll amount
	dz := 3
	if v, ok := action.Params["amount"].(float64); ok {
		dz = int(v)
	}

	// Direction: negative dz = scroll down in VBox convention
	dir, _ := action.Params["direction"].(string)
	switch dir {
	case "down", "":
		dz = -dz
	case "up":
		// dz stays positive
	default:
		return nil, fmt.Errorf("invalid scroll direction %q; expected 'up' or 'down'", dir)
	}

	if err := t.soap.MouseScroll(x, y, dz); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) drag(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	startCoord, ok := action.Params["start_coordinate"].([]any)
	if !ok || len(startCoord) < 2 {
		return nil, fmt.Errorf("field 'start_coordinate' required for action 'left_click_drag'")
	}
	endCoord, ok := action.Params["coordinate"].([]any)
	if !ok || len(endCoord) < 2 {
		return nil, fmt.Errorf("field 'coordinate' required for action 'left_click_drag'")
	}

	sx := toInt(startCoord[0])
	sy := toInt(startCoord[1])
	ex := toInt(endCoord[0])
	ey := toInt(endCoord[1])

	// Press at start
	if err := t.soap.MouseDown(sx, sy, 1); err != nil {
		return nil, err
	}

	var success bool
	defer func() {
		if !success {
			_ = t.soap.MouseUp(sx, sy)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	// Move to end
	if err := t.soap.MouseMoveAbsolute(ex, ey); err != nil {
		return nil, err
	}
	time.Sleep(50 * time.Millisecond)

	// Release
	if err := t.soap.MouseUp(ex, ey); err != nil {
		return nil, err
	}
	success = true

	t.cursorX, t.cursorY = ex, ey
	return &CanonicalResult{}, nil
}

// --- Helpers ---

func extractCoordinate(action *CanonicalAction) (int, int, error) {
	coord, ok := action.Params["coordinate"].([]any)
	if !ok || len(coord) < 2 {
		return 0, 0, fmt.Errorf("field 'coordinate' required for action %q", action.Action)
	}
	return toInt(coord[0]), toInt(coord[1]), nil
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// resizePNG decodes a PNG, resizes it to target dimensions, and re-encodes.
func resizePNG(data []byte, width, height int) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	srcBounds := src.Bounds()
	if srcBounds.Dx() == width && srcBounds.Dy() == height {
		return data, nil // Already the right size
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, srcBounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ImageToBase64 encodes raw PNG bytes as a base64 string.
func ImageToBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
