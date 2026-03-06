package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/getnenai/dexbox/internal/anthropic"
	"github.com/getnenai/dexbox/internal/tools"
)

const MaxImagesInContext = 10
const MaxTokens = 4096

type Agent struct {
	client       *anthropic.Client
	computerTool *tools.ComputerTool
	events       chan map[string]any
}

func NewAgent(apiKey string, baseURL string, events chan map[string]any) *Agent {
	return &Agent{
		client:       anthropic.NewClient(apiKey, baseURL),
		computerTool: tools.NewComputerTool(),
		events:       events,
	}
}

// SamplingLoop drives the VLM sampling loop.
func (a *Agent) SamplingLoop(ctx context.Context, model string, instruction string, maxIterations int) ([]anthropic.Message, error) {
	// Emit started event
	if a.events != nil {
		a.events <- map[string]any{
			"type": "progress",
			"data": map[string]any{
				"type": "log",
				"data": fmt.Sprintf("Starting execution with model %s, instruction: %s", model, instruction),
			},
		}
	}

	messages := []anthropic.Message{
		{
			Role: "user",
			Content: []anthropic.ContentBlock{
				{Type: "text", Text: instruction},
			},
		},
	}

	// Define tools based on model (we hardcode for computer_20250124 or computer_20241022 as fallback)
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

	displayNum := 0
	if d := os.Getenv("DISPLAY_NUM"); d != "" {
		displayNum, _ = strconv.Atoi(d)
	}

	apiType := "computer_20250124"
	betaFlag := "computer-use-2025-01-24"

	// Match logic from src/dexbox/tools/groups.py
	if model == "claude-opus-4-6" || model == "claude-sonnet-4-6" {
		apiType = "computer_20251124"
		betaFlag = "computer-use-2025-11-24"
	}

	toolList := []anthropic.Tool{
		{
			Type:            apiType,
			Name:            "computer",
			DisplayWidthPx:  scaledWidth,
			DisplayHeightPx: scaledHeight,
			DisplayNumber:   displayNum,
		},
	}

	betas := []string{betaFlag, "prompt-caching-2024-07-31"}

	for iteration := 0; iteration < maxIterations; iteration++ {
		// Filter images
		maybeFilterImages(messages)

		req := &anthropic.CreateMessageRequest{
			Model:     model,
			MaxTokens: MaxTokens,
			Messages:  messages,
			Tools:     toolList,
		}

		resp, err := a.client.CreateMessage(ctx, req, betas)
		if err != nil {
			return messages, fmt.Errorf("anthropic api error: %w", err)
		}

		// Convert response content to message format
		var assistantContent []anthropic.ContentBlock
		var toolUses []anthropic.ContentBlock

		for _, block := range resp.Content {
			if block.Type == "text" {
				assistantContent = append(assistantContent, anthropic.ContentBlock{Type: "text", Text: block.Text})
				if block.Text != "" {
					log.Printf("Agent thought: %s", block.Text)
					if a.events != nil {
						a.events <- map[string]any{
							"type": "progress",
							"data": map[string]any{
								"type": "text",
								"data": block.Text,
							},
						}
					}
				}
			} else if block.Type == "tool_use" {
				assistantContent = append(assistantContent, anthropic.ContentBlock{
					Type:  "tool_use",
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				})
				toolUses = append(toolUses, block)
			}
		}

		messages = append(messages, anthropic.Message{
			Role:    "assistant",
			Content: assistantContent,
		})

		if resp.StopReason == "end_turn" || len(toolUses) == 0 {
			break
		}

		// Execute tool calls
		var toolResults []anthropic.ContentBlock
		for _, toolUse := range toolUses {
			if a.events != nil {
				a.events <- map[string]any{
					"type": "progress",
					"data": map[string]any{
						"type": "tool_call",
						"data": map[string]any{
							"name":  toolUse.Name,
							"input": toolUse.Input,
						},
					},
				}
			}

			// We only support the computer tool internally here for now
			var resultStr string
			var resultB64 string
			var resultErr error

			if toolUse.Name == "computer" {
				resultStr, resultB64, resultErr = a.executeComputerTool(ctx, toolUse.Input)
			} else {
				resultErr = fmt.Errorf("unknown tool: %s", toolUse.Name)
			}

			if resultErr != nil {
				log.Printf("Tool %s failed: %v", toolUse.Name, resultErr)
			} else {
				log.Printf("Tool %s succeeded", toolUse.Name)
			}

			errStr := ""
			if resultErr != nil {
				errStr = resultErr.Error()
			}

			if a.events != nil {
				a.events <- map[string]any{
					"type": "progress",
					"data": map[string]any{
						"type": "tool_result",
						"data": map[string]any{
							"name":  toolUse.Name,
							"error": errStr,
						},
					},
				}
			}

			// Format tool result block
			var contentBlocks []anthropic.ContentBlock
			if resultStr != "" {
				contentBlocks = append(contentBlocks, anthropic.ContentBlock{Type: "text", Text: resultStr})
			}
			if resultB64 != "" {
				contentBlocks = append(contentBlocks, anthropic.ContentBlock{
					Type: "image",
					Source: &anthropic.ImageSource{
						Type:      "base64",
						MediaType: "image/png",
						Data:      resultB64,
					},
				})
			}
			if resultErr != nil {
				contentBlocks = append(contentBlocks, anthropic.ContentBlock{Type: "text", Text: resultErr.Error()})
			}

			// Anthropic content for tool_result can be a string or a list of blocks.
			toolResults = append(toolResults, anthropic.ContentBlock{
				Type:      "tool_result",
				ToolUseID: toolUse.ID,
				Content:   contentBlocks,
				IsError:   resultErr != nil,
			})
		}

		messages = append(messages, anthropic.Message{
			Role:    "user",
			Content: toolResults,
		})
	}

	return messages, fmt.Errorf("hit max iterations (%d) without completing task", maxIterations)
}

