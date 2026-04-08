package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mockDexboxAPI returns an httptest.Server that mimics the Dexbox HTTP API.
func mockDexboxAPI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		// GET /desktops
		case r.Method == http.MethodGet && r.URL.Path == "/desktops":
			json.NewEncoder(w).Encode(map[string]any{
				"desktops": []map[string]any{
					{"name": "win11", "type": "vm", "state": "running", "connected": true},
					{"name": "my-rdp", "type": "rdp", "state": "connected", "connected": true},
				},
			})

		// POST /actions (action tools)
		case r.Method == http.MethodPost && r.URL.Path == "/actions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("/actions: decode body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			toolType, _ := body["type"].(string)
			action, _ := body["action"].(string)
			desktop := r.URL.Query().Get("desktop")

			switch {
			case toolType == "computer_20250124" && action == "screenshot":
				if r.Header.Get("Accept") == "image/png" {
					w.Header().Set("Content-Type", "image/png")
					w.Write([]byte("FAKEPNG"))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"base64_image": "AAAA"})
			case toolType == "bash_20250124":
				json.NewEncoder(w).Encode(map[string]any{"output": "command output here", "desktop": desktop})
			case toolType == "text_editor_20250124":
				command, _ := body["command"].(string)
				path, _ := body["path"].(string)
				json.NewEncoder(w).Encode(map[string]any{
					"type":    "text_editor_20250124",
					"output":  fmt.Sprintf("editor %s on %s", command, path),
					"desktop": desktop,
				})
			case toolType == "computer_20250124":
				json.NewEncoder(w).Encode(map[string]any{"status": "ok", "action": action, "desktop": desktop})
			default:
				t.Errorf("/actions: unexpected type=%q action=%q", toolType, action)
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"error": "unexpected request"})
			}

		// POST /desktops (create)
		case r.Method == http.MethodPost && r.URL.Path == "/desktops":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"name": body["name"],
				"type": body["type"],
				"host": fmt.Sprintf("%s:3389", body["host"]),
			})

		// DELETE /desktops/{name}
		case r.Method == http.MethodDelete:
			name := r.URL.Path[len("/desktops/"):]
			json.NewEncoder(w).Encode(map[string]any{
				"name": name, "type": "rdp", "state": "deleted",
			})

		// POST /desktops/{name}?action=...
		case r.Method == http.MethodPost:
			name := r.URL.Path[len("/desktops/"):]
			action := r.URL.Query().Get("action")
			json.NewEncoder(w).Encode(map[string]any{
				"name": name, "status": action,
			})

		// GET /desktops/{name}
		case r.Method == http.MethodGet:
			name := r.URL.Path[len("/desktops/"):]
			json.NewEncoder(w).Encode(map[string]any{
				"name": name, "type": "vm", "state": "running", "connected": true,
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"error": "not_found"})
		}
	}))
}

// callTool invokes an MCP tool on the server and returns the text content.
func callTool(t *testing.T, srv *mcp.Server, name string, args map[string]any) string {
	t.Helper()
	ctx := context.Background()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("call %s: no content returned", name)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("call %s: expected TextContent, got %T", name, result.Content[0])
	}
	return tc.Text
}

func TestListDesktops(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "list_desktops", nil)

	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	desktops, ok := resp["desktops"].([]any)
	if !ok || len(desktops) != 2 {
		t.Fatalf("expected 2 desktops, got %v", resp)
	}
}

func TestCreateDesktop(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "create_desktop", map[string]any{
		"name":     "new-rdp",
		"type":     "rdp",
		"host":     "10.0.0.5",
		"username": "admin",
		"password": "secret",
	})

	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["name"] != "new-rdp" {
		t.Errorf("expected name 'new-rdp', got %v", resp["name"])
	}
}

