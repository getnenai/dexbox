// Package mcpserver exposes Dexbox desktop lifecycle and action tools as MCP tools.
//
// Each tool handler is a thin HTTP call to the Dexbox server (default
// localhost:8600). The MCP server runs over stdio so that IDE AI assistants
// (Cursor, Claude Code, etc.) can manage and control desktops directly.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Tool input structs ------------------------------------------------

type listDesktopsInput struct {
	Type string `json:"type,omitempty" jsonschema:"Filter by desktop type: vm or rdp. Omit to list all."`
}

type createDesktopInput struct {
	Name     string `json:"name" jsonschema:"Unique name for the desktop"`
	Type     string `json:"type" jsonschema:"Desktop type (currently only rdp is supported via API)"`
	Host     string `json:"host" jsonschema:"RDP host address"`
	Port     int    `json:"port,omitempty" jsonschema:"RDP port (default 3389)"`
	Username string `json:"username" jsonschema:"RDP username"`
	Password string `json:"password" jsonschema:"RDP password"`
}

type desktopNameInput struct {
	Name string `json:"name" jsonschema:"Desktop name"`
}

type stopDesktopInput struct {
	Name  string `json:"name" jsonschema:"Desktop name"`
	Force bool   `json:"force,omitempty" jsonschema:"Hard poweroff instead of graceful ACPI shutdown (VM only)"`
}

// --- Action tool input structs ------------------------------------------

type screenshotInput struct {
	Desktop string `json:"desktop,omitempty" jsonschema:"Desktop name. Omit if only one desktop is connected."`
}

type clickInput struct {
	Desktop    string `json:"desktop,omitempty" jsonschema:"Desktop name. Omit if only one desktop is connected."`
	X          int    `json:"x" jsonschema:"X coordinate"`
	Y          int    `json:"y" jsonschema:"Y coordinate"`
	Button     string `json:"button,omitempty" jsonschema:"Mouse button: left (default), right, middle, double"`
}

type typeTextInput struct {
	Desktop string `json:"desktop,omitempty" jsonschema:"Desktop name. Omit if only one desktop is connected."`
	Text    string `json:"text" jsonschema:"Text to type"`
}

type keyPressInput struct {
	Desktop string `json:"desktop,omitempty" jsonschema:"Desktop name. Omit if only one desktop is connected."`
	Key     string `json:"key" jsonschema:"Key or combo to press (e.g. enter, ctrl+c, alt+tab)"`
}

type scrollInput struct {
	Desktop   string `json:"desktop,omitempty" jsonschema:"Desktop name. Omit if only one desktop is connected."`
	X         int    `json:"x" jsonschema:"X coordinate to scroll at"`
	Y         int    `json:"y" jsonschema:"Y coordinate to scroll at"`
	Direction string `json:"direction,omitempty" jsonschema:"Scroll direction: up or down (default: down)"`
	Amount    int    `json:"amount,omitempty" jsonschema:"Scroll amount in lines (default: 3)"`
}

type bashInput struct {
	Desktop string `json:"desktop,omitempty" jsonschema:"Desktop name. Omit if only one desktop is connected."`
	Command string `json:"command" jsonschema:"PowerShell command to execute in the guest VM"`
}

// --- Helpers -----------------------------------------------------------

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

func imageResult(pngBytes []byte) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.ImageContent{
			Data:     pngBytes,
			MIMEType: "image/png",
		}},
	}
}

// httpClient is used for all outbound requests to the Dexbox server.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// doRequest performs an HTTP request against the Dexbox server and returns
// the response body as a string. Non-2xx status codes are treated as errors.
func doRequest(ctx context.Context, baseURL, method, path string, body any) (string, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reqBody)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dexbox server unreachable at %s (is 'dexbox start' running?): %w", baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("dexbox API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return string(respBody), nil
}

