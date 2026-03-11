package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/getnenai/dexbox/internal/tools"
	"github.com/getnenai/dexbox/pkg/cua"
)

type Agent struct {
	provider     cua.Provider
	computerTool *tools.ComputerTool
	events       chan map[string]any
}

func NewAgent(provider cua.Provider, events chan map[string]any) *Agent {
	return &Agent{
		provider:     provider,
		computerTool: tools.NewComputerTool(),
		events:       events,
	}
}

// SamplingLoop drives the CUA sampling loop.
func (a *Agent) SamplingLoop(ctx context.Context, instruction string, maxIterations int) ([]cua.Message, error) {
	if a.events != nil {
		a.events <- map[string]any{
			"type": "progress",
			"data": map[string]any{
				"type": "log",
				"data": fmt.Sprintf("Starting execution, instruction: %s", instruction),
			},
		}
	}

	messages := []cua.Message{
		{
			Role:    "user",
			Content: instruction,
		},
	}

	width := 0
	if w := os.Getenv("WIDTH"); w != "" {
		width, _ = strconv.Atoi(w)
	}
	height := 0
	if h := os.Getenv("HEIGHT"); h != "" {
		height, _ = strconv.Atoi(h)
	}
	scaledWidth, scaledHeight := a.computerTool.ScaleCoordinates(tools.ScalingSourceComputer, width, height)
	if scaledWidth == 0 {
		scaledWidth = width
		scaledHeight = height
	}

	err := a.provider.Setup(cua.DisplayConfig{
		WidthPX:  scaledWidth,
		HeightPX: scaledHeight,
	})
	if err != nil {
		return messages, fmt.Errorf("provider setup failed: %w", err)
	}

	var state cua.VisualState

	for iteration := 0; iteration < maxIterations; iteration++ {
		// Refresh the visual state with a new screenshot, unless the previous
		// action already set state to a zoomed image (in which case the model
		// should see the zoom result rather than a fresh full screenshot).
		if a.provider.NeedsVisuals() && state.ImageBase64 == "" {
			b64, err := a.computerTool.Screenshot(ctx, "/tmp")
			if err != nil {
				return messages, fmt.Errorf("failed to take required screenshot: %w", err)
			}
			if b64 == "" {
				return messages, fmt.Errorf("screenshot returned empty base64 string")
			}
			state = cua.VisualState{
				ImageBase64: b64,
				Format:      "png",
			}
		}

		action, assistantText, err := a.provider.PredictAction(ctx, messages, state)
		if err != nil {
			if err.Error() == "no computer tool use block found in response" {
				// No action returned => End of turn
				break
			}
			return messages, fmt.Errorf("provider predict error: %w", err)
		}

		if action == nil {
			if assistantText != "" {
				messages = append(messages, cua.Message{
					Role:    "assistant",
					Content: assistantText,
				})
			}
			break
		}

		log.Printf("Agent action: %v", action.Type)

		if a.events != nil {
			actionMap := map[string]any{
				"action": string(action.Type),
			}
			if action.Text != "" {
				actionMap["text"] = action.Text
			} else if action.Type == cua.ActionMouseMove || action.Type == cua.ActionDrag {
				if action.X != nil && action.Y != nil {
					actionMap["coordinate"] = []int{*action.X, *action.Y}
				}
			} else if action.Type == cua.ActionClick || action.Type == cua.ActionRightClick || action.Type == cua.ActionMiddleClick || action.Type == cua.ActionDoubleClick || action.Type == cua.ActionTripleClick {
				if action.HasCoordinate && action.X != nil && action.Y != nil {
					actionMap["coordinate"] = []int{*action.X, *action.Y}
				}
			} else if action.Type == cua.ActionWait && action.Duration > 0 {
				actionMap["duration"] = action.Duration
			}

			a.events <- map[string]any{
				"type": "progress",
				"data": map[string]any{
					"type": "tool_call",
					"data": map[string]any{
						"name":  "computer",
						"input": actionMap,
					},
				},
			}
		}

		// Clear state so a fresh screenshot is taken next iteration by default.
		state = cua.VisualState{}

		zoomedImg, err := a.executeAction(ctx, action)
		if err != nil {
			log.Printf("Tool failed: %v", err)
		} else {
			log.Printf("Tool succeeded")
			if zoomedImg != "" {
				// Propagate the zoomed image so the next iteration sends it to
				// the model instead of taking a new full screenshot.
				state = cua.VisualState{
					ImageBase64: zoomedImg,
					Format:      "png",
				}
			}
		}

		delaySec := 2.0
		if d := os.Getenv("SCREENSHOT_DELAY"); d != "" {
			if parsed, err := strconv.ParseFloat(d, 64); err == nil {
				delaySec = parsed
			}
		}
		if action.Type == cua.ActionWait && action.Duration > 0 {
			delaySec = action.Duration
		}
		time.Sleep(time.Duration(delaySec * float64(time.Second)))

		errStr := ""
		if err != nil {
			errStr = err.Error()
		}

		if a.events != nil {
			a.events <- map[string]any{
				"type": "progress",
				"data": map[string]any{
					"type": "tool_result",
					"data": map[string]any{
						"name":  "computer",
						"error": errStr,
					},
				},
			}
		}

		// Record the tool result back into history
		messages = append(messages, cua.Message{
			Role:    "assistant",
			Content: assistantText,
			Action:  action,
		})

		resText := "Completed"
		if err != nil {
			resText = "Error: " + err.Error()
		}
		messages = append(messages, cua.Message{
			Role:       "user",
			Result:     resText,
			ToolCallID: action.ToolCallID,
		})
	}

	return messages, nil
}

