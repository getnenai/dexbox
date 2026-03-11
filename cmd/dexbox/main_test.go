package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// setenv sets an environment variable for the duration of the test.
func setenv(t *testing.T, key, val string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Setenv(key, val) //nolint:tenv
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old) //nolint:tenv
		} else {
			os.Unsetenv(key)
		}
	})
}

// unsetenv removes an environment variable for the duration of the test.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old) //nolint:tenv
		}
	})
}

// withDexboxHost temporarily overrides the dexboxHost package variable.
func withDexboxHost(t *testing.T, host string) {
	t.Helper()
	old := dexboxHost
	dexboxHost = host
	t.Cleanup(func() { dexboxHost = old })
}

// ---------------------------------------------------------------------------
// streamRun — NDJSON event dispatch
// ---------------------------------------------------------------------------

func TestStreamRun_StartedEvent(t *testing.T) {
	ndjson := `{"type":"started"}` + "\n"
	var out, errOut bytes.Buffer
	code, err := streamRun(strings.NewReader(ndjson), "test.py", &out, &errOut)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(out.String(), "test.py") {
		t.Errorf("expected workflow ID in output, got: %q", out.String())
	}
}

func TestStreamRun_ProgressText(t *testing.T) {
	ndjson := `{"type":"progress","data":{"type":"text","data":"hello world"}}` + "\n"
	var out, errOut bytes.Buffer
	_, _ = streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if !strings.Contains(out.String(), "Agent thought: hello world") {
		t.Errorf("expected 💭 text output, got: %q", out.String())
	}
}

func TestStreamRun_ProgressText_BlankSkipped(t *testing.T) {
	ndjson := `{"type":"progress","data":{"type":"text","data":"   "}}` + "\n"
	var out, errOut bytes.Buffer
	_, _ = streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if strings.Contains(out.String(), "Agent thought:") {
		t.Errorf("expected blank text to be skipped, got: %q", out.String())
	}
}

func TestStreamRun_ProgressToolCall(t *testing.T) {
	ndjson := `{"type":"progress","data":{"type":"tool_call","data":{"name":"screenshot"}}}` + "\n"
	var out, errOut bytes.Buffer
	_, _ = streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if !strings.Contains(out.String(), "Executing screenshot()") {
		t.Errorf("expected tool_call output, got: %q", out.String())
	}
}

func TestStreamRun_ProgressToolResult_Success(t *testing.T) {
	ndjson := `{"type":"progress","data":{"type":"tool_result","data":{"name":"click"}}}` + "\n"
	var out, errOut bytes.Buffer
	_, _ = streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if !strings.Contains(out.String(), "Tool click succeeded") {
		t.Errorf("expected ✅ tool result, got: %q", out.String())
	}
}

func TestStreamRun_ProgressToolResult_Failure(t *testing.T) {
	ndjson := `{"type":"progress","data":{"type":"tool_result","data":{"name":"type","error":"timeout"}}}` + "\n"
	var out, errOut bytes.Buffer
	_, _ = streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	got := out.String()
	if !strings.Contains(got, "WARN") || !strings.Contains(got, "Tool type failed: timeout") {
		t.Errorf("expected ❌ tool result error, got: %q", got)
	}
}

func TestStreamRun_ProgressLog(t *testing.T) {
	ndjson := `{"type":"progress","data":{"type":"log","data":"some log line"}}` + "\n"
	var out, errOut bytes.Buffer
	_, _ = streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if !strings.Contains(out.String(), "some log line") {
		t.Errorf("expected log line in output, got: %q", out.String())
	}
}

func TestStreamRun_Result_Success(t *testing.T) {
	ndjson := `{"type":"result","data":{"success":true,"duration_ms":123}}` + "\n"
	var out, errOut bytes.Buffer
	code, err := streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("expected success, got code=%d err=%v", code, err)
	}
	if !strings.Contains(out.String(), "✓") {
		t.Errorf("expected ✓ in output, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "123 ms") {
		t.Errorf("expected '123 ms' in output, got: %q", out.String())
	}
}

func TestStreamRun_Result_SuccessWithAssetsURL(t *testing.T) {
	ndjson := `{"type":"result","data":{"success":true,"duration_ms":123,"assets_url":"https://s3.amazonaws.com/assets.zip"}}` + "\n"
	var out, errOut bytes.Buffer
	code, err := streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("expected success, got code=%d err=%v", code, err)
	}
	if !strings.Contains(out.String(), "Assets URL: https://s3.amazonaws.com/assets.zip") {
		t.Errorf("expected assets URL in output, got: %q", out.String())
	}
}

