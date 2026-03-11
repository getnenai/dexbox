package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type ActionResponse struct {
	Success bool    `json:"success"`
	Error   *string `json:"error,omitempty"`
}

type CommandRunner interface {
	runCommand(ctx context.Context, cmdName string, args ...string) (string, error)
}

type defaultRunner struct {
	displayNum string
}

func (r *defaultRunner) runCommand(ctx context.Context, cmdName string, args ...string) (string, error) {
	fullCmd := []string{cmdName}
	if r.displayNum != "" {
		fullCmd = append([]string{"DISPLAY=:" + r.displayNum}, fullCmd...)
	}
	fullCmd = append(fullCmd, args...)

	cmd := exec.CommandContext(ctx, cmdName, args...)
	if r.displayNum != "" {
		cmd.Env = append(os.Environ(), "DISPLAY=:"+r.displayNum)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command %s %v failed: %w (output: %s)", cmdName, args, err, string(output))
	}
	return string(output), nil
}

type ComputerTool struct {
	DisplayNum string
	Width      string
	Height     string
	Runner     CommandRunner
}

type Resolution struct {
	Width  int
	Height int
}

var maxScalingTargets = []Resolution{
	{1024, 768}, // XGA
	{1280, 800}, // WXGA
	{1366, 768}, // FWXGA
}

type ScalingSource string

const (
	ScalingSourceComputer ScalingSource = "computer"
	ScalingSourceAPI      ScalingSource = "api"
)

func (c *ComputerTool) ScaleCoordinates(source ScalingSource, x, y int) (int, int) {
	width, _ := strconv.Atoi(c.Width)
	height, _ := strconv.Atoi(c.Height)

	if width == 0 || height == 0 {
		return x, y
	}

	ratio := float64(width) / float64(height)
	var targetDimension *Resolution

	for _, dim := range maxScalingTargets {
		dimRatio := float64(dim.Width) / float64(dim.Height)
		diff := dimRatio - ratio
		if diff < 0 {
			diff = -diff
		}
		if diff < 0.02 {
			if dim.Width < width {
				target := dim
				targetDimension = &target
			}
			break
		}
	}

	if targetDimension == nil {
		return x, y
	}

	xScalingFactor := float64(targetDimension.Width) / float64(width)
	yScalingFactor := float64(targetDimension.Height) / float64(height)

	if source == ScalingSourceAPI {
		return int(math.Round(float64(x) / xScalingFactor)), int(math.Round(float64(y) / yScalingFactor))
	}

	return int(math.Round(float64(x) * xScalingFactor)), int(math.Round(float64(y) * yScalingFactor))
}

func NewComputerTool() *ComputerTool {
	displayNum := os.Getenv("DISPLAY_NUM")
	return &ComputerTool{
		DisplayNum: displayNum,
		Width:      os.Getenv("WIDTH"),
		Height:     os.Getenv("HEIGHT"),
		Runner:     &defaultRunner{displayNum: displayNum},
	}
}

func (c *ComputerTool) Type(ctx context.Context, text string) error {
	_, err := c.Runner.runCommand(ctx, "xdotool", "type", "--delay", "12", "--", text)
	return err
}

func mapX11Key(key string) string {
	keyMap := map[string]string{
		"enter":     "Return",
		"return":    "Return",
		"escape":    "Escape",
		"space":     "space",
		"tab":       "Tab",
		"backspace": "BackSpace",
		"up":        "Up",
		"down":      "Down",
		"left":      "Left",
		"right":     "Right",
		"page_up":   "Page_Up",
		"page_down": "Page_Down",
		"home":      "Home",
		"end":       "End",
	}
	if mapped, ok := keyMap[strings.ToLower(key)]; ok {
		return mapped
	}
	return key
}

func (c *ComputerTool) Key(ctx context.Context, key string) error {
	key = mapX11Key(key)
	_, err := c.Runner.runCommand(ctx, "xdotool", "key", "--", key)
	return err
}

func (c *ComputerTool) Click(ctx context.Context, button string) error {
	btnMap := map[string]string{
		"left":   "1",
		"right":  "3",
		"middle": "2",
	}
	btn, ok := btnMap[button]
	if !ok {
		btn = "1"
	}

	_, err := c.Runner.runCommand(ctx, "xdotool", "click", btn)
	return err
}

func (c *ComputerTool) Move(ctx context.Context, x, y int) error {
	// xdotool has a known idiosyncrasy: if you tell it to move the mouse with --sync to
	// coordinates it is already at, it hangs indefinitely waiting for a motion event.
	// We do a quick check to see if we're already there.
	output, err := c.Runner.runCommand(ctx, "xdotool", "getmouselocation", "--shell")
	if err == nil {
		var curX, curY int
		n, _ := fmt.Sscanf(output, "X=%d\nY=%d", &curX, &curY)
		if n >= 2 && curX == x && curY == y {
			return nil
		}
	}

	_, err = c.Runner.runCommand(ctx, "xdotool", "mousemove", "--sync", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y))
	return err
}