// executeAction executes a single CUA action. It returns a non-empty base64
// image string when the action produced a zoomed screenshot (ActionZoom), so
// the caller can feed it directly into the next model call instead of taking
// a new full screenshot.
func (a *Agent) executeAction(ctx context.Context, action *cua.Action) (string, error) {
	var err error
	var zoomedImg string

	switch action.Type {
	case cua.ActionKey:
		err = a.computerTool.Key(ctx, action.Text)
	case cua.ActionTypeString:
		err = a.computerTool.Type(ctx, action.Text)
	case cua.ActionMouseMove:
		x, y := a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, *action.X, *action.Y)
		err = a.computerTool.Move(ctx, x, y)
	case cua.ActionClick, cua.ActionRightClick, cua.ActionMiddleClick:
		if action.HasCoordinate {
			x, y := a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, *action.X, *action.Y)
			if err = a.computerTool.Move(ctx, x, y); err != nil {
				return "", err
			}
		}

		button := "left"
		if action.Type == cua.ActionRightClick {
			button = "right"
		} else if action.Type == cua.ActionMiddleClick {
			button = "middle"
		}
		err = a.computerTool.Click(ctx, button)
	case cua.ActionDrag:
		if action.HasCoordinate {
			x, y := a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, *action.X, *action.Y)
			err = a.computerTool.Move(ctx, x, y)
		}
		if err == nil {
			err = a.computerTool.Click(ctx, "left")
		}
	case cua.ActionDoubleClick, cua.ActionTripleClick:
		if action.HasCoordinate {
			x, y := a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, *action.X, *action.Y)
			if err = a.computerTool.Move(ctx, x, y); err != nil {
				return "", err
			}
		}

		clicks := 2
		if action.Type == cua.ActionTripleClick {
			clicks = 3
		}

		for i := 0; i < clicks; i++ {
			err = a.computerTool.Click(ctx, "left")
			if err != nil {
				break
			}
			if i < clicks-1 {
				time.Sleep(50 * time.Millisecond)
			}
		}
	case cua.ActionScreenshot, cua.ActionWait:
		// Do nothing, the next iteration will take a screenshot anyway
		return "", nil
	case cua.ActionScroll:
		var xPtr, yPtr *int
		if action.X != nil || action.Y != nil {
			xVal, yVal := 0, 0
			if action.X != nil {
				xVal = *action.X
			}
			if action.Y != nil {
				yVal = *action.Y
			}
			scaledX, scaledY := a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, xVal, yVal)
			xPtr = &scaledX
			yPtr = &scaledY
		}
		direction := "down" // Default interpretation, maybe handled better later if needed
		amount := 3
		err = a.computerTool.Scroll(ctx, direction, amount, xPtr, yPtr)
	case cua.ActionCursorPosition:
		var x, y int
		x, y, err = a.computerTool.CursorPosition(ctx)
		if err == nil {
			// Save back to assistant text for LLM visibility
			// Note: this happens automatically outside this func based on action parsing,
			// but we can augment the result later.
			action.X = &x
			action.Y = &y
		}
	case cua.ActionLeftMouseDown:
		err = a.computerTool.MouseDown(ctx, "left")
	case cua.ActionLeftMouseUp:
		err = a.computerTool.MouseUp(ctx, "left")
	case cua.ActionLeftClickDrag:
		if action.HasCoordinate {
			x, y := a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, *action.X, *action.Y)
			err = a.computerTool.MouseDown(ctx, "left")
			if err == nil {
				err = a.computerTool.Move(ctx, x, y)
			}
			if err == nil {
				err = a.computerTool.MouseUp(ctx, "left")
			}
		} else {
			return "", fmt.Errorf("coordinate is required for left_click_drag")
		}
	case cua.ActionHoldKey:
		if action.Text == "" {
			return "", fmt.Errorf("text is required for hold_key")
		}
		err = a.computerTool.HoldKey(ctx, action.Text, action.Duration)
	case cua.ActionZoom:
		if len(action.ZoomRegion) != 4 {
			return "", fmt.Errorf("zoom requires ZoomRegion with 4 elements [x0, y0, x1, y1]")
		}
		zoomedImg, err = a.computerTool.Zoom(ctx, action.ZoomRegion[0], action.ZoomRegion[1], action.ZoomRegion[2], action.ZoomRegion[3])
	default:
		return "", fmt.Errorf("unsupported action: %s", action.Type)
	}

	return zoomedImg, err
}