func TestStreamRun_Result_SuccessWithResult(t *testing.T) {
	ndjson := `{"type":"result","data":{"success":true,"result":{"key":"val"}}}` + "\n"
	var out, errOut bytes.Buffer
	code, _ := streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0")
	}
	if !strings.Contains(out.String(), `"key"`) {
		t.Errorf("expected result JSON in output, got: %q", out.String())
	}
}

func TestStreamRun_Result_Failure(t *testing.T) {
	ndjson := `{"type":"result","data":{"success":false,"error":"something went wrong"}}` + "\n"
	var out, errOut bytes.Buffer
	code, _ := streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "something went wrong") {
		t.Errorf("expected error in stderr, got: %q", errOut.String())
	}
}

func TestStreamRun_ErrorEvent(t *testing.T) {
	ndjson := `{"type":"error","data":{"error":"internal error"}}` + "\n"
	var out, errOut bytes.Buffer
	code, _ := streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "internal error") {
		t.Errorf("expected error in stderr, got: %q", errOut.String())
	}
}

func TestStreamRun_EmptyLinesSkipped(t *testing.T) {
	ndjson := "\n\n" + `{"type":"started"}` + "\n\n"
	var out, errOut bytes.Buffer
	code, err := streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("expected clean run, got code=%d err=%v", code, err)
	}
	if !strings.Contains(out.String(), "wf") {
		t.Errorf("expected started event to still be printed, got: %q", out.String())
	}
}

func TestStreamRun_InvalidJSON_PrintedAsIs(t *testing.T) {
	ndjson := "not-json-at-all\n" + `{"type":"started"}` + "\n"
	var out, errOut bytes.Buffer
	_, _ = streamRun(strings.NewReader(ndjson), "wf", &out, &errOut)
	if !strings.Contains(out.String(), "not-json-at-all") {
		t.Errorf("expected invalid JSON line printed as-is, got: %q", out.String())
	}
}

func TestStreamRun_MultipleEvents(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"type":"started"}`,
		`{"type":"progress","data":{"type":"text","data":"thinking"}}`,
		`{"type":"progress","data":{"type":"tool_call","data":{"name":"click"}}}`,
		`{"type":"progress","data":{"type":"tool_result","data":{"name":"click"}}}`,
		`{"type":"result","data":{"success":true}}`,
	}, "\n") + "\n"
	var out, errOut bytes.Buffer
	code, err := streamRun(strings.NewReader(ndjson), "multi.py", &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("expected clean run, got code=%d err=%v", code, err)
	}
	got := out.String()
	for _, want := range []string{"multi.py", "Agent thought:", "thinking", "Executing click()", "Tool click succeeded", "✓"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// cmdRun — argument and flag validation
// ---------------------------------------------------------------------------

func TestCmdRun_MissingAPIKey(t *testing.T) {
	unsetenv(t, "ANTHROPIC_API_KEY")
	f, err := os.CreateTemp(t.TempDir(), "*.py")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{f.Name()})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("expected ANTHROPIC_API_KEY error, got: %v", err)
	}
}

func TestCmdRun_Params_BadJSON(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "test-key")
	f, err := os.CreateTemp(t.TempDir(), "*.py")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--params", "not json {{", f.Name()})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "parsing params JSON") {
		t.Fatalf("expected JSON parse error for params, got: %v", err)
	}
}

func TestCmdRun_SecureParams_BadJSON(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "test-key")
	f, err := os.CreateTemp(t.TempDir(), "*.py")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--secure-params", "not json {{", f.Name()})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "parsing secure params JSON") {
		t.Fatalf("expected JSON parse error for secure params, got: %v", err)
	}
}

// payloadServer returns an httptest.Server that captures request bodies and
// always responds with a successful result NDJSON event.
func payloadServer(t *testing.T, captured *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*captured = b
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"type":"result","data":{"success":true,"duration_ms":456}}`)
	}))
}

func TestCmdRun_PayloadContainsAPIKey(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "my-api-key")
	unsetenv(t, "DEXBOX_MODEL")

	var captured []byte
	srv := payloadServer(t, &captured)
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	f, _ := os.CreateTemp(t.TempDir(), "*.py")
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--no-browser", f.Name()})
	_ = cmd.Execute()

	var payload map[string]interface{}
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("could not parse captured payload: %v", err)
	}
	if payload["api_key"] != "my-api-key" {
		t.Errorf("expected api_key=my-api-key, got %v", payload["api_key"])
	}
}

