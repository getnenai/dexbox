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

func (c *ComputerTool) Key(ctx context.Context, key string) error {
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
	_, err := c.Runner.runCommand(ctx, "xdotool", "mousemove", "--sync", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y))
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