func TestDestroyDesktop(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "destroy_desktop", map[string]any{"name": "old-vm"})

	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["name"] != "old-vm" {
		t.Errorf("expected name 'old-vm', got %v", resp["name"])
	}
	if resp["state"] != "deleted" {
		t.Errorf("expected state 'deleted', got %v", resp["state"])
	}
}

func TestStartDesktop(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "start_desktop", map[string]any{"name": "win11"})

	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["name"] != "win11" {
		t.Errorf("expected name 'win11', got %v", resp["name"])
	}
	if resp["status"] != "up" {
		t.Errorf("expected status 'up', got %v", resp["status"])
	}
}

func TestStopDesktop(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "stop_desktop", map[string]any{
		"name": "win11",
	})

	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["name"] != "win11" {
		t.Errorf("expected name 'win11', got %v", resp["name"])
	}
	if resp["status"] != "down" {
		t.Errorf("expected status 'down', got %v", resp["status"])
	}
}

func TestGetDesktop(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "status_desktop", map[string]any{"name": "win11"})

	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["name"] != "win11" {
		t.Errorf("expected name 'win11', got %v", resp["name"])
	}
	if resp["state"] != "running" {
		t.Errorf("expected state 'running', got %v", resp["state"])
	}
}

func TestHTTPErrorPropagation(t *testing.T) {
	// API that always returns 404
	errAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": "not_found", "message": "desktop does not exist"})
	}))
	defer errAPI.Close()

	srv := New(errAPI.URL)

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "status_desktop",
		Arguments: map[string]any{"name": "nonexistent"},
	})
	if err != nil {
		t.Fatalf("call status_desktop: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError to be true for a 404 response")
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(tc.Text, "404") {
		t.Errorf("expected error text to mention 404, got: %s", tc.Text)
	}
}

func TestServerUnreachable(t *testing.T) {
	// Point at a port that nothing is listening on
	srv := New("http://localhost:19999")
	text := callTool(t, srv, "list_desktops", nil)

	// Should get an error result, not a test crash
	if text == "" {
		t.Fatal("expected error message, got empty string")
	}
}

// callToolResult invokes an MCP tool and returns the raw CallToolResult.
func callToolResult(t *testing.T, srv *mcp.Server, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("call %s: no content returned", name)
	}
	return result
}

func TestScreenshot(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	result := callToolResult(t, srv, "screenshot", nil)

	img, ok := result.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("expected ImageContent, got %T", result.Content[0])
	}
	if img.MIMEType != "image/png" {
		t.Errorf("expected image/png, got %s", img.MIMEType)
	}
	if string(img.Data) != "FAKEPNG" {
		t.Errorf("expected image data %q, got %q", "FAKEPNG", string(img.Data))
	}
}

func TestClick(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "click", map[string]any{
		"x": 100, "y": 200,
	})

	if !strings.Contains(text, "clicked") {
		t.Errorf("expected 'clicked' in result, got: %s", text)
	}
}

func TestClickRightButton(t *testing.T) {
	var gotAction string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/actions" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			gotAction, _ = body["action"].(string)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "click", map[string]any{
		"x": 100, "y": 200, "button": "right",
	})

	if !strings.Contains(text, "clicked") {
		t.Errorf("expected 'clicked' in result, got: %s", text)
	}
	if gotAction != "right_click" {
		t.Errorf("expected action %q sent to server, got %q", "right_click", gotAction)
	}
}

func TestTypeText(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "type_text", map[string]any{
		"text": "hello world",
	})

	if !strings.Contains(text, "typed") {
		t.Errorf("expected 'typed' in result, got: %s", text)
	}
}

func TestKeyPress(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "key_press", map[string]any{
		"key": "ctrl+c",
	})

	if !strings.Contains(text, "ctrl+c") {
		t.Errorf("expected 'ctrl+c' in result, got: %s", text)
	}
}

func TestScroll(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "scroll", map[string]any{
		"x": 500, "y": 300, "direction": "down", "amount": 5,
	})

	if !strings.Contains(text, "scrolled") {
		t.Errorf("expected 'scrolled' in result, got: %s", text)
	}
}

