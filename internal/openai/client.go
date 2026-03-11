package openai

import (
	"context"
	"errors"
	"fmt"
	"log"

	"strings"

	"github.com/getnenai/dexbox/pkg/cua"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// Client implements cua.Provider for OpenAI's Responses API.
type Client struct {
	client          *openai.Client
	model           string
	config          cua.DisplayConfig
	bufferedActions []*cua.Action
}

// NewClient creates a new OpenAI provider client.
func NewClient(apiKey, model, baseURL string) *Client {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(3),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	cl := openai.NewClient(opts...)
	return &Client{
		client: &cl,
		model:  model,
	}
}

// Setup initializes the display configuration.
func (c *Client) Setup(config cua.DisplayConfig) error {
	c.config = config
	return nil
}

// NeedsVisuals returns true if we don't have any buffered actions from a previous batch.
func (c *Client) NeedsVisuals() bool {
	return len(c.bufferedActions) == 0
}

// FormatHistory translates cua.Message interaction history into the OpenAI SDK inputs.
// It returns an array of response items for Responses API.
func (c *Client) FormatHistory(history []cua.Message) (any, error) {
	var inputs []responses.ResponseInputItemUnionParam

	// Map to keep track of tool calls so we don't duplicate computer calls
	// that were batched by the model but sequentially processed by the agent.
	seenCalls := make(map[string]bool)

	for _, msg := range history {
		if msg.Role == "user" && msg.Content != "" {
			inputs = append(inputs, responses.ResponseInputItemParamOfMessage(
				msg.Content,
				responses.EasyInputMessageRoleUser,
			))
		}

		if msg.Role == "assistant" && msg.Content != "" {
			inputs = append(inputs, responses.ResponseInputItemParamOfMessage(
				msg.Content,
				responses.EasyInputMessageRoleAssistant,
			))
		}

		if msg.Role == "assistant" && msg.Action != nil {
			if !seenCalls[msg.Action.ToolCallID] {
				seenCalls[msg.Action.ToolCallID] = true

				var itemID, callID string
				parts := strings.Split(msg.Action.ToolCallID, "|")
				if len(parts) == 2 {
					itemID = parts[0]
					callID = parts[1]
				} else {
					itemID = "cu_" + msg.Action.ToolCallID
					callID = msg.Action.ToolCallID
				}

				// Find all actions in history that belong to this batched tool call
				var batchedActions []responses.ComputerActionUnionParam
				for _, hMsg := range history {
					if hMsg.Role == "assistant" && hMsg.Action != nil && hMsg.Action.ToolCallID == msg.Action.ToolCallID {
						var actionParam responses.ComputerActionUnionParam
						switch hMsg.Action.Type {
						case cua.ActionClick, cua.ActionRightClick, cua.ActionMiddleClick:
							btn := "left"
							switch hMsg.Action.Type {
							case cua.ActionRightClick:
								btn = "right"
							case cua.ActionMiddleClick:
								btn = "middle"
							}
							x, y := int64(0), int64(0)
							if hMsg.Action.X != nil {
								x = int64(*hMsg.Action.X)
							}
							if hMsg.Action.Y != nil {
								y = int64(*hMsg.Action.Y)
							}
							actionParam = responses.ComputerActionParamOfClick(btn, x, y)
						case cua.ActionDoubleClick:
							x, y := int64(0), int64(0)
							if hMsg.Action.X != nil {
								x = int64(*hMsg.Action.X)
							}
							if hMsg.Action.Y != nil {
								y = int64(*hMsg.Action.Y)
							}
							actionParam = responses.ComputerActionParamOfDoubleClick(x, y)
						case cua.ActionTypeString:
							actionParam = responses.ComputerActionParamOfType(hMsg.Action.Text)
						case cua.ActionKey:
							actionParam = responses.ComputerActionParamOfKeypress([]string{hMsg.Action.Text})
						case cua.ActionMouseMove:
							x, y := int64(0), int64(0)
							if hMsg.Action.X != nil {
								x = int64(*hMsg.Action.X)
							}
							if hMsg.Action.Y != nil {
								y = int64(*hMsg.Action.Y)
							}
							actionParam = responses.ComputerActionParamOfMove(x, y)
						case cua.ActionDrag, cua.ActionLeftClickDrag:
							x, y := int64(0), int64(0)
							if hMsg.Action.X != nil {
								x = int64(*hMsg.Action.X)
							}
							if hMsg.Action.Y != nil {
								y = int64(*hMsg.Action.Y)
							}
							actionParam = responses.ComputerActionParamOfDrag([]responses.ComputerActionDragPathParam{{X: x, Y: y}})
						case cua.ActionScroll:
							x, y := int64(0), int64(0)
							if hMsg.Action.X != nil {
								x = int64(*hMsg.Action.X)
							}
							if hMsg.Action.Y != nil {
								y = int64(*hMsg.Action.Y)
							}
							actionParam = responses.ComputerActionUnionParam{
								OfScroll: &responses.ComputerActionScrollParam{
									X:       x,
									Y:       y,
									ScrollX: int64(hMsg.Action.ScrollDeltaX),
									ScrollY: int64(hMsg.Action.ScrollDeltaY),
								},
							}
						case cua.ActionScreenshot:
							actionParam = responses.ComputerActionUnionParam{OfScreenshot: openai.Ptr(responses.NewComputerActionScreenshotParam())}
						case cua.ActionWait:
							actionParam = responses.ComputerActionUnionParam{OfWait: openai.Ptr(responses.NewComputerActionWaitParam())}
						default:
							// ActionTripleClick, ActionHoldKey, ActionZoom, ActionCursorPosition,
							// ActionLeftMouseDown, ActionLeftMouseUp have no OpenAI wire equivalent.
							return nil, fmt.Errorf("FormatHistory: unsupported action type %q for OpenAI provider", hMsg.Action.Type)
						}
						batchedActions = append(batchedActions, actionParam)
					}
				}

				inputs = append(inputs, responses.ResponseInputItemUnionParam{
					OfComputerCall: &responses.ResponseComputerToolCallParam{
						ID:                  itemID,
						CallID:              callID,
						Type:                responses.ResponseComputerToolCallTypeComputerCall,
						Status:              responses.ResponseComputerToolCallStatus("completed"),
						Actions:             batchedActions,
						PendingSafetyChecks: []responses.ResponseComputerToolCallPendingSafetyCheckParam{},
					},
				})
			}
		}

		if msg.Role == "user" && msg.Result != "" {
			var callID string
			parts := strings.Split(msg.ToolCallID, "|")
			if len(parts) == 2 {
				callID = parts[1]
			} else {
				callID = msg.ToolCallID
			}

			// In OpenAI API, there is only one output per tool call.
			// We only append it if we haven't already for this ToolCallID.
			if !seenCalls["output_"+callID] {
				seenCalls["output_"+callID] = true
				inputs = append(inputs, responses.ResponseInputItemParamOfComputerCallOutput(
					callID,
					responses.ResponseComputerToolCallOutputScreenshotParam{
						ImageURL: openai.String("data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="),
					},
				))
			}

			// Append the tool result text as a user message to preserve the signal
			inputs = append(inputs, responses.ResponseInputItemParamOfMessage(
				"Action Result: "+msg.Result,
				responses.EasyInputMessageRoleUser,
			))
		}
	}

	return inputs, nil
}

// PredictAction takes the current conversation history and the latest visual state, returning the next predicted Action.
func (c *Client) PredictAction(ctx context.Context, history []cua.Message, state cua.VisualState) (*cua.Action, string, error) {
	// 0. Process buffered actions first
	if len(c.bufferedActions) > 0 {
		action := c.bufferedActions[0]
		c.bufferedActions = c.bufferedActions[1:]
		log.Printf("[OpenAI] Using buffered action: %s", action.Type)
		return action, "", nil
	}

	// 1. Format history
	formattedHistory, err := c.FormatHistory(history)
	if err != nil {
		return nil, "", fmt.Errorf("formatting history: %w", err)
	}

	inputsSlice, ok := formattedHistory.([]responses.ResponseInputItemUnionParam)
	if !ok {
		return nil, "", fmt.Errorf("unexpected formatted history type")
	}

	// 2. Add screenshot to current input if provided
	if state.ImageBase64 != "" {
		contentList := responses.ResponseInputMessageContentListParam{
			{
				OfInputImage: &responses.ResponseInputImageParam{
					Detail:   responses.ResponseInputImageDetailAuto,
					ImageURL: openai.String("data:image/" + state.Format + ";base64," + state.ImageBase64),
				},
			},
		}
		inputsSlice = append(inputsSlice, responses.ResponseInputItemParamOfMessage(
			contentList,
			responses.EasyInputMessageRoleUser,
		))
	}

	// 3. Build payload param
	params := responses.ResponseNewParams{
		Model: openai.ResponsesModel(c.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputsSlice,
		},
		Tools: []responses.ToolUnionParam{
			{
				OfComputer: openai.Ptr(responses.NewComputerToolParam()),
			},
		},
	}

	// 4. Send request
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return nil, "", fmt.Errorf("API error: %s", apiErr.Message)
		}
		return nil, "", fmt.Errorf("api error: %w", err)
	}

	// 5. Parse response
	var actions []responses.ComputerActionUnion
	var assistantText string

	var toolCallID string

	for _, outputItem := range resp.Output {
		if outputItem.Type == "message" {
			msg := outputItem.AsMessage()
			for _, content := range msg.Content {
				if content.Type == "text" {
					assistantText += content.Text
				}
			}
		}
		if outputItem.Type == "computer_call" {
			computerCall := outputItem.AsComputerCall()
			actions = computerCall.Actions
			toolCallID = computerCall.ID + "|" + computerCall.CallID
			break
		}
	}

	if len(actions) == 0 {
		if assistantText != "" {
			return nil, assistantText, nil
		}
		return nil, "", fmt.Errorf("no computer tool use block found in response")
	}

	// 6. Map to cua.Action
	for _, rawAction := range actions {
		if rawAction.Type == "" {
			continue
		}

		action := &cua.Action{
			Type:       cua.ActionType(rawAction.Type),
			ToolCallID: toolCallID,
		}

		switch rawAction.Type {
		case "move":
			action.Type = cua.ActionMouseMove
		case "keypress":
			action.Type = cua.ActionKey
		}

		if rawAction.Text != "" {
			action.Text = rawAction.Text
		}

		if len(rawAction.Keys) > 0 {
			action.Text = strings.Join(rawAction.Keys, "+")
		}

		if rawAction.X != 0 || rawAction.Type == "click" || rawAction.Type == "move" {
			action.HasCoordinate = true
			x := int(rawAction.X)
			action.X = &x
		}
		if rawAction.Y != 0 || rawAction.Type == "click" || rawAction.Type == "move" {
			action.HasCoordinate = true
			y := int(rawAction.Y)
			action.Y = &y
		}

		if rawAction.ScrollY != 0 {
			action.ScrollDeltaY = int(rawAction.ScrollY)
		}
		if rawAction.ScrollX != 0 {
			action.ScrollDeltaX = int(rawAction.ScrollX)
		}

		if rawAction.Button != "" && action.Type == "click" {
			switch rawAction.Button {
			case "right":
				action.Type = cua.ActionRightClick
			case "middle":
				action.Type = cua.ActionMiddleClick
			}
		}

		c.bufferedActions = append(c.bufferedActions, action)
	}

	log.Printf("[OpenAI] Received %d actions in batch.", len(c.bufferedActions))

	action := c.bufferedActions[0]
	c.bufferedActions = c.bufferedActions[1:]
	return action, assistantText, nil
}

// CallDirect sends a prompt and an optional base64 image directly to the model, returning the text response.
func (c *Client) CallDirect(ctx context.Context, prompt, imageB64 string, maxTokens int) (string, error) {
	content := []openai.ChatCompletionContentPartUnionParam{
		openai.TextContentPart(prompt),
	}

	if imageB64 != "" {
		content = append(content, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
			URL: "data:image/png;base64," + imageB64,
		}))
	}

	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(c.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(content),
		},
	}
	if maxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(maxTokens))
	}

	resp, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return "", fmt.Errorf("API error: %s", apiErr.Message)
		}
		return "", fmt.Errorf("api error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from model")
	}

	return resp.Choices[0].Message.Content, nil
}