func (c *ComputerTool) Scroll(ctx context.Context, direction string, amount int, x, y *int) error {
	mouseMove := ""
	if x != nil && y != nil {
		mouseMove = fmt.Sprintf("mousemove --sync %d %d ", *x, *y)
	}

	btnMap := map[string]string{
		"up":    "4",
		"down":  "5",
		"left":  "6",
		"right": "7",
	}
	btn, ok := btnMap[direction]
	if !ok {
		btn = "5"
	}

	args := []string{}
	if mouseMove != "" {
		args = append(args, "mousemove", "--sync", fmt.Sprintf("%d", *x), fmt.Sprintf("%d", *y))
	}
	args = append(args, "click", "--repeat", fmt.Sprintf("%d", amount), btn)

	_, err := c.Runner.runCommand(ctx, "xdotool", args...)
	return err
}

func (c *ComputerTool) Screenshot(ctx context.Context, artifactsDir string) (string, error) {
	// 1. Determine screenshot tool
	var cmdName string
	var args []string

	path := filepath.Join("/tmp", fmt.Sprintf("screenshot_%s.png", uuid.New().String()[:8]))

	if _, err := exec.LookPath("gnome-screenshot"); err == nil {
		cmdName = "gnome-screenshot"
		args = []string{"-f", path, "-p"}
	} else if _, err := exec.LookPath("scrot"); err == nil {
		cmdName = "scrot"
		args = []string{"-p", path}
	} else {
		return "", fmt.Errorf("neither gnome-screenshot nor scrot found")
	}

	// 2. Take screenshot
	_, err := c.Runner.runCommand(ctx, cmdName, args...)
	if err != nil {
		return "", err
	}

	width, _ := strconv.Atoi(c.Width)
	height, _ := strconv.Atoi(c.Height)
	scaledW, scaledH := c.ScaleCoordinates(ScalingSourceComputer, width, height)
	if scaledW != width && scaledW != 0 {
		resizeCmd := fmt.Sprintf("%dx%d!", scaledW, scaledH)
		_, err := c.Runner.runCommand(ctx, "convert", path, "-resize", resizeCmd, path)
		if err != nil {
			return "", fmt.Errorf("failed to resize screenshot: %w", err)
		}
	}

	// 3. Read and encode
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	defer os.Remove(path)

	return base64.StdEncoding.EncodeToString(data), nil
}

func (c *ComputerTool) CursorPosition(ctx context.Context) (int, int, error) {
	output, err := c.Runner.runCommand(ctx, "xdotool", "getmouselocation", "--shell")
	if err != nil {
		return 0, 0, err
	}

	var x, y int
	// output looks like: X=123\nY=456\nSCREEN=0\nWINDOW=123456
	n, _ := fmt.Sscanf(output, "X=%d\nY=%d", &x, &y)
	if n < 2 {
		return 0, 0, fmt.Errorf("failed to parse cursor position from output: %q", output)
	}

	// Coordinates returned from xdotool need scaling back to API space
	scaledX, scaledY := c.ScaleCoordinates(ScalingSourceComputer, x, y)
	return scaledX, scaledY, nil
}

func (c *ComputerTool) MouseDown(ctx context.Context, button string) error {
	btnMap := map[string]string{
		"left":   "1",
		"right":  "3",
		"middle": "2",
	}
	btn, ok := btnMap[button]
	if !ok {
		return fmt.Errorf("invalid mouse button: %s", button)
	}
	_, err := c.Runner.runCommand(ctx, "xdotool", "mousedown", btn)
	return err
}

func (c *ComputerTool) MouseUp(ctx context.Context, button string) error {
	btnMap := map[string]string{
		"left":   "1",
		"right":  "3",
		"middle": "2",
	}
	btn, ok := btnMap[button]
	if !ok {
		return fmt.Errorf("invalid mouse button: %s", button)
	}
	_, err := c.Runner.runCommand(ctx, "xdotool", "mouseup", btn)
	return err
}

func (c *ComputerTool) HoldKey(ctx context.Context, key string, duration float64) error {
	key = mapX11Key(key)
	_, err := c.Runner.runCommand(ctx, "xdotool", "keydown", key, "sleep", fmt.Sprintf("%f", duration), "keyup", key)
	return err
}

func (c *ComputerTool) Zoom(ctx context.Context, x0, y0, x1, y1 int) (string, error) {
	// Re-use screenshot logic to take a full screenshot, then crop it.
	b64, err := c.Screenshot(ctx, "/tmp")
	if err != nil {
		return "", err
	}

	rawImage, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}

	path := filepath.Join("/tmp", fmt.Sprintf("zoom_tmp_%s.png", uuid.New().String()[:8]))
	err = os.WriteFile(path, rawImage, 0644)
	if err != nil {
		return "", err
	}
	defer os.Remove(path)

	croppedPath := filepath.Join("/tmp", fmt.Sprintf("zoom_cropped_%s.png", uuid.New().String()[:8]))

	// Screenshot() already returns an API-space image, so crop directly
	// using the API-space coordinates.
	width := x1 - x0
	height := y1 - y0
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("invalid zoom region: width and height must be positive")
	}

	// Crop using ImageMagick
	cropCmd := fmt.Sprintf("%dx%d+%d+%d", width, height, x0, y0)
	_, err = c.Runner.runCommand(ctx, "convert", path, "-crop", cropCmd, "+repage", croppedPath)
	if err != nil {
		return "", fmt.Errorf("failed to crop zoomed region: %w", err)
	}
	defer os.Remove(croppedPath)

	data, err := os.ReadFile(croppedPath)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}
