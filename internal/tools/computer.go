package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"time"

	"github.com/getnenai/dexbox/internal/desktop"
	"golang.org/x/image/draw"
)

// ComputerTool executes computer-use actions (screenshot, keyboard, mouse)
// against any Desktop backend (VBox, RDP, etc.).
type ComputerTool struct {
	desktop  desktop.Desktop
	width    int
	height   int
	cursorX  int
	cursorY  int
	displayW int // actual VM display dimensions (what SOAP uses), set on first screenshot
	displayH int
}

// NewComputerTool creates a computer tool bound to a Desktop.
func NewComputerTool(d desktop.Desktop, width, height int) *ComputerTool {
	return &ComputerTool{
		desktop: d,
		width:   width,
		height:  height,
	}
}

// Execute dispatches a canonical computer action.
func (t *ComputerTool) Execute(ctx context.Context, action *CanonicalAction) (*CanonicalResult, error) {
	var p ComputerParams
	if err := action.UnmarshalParams(&p); err != nil {
		return nil, fmt.Errorf("invalid computer params: %w", err)
	}

	// action.Action is set by the adapter (not in Params), so use it directly.
	switch action.Action {
	case "screenshot":
		return t.screenshot(ctx)
	case "left_click", "click":
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
		return nil, fmt.Errorf("unknown computer action %q", action.Action)
	}
}

func (t *ComputerTool) screenshot(ctx context.Context) (*CanonicalResult, error) {
	raw, err := t.desktop.Screenshot(ctx)
	if err != nil {
		return nil, err
	}

	resized, srcW, srcH, err := resizePNG(raw, t.width, t.height)
	if err != nil {
		resized = raw
	} else {
		t.displayW = srcW
		t.displayH = srcH
	}

	return &CanonicalResult{Image: resized}, nil
}

// scaleCoord maps coordinates from screenshot space to VM display space.
func (t *ComputerTool) scaleCoord(coord *[2]int) *[2]int {
	if coord == nil || t.displayW == 0 || t.displayH == 0 {
		return coord
	}
	if t.displayW == t.width && t.displayH == t.height {
		return coord
	}
	return &[2]int{
		coord[0] * t.displayW / t.width,
		coord[1] * t.displayH / t.height,
	}
}

func (t *ComputerTool) click(ctx context.Context, coord *[2]int, buttonMask int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for click action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]
	sc := t.scaleCoord(coord)

	if err := t.desktop.MouseClick(sc[0], sc[1], buttonMask); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) doubleClick(ctx context.Context, coord *[2]int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for double_click action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]
	sc := t.scaleCoord(coord)

	if err := t.desktop.MouseDoubleClick(sc[0], sc[1], 1); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) typeText(ctx context.Context, text string) (*CanonicalResult, error) {
	if text == "" {
		return nil, fmt.Errorf("field 'text' required for action 'type'")
	}

	if err := t.desktop.TypeText(ctx, text); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) key(ctx context.Context, text string) (*CanonicalResult, error) {
	if text == "" {
		return nil, fmt.Errorf("field 'text' required for action 'key'")
	}

	if err := t.desktop.KeyPress(ctx, text); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) mouseMove(ctx context.Context, coord *[2]int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for mouse_move action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]
	sc := t.scaleCoord(coord)

	if err := t.desktop.MouseMoveAbsolute(sc[0], sc[1]); err != nil {
		return nil, err
	}
	return &CanonicalResult{}, nil
}

func (t *ComputerTool) scroll(ctx context.Context, coord *[2]int, direction string, amount int) (*CanonicalResult, error) {
	if coord == nil {
		return nil, fmt.Errorf("field 'coordinate' required for scroll action")
	}
	t.cursorX, t.cursorY = coord[0], coord[1]
	sc := t.scaleCoord(coord)

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

	if err := t.desktop.MouseScroll(sc[0], sc[1], dz); err != nil {
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

	// Keep original end coords for cursor tracking (screenshot space).
	t.cursorX, t.cursorY = end[0], end[1]

	ss := t.scaleCoord(start)
	se := t.scaleCoord(end)
	sx, sy := ss[0], ss[1]
	ex, ey := se[0], se[1]

	if err := t.desktop.MouseDown(sx, sy, 1); err != nil {
		return nil, err
	}

	var success bool
	defer func() {
		if !success {
			_ = t.desktop.MouseUp(sx, sy)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	if err := t.desktop.MouseMoveAbsolute(ex, ey); err != nil {
		return nil, err
	}
	time.Sleep(50 * time.Millisecond)

	if err := t.desktop.MouseUp(ex, ey); err != nil {
		return nil, err
	}
	success = true
	return &CanonicalResult{}, nil
}

// resizePNG decodes a PNG, resizes it to target dimensions, re-encodes it,
// and returns the original source dimensions alongside the resized bytes.
func resizePNG(data []byte, width, height int) (resized []byte, srcW, srcH int, err error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}

	srcBounds := src.Bounds()
	srcW, srcH = srcBounds.Dx(), srcBounds.Dy()

	if srcW == width && srcH == height {
		return data, srcW, srcH, nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, srcBounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), srcW, srcH, nil
}

// ImageToBase64 encodes raw PNG bytes as a base64 string.
func ImageToBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
