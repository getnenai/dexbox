package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		"name":     "win11",
		"shutdown": true,
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
	text := callTool(t, srv, "get_desktop", map[string]any{"name": "win11"})

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

func TestServerUnreachable(t *testing.T) {
	// Point at a port that nothing is listening on
	srv := New("http://localhost:19999")
	text := callTool(t, srv, "list_desktops", nil)

	// Should get an error result, not a test crash
	if text == "" {
		t.Fatal("expected error message, got empty string")
	}
}