func (a *Agent) executeComputerTool(ctx context.Context, input any) (string, string, error) {
	inputMap, ok := input.(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("invalid input type")
	}

	action, _ := inputMap["action"].(string)
	var err error

	switch action {
	case "key":
		text, _ := inputMap["text"].(string)
		err = a.computerTool.Key(ctx, text)
	case "type":
		text, _ := inputMap["text"].(string)
		err = a.computerTool.Type(ctx, text)
	case "mouse_move":
		coords, ok := inputMap["coordinate"].([]any)
		if ok && len(coords) >= 2 {
			x := int(coords[0].(float64))
			y := int(coords[1].(float64))
			x, y = a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, x, y)
			err = a.computerTool.Move(ctx, x, y)
		} else {
			return "", "", fmt.Errorf("invalid coordinate")
		}
	case "left_click", "right_click", "middle_click":
		coords, ok := inputMap["coordinate"].([]any)
		if ok && len(coords) >= 2 {
			x := int(coords[0].(float64))
			y := int(coords[1].(float64))
			x, y = a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, x, y)
			if err = a.computerTool.Move(ctx, x, y); err != nil {
				return "", "", err
			}
		}

		button := "left"
		if action == "right_click" {
			button = "right"
		} else if action == "middle_click" {
			button = "middle"
		}
		err = a.computerTool.Click(ctx, button)
	case "left_click_drag":
		coords, ok := inputMap["coordinate"].([]any)
		if ok && len(coords) >= 2 {
			x := int(coords[0].(float64))
			y := int(coords[1].(float64))
			x, y = a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, x, y)
			// Approximate drag by clicking - we may need a specific drag implement in tools
			err = a.computerTool.Move(ctx, x, y)
			if err == nil {
				err = a.computerTool.Click(ctx, "left")
			}
		} else {
			return "", "", fmt.Errorf("invalid coordinate for drag")
		}
	case "double_click":
		coords, ok := inputMap["coordinate"].([]any)
		if ok && len(coords) >= 2 {
			x := int(coords[0].(float64))
			y := int(coords[1].(float64))
			x, y = a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, x, y)
			if err = a.computerTool.Move(ctx, x, y); err != nil {
				return "", "", err
			}
		}

		err = a.computerTool.Click(ctx, "left")
		if err == nil {
			time.Sleep(50 * time.Millisecond)
			err = a.computerTool.Click(ctx, "left")
		}
	case "wait":
		duration := time.Duration(3 * time.Second) // default to 3s if not provided / parseable
		if d, ok := inputMap["duration"].(float64); ok {
			duration = time.Duration(d * float64(time.Second))
		}
		time.Sleep(duration)
	case "screenshot":
		// TODO: the artifacts dir
		b64, screenErr := a.computerTool.Screenshot(ctx, "/tmp")
		return "", b64, screenErr
	case "scroll":
		coords, ok := inputMap["coordinate"].([]any)
		var x, y *int
		if ok && len(coords) >= 2 {
			xVal := int(coords[0].(float64))
			yVal := int(coords[1].(float64))
			xVal, yVal = a.computerTool.ScaleCoordinates(tools.ScalingSourceAPI, xVal, yVal)
			x = &xVal
			y = &yVal
		}
		direction, ok := inputMap["scroll_direction"].(string)
		if !ok || direction == "" {
			direction = "down"
		}
		amount := 3
		if amt, ok := inputMap["amount"].(float64); ok {
			amount = int(amt)
		}

		err = a.computerTool.Scroll(ctx, direction, amount, x, y)
	default:
		return "", "", fmt.Errorf("unsupported action: %s", action)
	}

	if err != nil {
		return "", "", err
	}

	b64, err := a.computerTool.Screenshot(ctx, "/tmp")
	if err != nil {
		return "", "", fmt.Errorf("error taking screenshot after action: %w", err)
	}
	return "", b64, nil
}

func maybeFilterImages(messages []anthropic.Message) {
	imageCount := 0

	// Iterate backwards over messages
	for i := len(messages) - 1; i >= 0; i-- {
		msg := &messages[i]

		// For tool_results inside user messages:
		for j := len(msg.Content) - 1; j >= 0; j-- {
			block := &msg.Content[j]

			if block.Type == "tool_result" {
				// Content can be []ContentBlock. We need type assertion if it is "any".
				// Oh, in our anthropic models, Content in tool_result is `any`. In our agent, we set it to `[]ContentBlock`.
				blocks, ok := block.Content.([]anthropic.ContentBlock)
				if !ok {
					continue
				}

				// Collect indices of images to remove
				var keepBlocks []anthropic.ContentBlock

				// Iterate backwards over inner blocks
				for k := len(blocks) - 1; k >= 0; k-- {
					inner := blocks[k]
					if inner.Type == "image" {
						imageCount++
						if imageCount <= MaxImagesInContext {
							keepBlocks = append([]anthropic.ContentBlock{inner}, keepBlocks...)
						}
					} else {
						keepBlocks = append([]anthropic.ContentBlock{inner}, keepBlocks...)
					}
				}

				block.Content = keepBlocks
			}
		}
	}
}
