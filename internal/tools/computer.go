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
	var p ComputerParams
	if err := action.UnmarshalParams(&p); err != nil {
		return nil, fmt.Errorf("invalid computer params: %w", err)
	}

	switch p.Action {
	case "screenshot":
		return t.screenshot(ctx)
	case "left_click":
		return t.click(ctx, p.Coordinate, 1)
	case "right_click":
		return t.click(ctx, p.Coordinate, 2)
	case "middle_click":
		return t.click(ctx, p.Coordinate, 4)
	case "double_click":
		return t.doubleClick(ctx, p.Coordinate)
	case "type":
		return t.typeText(ctx, p.Text)
	case "key":
		return t.key(ctx, p.Text)
	case "mouse_move":
		return t.mouseMove(ctx, p.Coordinate)
	case "scroll":
		return t.scroll(ctx, p.Coordinate, p.Direction, p.Amount)
	case "left_click_drag":
		return t.drag(ctx, p.StartCoordinate, p.Coordinate)
	case "cursor_position":
		return &CanonicalResult{Coordinate: [2]int{t.cursorX, t.cursorY}}, nil
	default:
		return nil, fmt.Errorf("unknown computer action %q", p.Action)
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

func (t *ComputerTool) click(ctx context.Context, coord *[2]int, buttonMask int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for click action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]

	if err := t.soap.MouseClick(coord[0], coord[1], buttonMask); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) doubleClick(ctx context.Context, coord *[2]int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for double_click action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]

	if err := t.soap.MouseDoubleClick(coord[0], coord[1], 1); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) typeText(ctx context.Context, text string) (*CanonicalResult, error) {
	if text == "" {
		return nil, fmt.Errorf("field 'text' required for action 'type'")
	}

	codes := vbox.TextToScancodes(text)
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

func (t *ComputerTool) key(ctx context.Context, text string) (*CanonicalResult, error) {
	if text == "" {
		return nil, fmt.Errorf("field 'text' required for action 'key'")
	}

	codes := vbox.KeyToScancodes(text)
	if err := vbox.SendScancodes(ctx, t.vmName, codes); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) mouseMove(ctx context.Context, coord *[2]int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for mouse_move action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]

	if err := t.soap.MouseMoveAbsolute(coord[0], coord[1]); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) scroll(ctx context.Context, coord *[2]int, direction string, amount int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for scroll action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]

	dz := amount
	if dz == 0 {
		dz = 3
	}

	switch direction {
	case "down", "":
		dz = -dz
	case "up":
		// dz stays positive
	default:
		return nil, fmt.Errorf("invalid scroll direction %q; expected 'up' or 'down'", direction)
	}

	if err := t.soap.MouseScroll(coord[0], coord[1], dz); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) drag(ctx context.Context, start, end *[2]int) (*CanonicalResult, error) {
	if start == nil {
		return nil, fmt.Errorf("field 'start_coordinate' required for action 'left_click_drag'")
	}
	if end == nil {
		return nil, fmt.Errorf("field 'coordinate' required for action 'left_click_drag'")
	}

	sx, sy := start[0], start[1]
	ex, ey := end[0], end[1]

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

	if err := t.soap.MouseMoveAbsolute(ex, ey); err != nil {
		return nil, err
	}
	time.Sleep(50 * time.Millisecond)

	if err := t.soap.MouseUp(ex, ey); err != nil {
		return nil, err
	}
	success = true

	t.cursorX, t.cursorY = ex, ey
	return &CanonicalResult{}, nil
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