func TestBash(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "bash", map[string]any{
		"command": "Get-Process",
	})

	if !strings.Contains(text, "command output here") {
		t.Errorf("expected bash output, got: %s", text)
	}
}

func TestScreenshotWithDesktop(t *testing.T) {
	var gotDesktop string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/actions" {
			gotDesktop = r.URL.Query().Get("desktop")
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("FAKEPNG"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()

	srv := New(api.URL)
	result := callToolResult(t, srv, "screenshot", map[string]any{
		"desktop": "win11",
	})

	img, ok := result.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("expected ImageContent, got %T", result.Content[0])
	}
	if string(img.Data) != "FAKEPNG" {
		t.Errorf("expected image data %q, got %q", "FAKEPNG", string(img.Data))
	}
	if gotDesktop != "win11" {
		t.Errorf("expected desktop param %q, got %q", "win11", gotDesktop)
	}
}

func TestClickWithDesktop(t *testing.T) {
	var gotDesktop, gotAction string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/actions" {
			gotDesktop = r.URL.Query().Get("desktop")
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			gotAction, _ = body["action"].(string)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "click", map[string]any{
		"desktop": "my-rdp", "x": 50, "y": 60,
	})
	if !strings.Contains(text, "clicked") {
		t.Errorf("expected 'clicked' in result, got: %s", text)
	}
	if gotDesktop != "my-rdp" {
		t.Errorf("expected desktop param %q, got %q", "my-rdp", gotDesktop)
	}
	if gotAction != "left_click" {
		t.Errorf("expected action %q, got %q", "left_click", gotAction)
	}
}

func TestBashWithDesktop(t *testing.T) {
	var gotDesktop, gotCommand string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/actions" {
			gotDesktop = r.URL.Query().Get("desktop")
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			gotCommand, _ = body["command"].(string)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"output": "command output here"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "bash", map[string]any{
		"desktop": "win11",
		"command": "whoami",
	})
	if !strings.Contains(text, "command output here") {
		t.Errorf("expected bash output, got: %s", text)
	}
	if gotDesktop != "win11" {
		t.Errorf("expected desktop param %q, got %q", "win11", gotDesktop)
	}
	if gotCommand != "whoami" {
		t.Errorf("expected command %q, got %q", "whoami", gotCommand)
	}
}

func TestViewFile(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "view_file", map[string]any{
		"path": `C:\Users\dexbox\file.txt`,
	})
	if !strings.Contains(text, "editor view") {
		t.Errorf("expected editor output, got: %s", text)
	}
}

func TestCreateFile(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "create_file", map[string]any{
		"path":      `C:\Users\dexbox\new.txt`,
		"file_text": "hello world",
	})
	if !strings.Contains(text, "editor create") {
		t.Errorf("expected editor output, got: %s", text)
	}
}

func TestStrReplace(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "str_replace", map[string]any{
		"path":    `C:\Users\dexbox\file.txt`,
		"old_str": "hello",
		"new_str": "world",
	})
	if !strings.Contains(text, "editor str_replace") {
		t.Errorf("expected editor output, got: %s", text)
	}
}

func TestInsertText(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "insert_text", map[string]any{
		"path":        `C:\Users\dexbox\file.txt`,
		"insert_line": 5,
		"new_str":     "inserted line",
	})
	if !strings.Contains(text, "editor insert") {
		t.Errorf("expected editor output, got: %s", text)
	}
}

func TestUndoEdit(t *testing.T) {
	api := mockDexboxAPI(t)
	defer api.Close()

	srv := New(api.URL)
	text := callTool(t, srv, "undo_edit", map[string]any{
		"path": `C:\Users\dexbox\file.txt`,
	})
	if !strings.Contains(text, "editor undo_edit") {
		t.Errorf("expected editor output, got: %s", text)
	}
}
