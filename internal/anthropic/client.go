package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/getnenai/dexbox/pkg/cua"
)

const (
	defaultBaseURL = "https://api.anthropic.com"
	betaHeader     = "computer-use-2025-01-24"
	apiVersion     = "2023-06-01"
)

// Client implements cua.Provider for Anthropic's Claude models.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	config     cua.DisplayConfig
}

// NewClient creates a new Anthropic provider client.
func NewClient(apiKey, model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{},
	}
}

// Setup initializes the display configuration used for the computer tool.
func (c *Client) Setup(config cua.DisplayConfig) error {
	c.config = config
	return nil
}

// NeedsVisuals returns true because Anthropic always needs a screenshot per action.
func (c *Client) NeedsVisuals() bool {
	return true
}

// FormatHistory translates cua.Message interaction history into Anthropic's specific message schema.
func (c *Client) FormatHistory(history []cua.Message) (any, error) {
	var messages []map[string]any

	for _, msg := range history {
		// Create anthropic format block
		var blocks []map[string]any

		if msg.Role == "user" {
			if msg.Content != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": msg.Content,
				})
			}
			if msg.Result != "" {
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": msg.ToolCallID,
					"content": []map[string]any{
						{
							"type": "text",
							"text": msg.Result,
						},
					},
				})
			}
		} else if msg.Role == "assistant" {
			if msg.Content != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": msg.Content,
				})
			}
			if msg.Action != nil {
				actionStr := string(msg.Action.Type)
				if actionStr == string(cua.ActionClick) {
					actionStr = "left_click"
				}

				inputBlock := map[string]any{
					"action": actionStr,
				}
				if msg.Action.Text != "" {
					inputBlock["text"] = msg.Action.Text
				} else if msg.Action.Type == cua.ActionMouseMove || msg.Action.Type == cua.ActionClick || msg.Action.Type == cua.ActionRightClick || msg.Action.Type == cua.ActionMiddleClick || msg.Action.Type == cua.ActionDrag || msg.Action.Type == cua.ActionDoubleClick || msg.Action.Type == cua.ActionTripleClick || msg.Action.Type == cua.ActionLeftClickDrag {
					if msg.Action.X != nil && msg.Action.Y != nil {
						inputBlock["coordinate"] = []int{*msg.Action.X, *msg.Action.Y}
					}
				}
				if msg.Action.Duration != 0 {
					inputBlock["duration"] = msg.Action.Duration
				}
				if len(msg.Action.ZoomRegion) > 0 {
					region := make([]any, len(msg.Action.ZoomRegion))
					for i, v := range msg.Action.ZoomRegion {
						region[i] = v
					}
					inputBlock["zoom_region"] = region
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    msg.Action.ToolCallID,
					"name":  "computer",
					"input": inputBlock,
				})
			}
		}

		if len(blocks) > 0 {
			messages = append(messages, map[string]any{
				"role":    msg.Role,
				"content": blocks,
			})
		}
	}

	return messages, nil
}

// PredictAction takes the current conversation history and the latest visual state, returning the next predicted unified Action and assistant's text.
func (c *Client) PredictAction(ctx context.Context, history []cua.Message, state cua.VisualState) (*cua.Action, string, error) {
	// 1. Format history
	formattedHistory, err := c.FormatHistory(history)
	if err != nil {
		return nil, "", fmt.Errorf("formatting history: %w", err)
	}

	messagesSlice, ok := formattedHistory.([]map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("unexpected formatted history type")
	}

	// 2. Append the visual state if provided, to the LAST user message or as a new user message
	if state.ImageBase64 != "" {
		imageBlock := map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": "image/" + state.Format,
				"data":       state.ImageBase64,
			},
		}

		// Try to append to last user message
		appended := false
		for i := len(messagesSlice) - 1; i >= 0; i-- {
			if messagesSlice[i]["role"] == "user" {
				if content, ok := messagesSlice[i]["content"].([]map[string]any); ok {
					messagesSlice[i]["content"] = append(content, imageBlock)
					appended = true
					break
				}
			}
		}

		if !appended {
			messagesSlice = append(messagesSlice, map[string]any{
				"role":    "user",
				"content": []map[string]any{imageBlock},
			})
		}
	}

	// 3. Build payload
	payload := map[string]any{
		"model":      c.model,
		"max_tokens": 1024,
		"messages":   messagesSlice,
		"tools": []map[string]any{
			{
				"type":              "computer_20250124",
				"name":              "computer",
				"display_width_px":  c.config.WidthPX,
				"display_height_px": c.config.HeightPX,
				"display_number":    1,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling request: %w", err)
	}

	// 4. Send request
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("API error: %s", string(respBody))
	}

	// 5. Parse response
	var responseData struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			ID    string         `json:"id"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}

	if err := json.Unmarshal(respBody, &responseData); err != nil {
		return nil, "", fmt.Errorf("unmarshaling response: %w", err)
	}

	// 6. Map to cua.Action
	var actionBlock map[string]any
	var toolCallID string
	var assistantText string
	for _, block := range responseData.Content {
		if block.Type == "text" && block.Text != "" {
			assistantText += block.Text
		}
		if block.Type == "tool_use" && block.Name == "computer" {
			actionBlock = block.Input
			toolCallID = block.ID
			break
		}
	}

	if actionBlock == nil {
		return nil, "", fmt.Errorf("no computer tool use block found in response")
	}

	actionStr, _ := actionBlock["action"].(string)
	if actionStr == "left_click" {
		actionStr = string(cua.ActionClick)
	}

	action := &cua.Action{
		Type:       cua.ActionType(actionStr),
		ToolCallID: toolCallID,
	}

	// Parse specific fields based on anthropic inputs
	if coordsInterface, ok := actionBlock["coordinate"]; ok {
		action.HasCoordinate = true
		if coords, ok := coordsInterface.([]any); ok && len(coords) >= 2 {
			if xArr, ok := coords[0].(float64); ok {
				x := int(xArr)
				action.X = &x
			}
			if yArr, ok := coords[1].(float64); ok {
				y := int(yArr)
				action.Y = &y
			}
		}
	}

	if regionInterface, ok := actionBlock["zoom_region"]; ok {
		if region, ok := regionInterface.([]any); ok && len(region) == 4 {
			action.ZoomRegion = make([]int, 4)
			for i := 0; i < 4; i++ {
				if val, ok := region[i].(float64); ok {
					action.ZoomRegion[i] = int(val)
				}
			}
		}
	}

	if text, ok := actionBlock["text"].(string); ok {
		action.Text = text
	}

	if dur, ok := actionBlock["duration"].(float64); ok {
		action.Duration = dur
	}

	return action, assistantText, nil
}

// CallDirect makes a simple, non-agentic call to the model with a prompt and an optional image.
func (c *Client) CallDirect(ctx context.Context, prompt string, imageBase64 string, maxTokens int) (string, error) {
	var content []map[string]any

	if imageBase64 != "" {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": "image/png",
				"data":       imageBase64,
			},
		})
	}

	content = append(content, map[string]any{
		"type": "text",
		"text": prompt,
	})

	payload := map[string]any{
		"model":      c.model,
		"max_tokens": maxTokens,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": content,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error: %s", string(respBody))
	}

	var responseData struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal(respBody, &responseData); err != nil {
		return "", fmt.Errorf("unmarshaling response: %w", err)
	}

	for _, block := range responseData.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}

	return "", fmt.Errorf("no text returned from model")
}