func TestCmdRun_NoBrowserFlag(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "key")

	var captured []byte
	srv := payloadServer(t, &captured)
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	f, _ := os.CreateTemp(t.TempDir(), "*.py")
	fmt.Fprintln(f, "pass")
	f.Close()

	// Mock openBrowser
	browserCalled := false
	oldOpenBrowser := openBrowser
	openBrowser = func(url string) error {
		browserCalled = true
		return nil
	}
	t.Cleanup(func() { openBrowser = oldOpenBrowser })

	// Test with --no-browser
	cmd := cmdRun()
	cmd.SetArgs([]string{"--no-browser", f.Name()})
	_ = cmd.Execute()

	if browserCalled {
		t.Errorf("expected browser NOT to be called when --no-browser is set")
	}

	// Reset and test without --no-browser
	browserCalled = false
	cmd = cmdRun()
	cmd.SetArgs([]string{f.Name()})
	_ = cmd.Execute()

	if !browserCalled {
		t.Errorf("expected browser to be called when --no-browser is NOT set")
	}
}

func TestCmdRun_ModelFlag_Override(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "key")
	unsetenv(t, "DEXBOX_MODEL")

	var captured []byte
	srv := payloadServer(t, &captured)
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	f, _ := os.CreateTemp(t.TempDir(), "*.py")
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--no-browser", "--model", "my-custom-model", f.Name()})
	_ = cmd.Execute()

	var payload map[string]interface{}
	json.Unmarshal(captured, &payload)
	if payload["model"] != "my-custom-model" {
		t.Errorf("expected model=my-custom-model, got %v", payload["model"])
	}
}

func TestCmdRun_ModelEnvVar(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "key")
	setenv(t, "DEXBOX_MODEL", "env-model")

	var captured []byte
	srv := payloadServer(t, &captured)
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	f, _ := os.CreateTemp(t.TempDir(), "*.py")
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--no-browser", f.Name()})
	_ = cmd.Execute()

	var payload map[string]interface{}
	json.Unmarshal(captured, &payload)
	if payload["model"] != "env-model" {
		t.Errorf("expected env-model, got %v", payload["model"])
	}
}

func TestCmdRun_DefaultModel(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "key")
	unsetenv(t, "DEXBOX_MODEL")

	var captured []byte
	srv := payloadServer(t, &captured)
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	f, _ := os.CreateTemp(t.TempDir(), "*.py")
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--no-browser", f.Name()})
	_ = cmd.Execute()

	var payload map[string]interface{}
	json.Unmarshal(captured, &payload)
	if payload["model"] != "claude-opus-4-6" {
		t.Errorf("expected default model, got %v", payload["model"])
	}
}

func TestCmdRun_AlreadyRunning(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	f, _ := os.CreateTemp(t.TempDir(), "*.py")
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--no-browser", f.Name()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected 'already running' error, got: %v", err)
	}
}

func TestCmdRun_ServerError(t *testing.T) {
	setenv(t, "ANTHROPIC_API_KEY", "key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	f, _ := os.CreateTemp(t.TempDir(), "*.py")
	fmt.Fprintln(f, "pass")
	f.Close()

	cmd := cmdRun()
	cmd.SetArgs([]string{"--no-browser", f.Name()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "server error") {
		t.Fatalf("expected 'server error', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// cmdCancel
// ---------------------------------------------------------------------------

func TestCmdCancel_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	if err := cmdCancel().Execute(); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestCmdCancel_NoWorkflowRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	// 404 should not be an error — it just means nothing was running.
	if err := cmdCancel().Execute(); err != nil {
		t.Fatalf("expected nil error for 404, got: %v", err)
	}
}

func TestCmdCancel_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	err := cmdCancel().Execute()
	if err == nil || !strings.Contains(err.Error(), "cancel failed") {
		t.Fatalf("expected 'cancel failed' error, got: %v", err)
	}
}

func TestCmdCancel_Unreachable(t *testing.T) {
	withDexboxHost(t, "http://localhost:19997") // nothing listening here
	err := cmdCancel().Execute()
	if err == nil || !strings.Contains(err.Error(), "could not reach service") {
		t.Fatalf("expected 'could not reach service' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// cmdStatus
// ---------------------------------------------------------------------------

func TestCmdStatus_Running(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"running":        true,
			"workflow_id":    "my-workflow.py",
			"uptime_seconds": 42.0,
		})
	}))
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	if err := cmdStatus().Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCmdStatus_Idle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"running":        false,
			"uptime_seconds": 10.0,
		})
	}))
	defer srv.Close()
	withDexboxHost(t, srv.URL)

	if err := cmdStatus().Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCmdStatus_Unreachable(t *testing.T) {
	withDexboxHost(t, "http://localhost:19998") // nothing listening here
	// cmdStatus should return nil even when the service is unreachable — it
	// prints "Status: STOPPED" and exits cleanly.
	if err := cmdStatus().Execute(); err != nil {
		t.Fatalf("expected nil error for unreachable service, got: %v", err)
	}
}
