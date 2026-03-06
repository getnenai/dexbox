package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const DefaultBaseURL = "https://api.anthropic.com"

// Client is a lightweight HTTP client for the Anthropic Messages API.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func NewClient(apiKey string, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		HTTPClient: &http.Client{},
	}
}

// ---------------------------------------------------------------------------
// Request Types
// ---------------------------------------------------------------------------

type CreateMessageRequest struct {
	Model       string         `json:"model"`
	MaxTokens   int            `json:"max_tokens"`
	System      []ContentBlock `json:"system,omitempty"`
	Messages    []Message      `json:"messages"`
	Tools       []Tool         `json:"tools,omitempty"`
	ToolChoice  *ToolChoice    `json:"tool_choice,omitempty"`
	Temperature float32        `json:"temperature,omitempty"`
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`    // For tool_use
	Name  string `json:"name,omitempty"`  // For tool_use
	Input any    `json:"input,omitempty"` // For tool_use

	Source *ImageSource `json:"source,omitempty"` // For image

	ToolUseID string `json:"tool_use_id,omitempty"` // For tool_result
	IsError   bool   `json:"is_error,omitempty"`    // For tool_result
	Content   any    `json:"content,omitempty"`     // For tool_result (can be string or []ContentBlock)

	CacheControl *CacheControl `json:"cache_control,omitempty"` // For prompt caching
}

type CacheControl struct {
	Type string `json:"type"`
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type Tool struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	InputSchema *InputSchema `json:"input_schema,omitempty"`

	// For computer use beta specific
	Type            string `json:"type,omitempty"` // e.g. "computer_20241022"
	DisplayWidthPx  int    `json:"display_width_px,omitempty"`
	DisplayHeightPx int    `json:"display_height_px,omitempty"`
	DisplayNumber   int    `json:"display_number,omitempty"`
}

type InputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Required   []string               `json:"required,omitempty"`
}

type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// ---------------------------------------------------------------------------
// Response Types
// ---------------------------------------------------------------------------

type CreateMessageResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	StopReason   string         `json:"stop_reason"`
	StopSequence string         `json:"stop_sequence,omitempty"`
	Usage        Usage          `json:"usage"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

// ---------------------------------------------------------------------------
// API Methods
// ---------------------------------------------------------------------------

func (c *Client) CreateMessage(ctx context.Context, req *CreateMessageRequest, betas []string) (*CreateMessageResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1/messages", c.BaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	for _, beta := range betas {
		httpReq.Header.Add("anthropic-beta", beta)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if jsonErr := json.Unmarshal(respBody, &errResp); jsonErr == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic api error %d: [%s] %s", resp.StatusCode, errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("anthropic request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var messageResp CreateMessageResponse
	if err := json.Unmarshal(respBody, &messageResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &messageResp, nil
}