// doActionRaw sends a tool action to POST /actions and returns the raw response
// bytes. The model=claude-mcp prefix routes through the Anthropic adapter which
// uses the simplest wire format. The desktop query param enables per-desktop routing.
func doActionRaw(ctx context.Context, baseURL, desktop string, body map[string]any, accept string) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal action body: %w", err)
	}

	qs := url.Values{"model": {"claude-mcp"}}
	if desktop != "" {
		qs.Set("desktop", desktop)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/actions?"+qs.Encode(), strings.NewReader(string(b)))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dexbox server unreachable at %s (is 'dexbox start' running?): %w", baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("dexbox API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// --- Server factory ----------------------------------------------------

// empty is used for tools that return only *mcp.CallToolResult (no typed output).
type empty struct{}

// New creates an MCP server with all desktop management tools registered.
// baseURL is the Dexbox HTTP API base (e.g. "http://localhost:8600").
func New(baseURL string) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "dexbox", Version: "1.0.0"},
		nil,
	)

	// list_desktops
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_desktops",
		Description: "List all Dexbox desktops (VMs and RDP connections) with their current state.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input listDesktopsInput) (*mcp.CallToolResult, empty, error) {
		path := "/desktops"
		if input.Type != "" {
			path += "?type=" + url.QueryEscape(input.Type)
		}
		body, err := doRequest(ctx, baseURL, http.MethodGet, path, nil)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	// create_desktop
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_desktop",
		Description: "Register a new RDP desktop connection.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input createDesktopInput) (*mcp.CallToolResult, empty, error) {
		body, err := doRequest(ctx, baseURL, http.MethodPost, "/desktops", input)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	// destroy_desktop
	mcp.AddTool(server, &mcp.Tool{
		Name:        "destroy_desktop",
		Description: "Destroy a desktop (delete VM or unregister RDP connection).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input desktopNameInput) (*mcp.CallToolResult, empty, error) {
		body, err := doRequest(ctx, baseURL, http.MethodDelete, "/desktops/"+url.PathEscape(input.Name), nil)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	// start_desktop
	mcp.AddTool(server, &mcp.Tool{
		Name:        "start_desktop",
		Description: "Bring a desktop online (boot VM or connect RDP session).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input desktopNameInput) (*mcp.CallToolResult, empty, error) {
		body, err := doRequest(ctx, baseURL, http.MethodPost, "/desktops/"+url.PathEscape(input.Name)+"?action=up", nil)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	// stop_desktop
	mcp.AddTool(server, &mcp.Tool{
		Name:        "stop_desktop",
		Description: "Disconnect a desktop session and shut down the VM guest OS.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input stopDesktopInput) (*mcp.CallToolResult, empty, error) {
		qs := url.Values{"action": {"down"}, "shutdown": {"true"}}
		if input.Force {
			qs.Set("force", "true")
		}
		path := "/desktops/" + url.PathEscape(input.Name) + "?" + qs.Encode()
		body, err := doRequest(ctx, baseURL, http.MethodPost, path, nil)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	// status_desktop
	mcp.AddTool(server, &mcp.Tool{
		Name:        "status_desktop",
		Description: "Get the current status of a single desktop.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input desktopNameInput) (*mcp.CallToolResult, empty, error) {
		body, err := doRequest(ctx, baseURL, http.MethodGet, "/desktops/"+url.PathEscape(input.Name), nil)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	// --- Action tools (computer-use) ------------------------------------

	// screenshot
	mcp.AddTool(server, &mcp.Tool{
		Name:        "screenshot",
		Description: "Take a screenshot of the desktop. Returns a PNG image.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input screenshotInput) (*mcp.CallToolResult, empty, error) {
		pngBytes, err := doActionRaw(ctx, baseURL, input.Desktop, map[string]any{
			"type":   "computer_20250124",
			"action": "screenshot",
		}, "image/png")
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return imageResult(pngBytes), empty{}, nil
	})

	// click
	mcp.AddTool(server, &mcp.Tool{
		Name:        "click",
		Description: "Click at a coordinate on the desktop. Use button to specify left (default), right, middle, or double click.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input clickInput) (*mcp.CallToolResult, empty, error) {
		var action string
		switch input.Button {
		case "", "left":
			action = "left_click"
		case "right":
			action = "right_click"
		case "middle":
			action = "middle_click"
		case "double":
			action = "double_click"
		default:
			return errorResult(fmt.Sprintf("unsupported button %q; use left, right, middle, or double", input.Button)), empty{}, nil
		}
		_, err := doActionRaw(ctx, baseURL, input.Desktop, map[string]any{
			"type":       "computer_20250124",
			"action":     action,
			"coordinate": []int{input.X, input.Y},
		}, "")
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(fmt.Sprintf("clicked (%d, %d)", input.X, input.Y)), empty{}, nil
	})

	// type_text
	mcp.AddTool(server, &mcp.Tool{
		Name:        "type_text",
		Description: "Type text on the desktop. Click on the target input field first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input typeTextInput) (*mcp.CallToolResult, empty, error) {
		_, err := doActionRaw(ctx, baseURL, input.Desktop, map[string]any{
			"type":   "computer_20250124",
			"action": "type",
			"text":   input.Text,
		}, "")
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult("typed text"), empty{}, nil
	})

	// key_press
	mcp.AddTool(server, &mcp.Tool{
		Name:        "key_press",
		Description: "Press a key or key combo (e.g. enter, ctrl+c, alt+tab, shift+F5).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input keyPressInput) (*mcp.CallToolResult, empty, error) {
		_, err := doActionRaw(ctx, baseURL, input.Desktop, map[string]any{
			"type":   "computer_20250124",
			"action": "key",
			"text":   input.Key,
		}, "")
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(fmt.Sprintf("pressed %s", input.Key)), empty{}, nil
	})

	// scroll
	mcp.AddTool(server, &mcp.Tool{
		Name:        "scroll",
		Description: "Scroll at a coordinate on the desktop.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input scrollInput) (*mcp.CallToolResult, empty, error) {
		body := map[string]any{
			"type":       "computer_20250124",
			"action":     "scroll",
			"coordinate": []int{input.X, input.Y},
		}
		if input.Direction != "" {
			body["direction"] = input.Direction
		}
		if input.Amount > 0 {
			body["amount"] = input.Amount
		}
		_, err := doActionRaw(ctx, baseURL, input.Desktop, body, "")
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult("scrolled"), empty{}, nil
	})

	// bash
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bash",
		Description: "Run a PowerShell command on the desktop guest OS. Returns the command output.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input bashInput) (*mcp.CallToolResult, empty, error) {
		respBytes, err := doActionRaw(ctx, baseURL, input.Desktop, map[string]any{
			"type":    "bash_20250124",
			"command": input.Command,
		}, "")
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		// Parse the Anthropic-format response to extract the output text.
		var resp struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return textResult(string(respBytes)), empty{}, nil
		}
		return textResult(resp.Output), empty{}, nil
	})

	return server
}
