package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mockAnthropicServer spins up an httptest.Server that intercepts requests
// to the Anthropic API and returns predefined JSON responses.
func mockAnthropicServer(t *testing.T, expectedResponses []string) *httptest.Server {
	responseIndex := 0

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log the incoming request for debugging
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		t.Logf("Mock LLM Server received request to %s: %s", r.URL.Path, body.String())

		if responseIndex < len(expectedResponses) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(expectedResponses[responseIndex]))
			responseIndex++
		} else {
			t.Errorf("Mock LLM Server received unexpected request #%d", responseIndex+1)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": {"type": "internal_error", "message": "Unexpected request"}}`))
		}
	})

	return httptest.NewServer(handler)
}

// TestIntegration_MockedLLM_SimpleComputer executes a simple workflow that should
// trigger the LLM to output a computer tool command, which the
// Python server should then execute in the sandbox, and stream the result back.
func TestIntegration_MockedLLM_SimpleComputer(t *testing.T) {
	// 1. Create a dummy workflow Python script in a temporary directory
	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "test_workflow.py")
	workflowScript := `
from pydantic import BaseModel
from dexbox import Agent

class Input(BaseModel):
    pass

class Output(BaseModel):
    success: bool

def run(params: Input) -> Output:
    # Trigger the agentic loop that will hit our Mock Anthropic server
    agent = Agent()
    result = agent.execute("Move the mouse")
    is_success = result.get("success", False) if isinstance(result, dict) else False
    print(f"Agent finished: {is_success}")
    return Output(success=is_success)
`
	err := os.WriteFile(workflowPath, []byte(workflowScript), 0644)
	if err != nil {
		t.Fatalf("Failed to write mock workflow: %v", err)
	}

	// 2. Define the exact API response we want the Mock LLM to return
	// This simulates Claude deciding to use the "computer" tool
	mockToolUseResponse := `{
		"id": "msg_mock123",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": [
			{
				"type": "text",
				"text": "I will change the mouse position."
			},
			{
				"type": "tool_use",
				"id": "toolu_mock123",
				"name": "computer",
				"input": {
					"action": "mouse_move",
					"coordinate": [100, 100]
				}
			}
		],
		"stop_reason": "tool_use",
		"stop_sequence": null,
		"usage": {"input_tokens": 100, "output_tokens": 50}
	}`

	// After the tool runs, Claude should see the result and return a final message saying it succeeded.
	mockFinalResponse := `{
		"id": "msg_mock456",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": [
			{
				"type": "text",
				"text": "The computer interaction executed successfully."
			}
		],
		"stop_reason": "end_turn",
		"stop_sequence": null,
		"usage": {"input_tokens": 150, "output_tokens": 20}
	}`

	mockServer := mockAnthropicServer(t, []string{mockToolUseResponse, mockFinalResponse})
	defer mockServer.Close()

	// 3. Set the environment variable to point the `dexbox` Python server
	// to our Go Mock Server.
	// NOTE: The Python server must support ANTHROPIC_BASE_URL (or similar) to override the API endpoint.
	// For this test to be truly black-box, the Python server needs to be running.
	// We assume it is running on the default localhost:8600 port.

	// Check if dexbox is running
	resp, err := http.Get("http://localhost:8600/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skip("dexbox server is not running at http://localhost:8600. Start it with `make start` before running integration tests.")
	}
	resp.Body.Close()

	// Build the CLI using the repository root
	cwd, _ := os.Getwd()
	repoRoot := filepath.Dir(filepath.Dir(cwd)) // integration_test.go is in cmd/dexbox, so root is ../..

	buildCmd := exec.Command("go", "build", "-o", filepath.Join(tmpDir, "dexbox"), "./cmd/dexbox")
	buildCmd.Dir = repoRoot
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("Failed to build dexbox CLI: %v", err)
	}

	// Because dexbox runs in a Docker container, we must use host.docker.internal
	// for the container to reach the httptest server running on the Mac host
	mockURL := strings.Replace(mockServer.URL, "127.0.0.1", "host.docker.internal", 1)

	dexboxBin := filepath.Join(tmpDir, "dexbox")
	runCmd := exec.Command(dexboxBin, "run", workflowPath, "--anthropic-base-url", mockURL, "--no-browser")

	// Ensure we pass the API key, even though the mock server won't validate it
	runCmd.Env = append(os.Environ(), "ANTHROPIC_API_KEY=sk-mock12345")

	var stdoutBuf, stderrBuf bytes.Buffer
	runCmd.Stdout = &stdoutBuf
	runCmd.Stderr = &stderrBuf

	t.Logf("Running command: %s %v", runCmd.Path, runCmd.Args)
	err = runCmd.Run()

	stdoutStr := stdoutBuf.String()
	t.Logf("STDOUT: \n%s", stdoutStr)
	t.Logf("STDERR: \n%s", stderrBuf.String())

	if err != nil {
		t.Fatalf("dexbox run failed: %v", err)
	}

	// 4. Assert that the computer tool was called and the output streamed back
	if !strings.Contains(stdoutStr, "Executing tool: computer") {
		t.Errorf("Expected 'Executing tool: computer' in stdout")
	}
	if !strings.Contains(stdoutStr, "Workflow completed in") {
		t.Errorf("Expected 'Workflow completed in' in stdout")
	}
	if strings.Contains(stdoutStr, "completed in 0 ms") {
		t.Errorf("Expected non-zero duration in stdout, got: %q", stdoutStr)
	}
}
