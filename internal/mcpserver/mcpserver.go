// Package mcpserver exposes Dexbox desktop lifecycle operations as MCP tools.
//
// Each tool handler is a thin HTTP call to the Dexbox server (default
// localhost:8600). The MCP server runs over stdio so that IDE AI assistants
// (Cursor, Claude Code, etc.) can manage desktops directly.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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

// doRequest performs an HTTP request against the Dexbox server and returns
// the response body as a string.
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dexbox server unreachable at %s (is 'dexbox start' running?): %w", baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	return string(respBody), nil
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
			path += "?type=" + input.Type
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
		body, err := doRequest(ctx, baseURL, http.MethodDelete, "/desktops/"+input.Name, nil)
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
		body, err := doRequest(ctx, baseURL, http.MethodPost, "/desktops/"+input.Name+"?action=up", nil)
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
		path := "/desktops/" + input.Name + "?action=down&shutdown=true"
		if input.Force {
			path += "&force=true"
		}
		body, err := doRequest(ctx, baseURL, http.MethodPost, path, nil)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	// get_desktop
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_desktop",
		Description: "Get the current status of a single desktop.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input desktopNameInput) (*mcp.CallToolResult, empty, error) {
		body, err := doRequest(ctx, baseURL, http.MethodGet, "/desktops/"+input.Name, nil)
		if err != nil {
			return errorResult(err.Error()), empty{}, nil
		}
		return textResult(body), empty{}, nil
	})

	return server
}
