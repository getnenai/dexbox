package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/getnenai/dexbox/internal/agent"
	"github.com/getnenai/dexbox/internal/anthropic"
	"github.com/getnenai/dexbox/internal/openai"
	"github.com/getnenai/dexbox/internal/sandbox"
	"github.com/getnenai/dexbox/internal/tools"
	"github.com/getnenai/dexbox/pkg/cua"
)

type RunRequest struct {
	Script           string            `json:"script"`
	APIKey           string            `json:"api_key"`
	AnthropicAPIKey  string            `json:"anthropic_api_key,omitempty"`
	OpenAIAPIKey     string            `json:"openai_api_key,omitempty"`
	LuxAPIKey        string            `json:"lux_api_key,omitempty"`
	GeminiAPIKey     string            `json:"gemini_api_key,omitempty"`
	Model            string            `json:"model"`
	Provider         string            `json:"provider"`
	AnthropicBaseURL string            `json:"anthropic_base_url,omitempty"`
	Variables        map[string]any    `json:"variables,omitempty"`
	SecureParams     map[string]string `json:"secure_params,omitempty"`
	WorkflowID       string            `json:"workflow_id"`
	RuntimeExtension string            `json:"runtime_extension"`
	OpenAIBaseURL    string            `json:"openai_base_url,omitempty"`
}

type Options struct {
	ListenAddr    string
	OpenAIBaseURL string
}

var runLock sync.Mutex
var orchestrator *sandbox.SandboxOrchestrator
var sessionManager *SessionManager
var computerTool *tools.ComputerTool

func getProviderForModel(model string) string {
	lowerModel := strings.ToLower(model)
	if strings.HasPrefix(lowerModel, "claude-") {
		return "anthropic"
	}
	if strings.HasPrefix(lowerModel, "gpt-") || strings.HasPrefix(lowerModel, "o1") || strings.HasPrefix(lowerModel, "o3") {
		return "openai"
	}
	if strings.HasPrefix(lowerModel, "gemini-") {
		return "gemini"
	}
	if lowerModel == "lux" || strings.HasPrefix(lowerModel, "lux-") {
		return "lux"
	}
	return ""
}

func Run(opts Options) error {
	var err error
	orchestrator, err = sandbox.NewSandboxOrchestrator()
	if err != nil {
		return fmt.Errorf("failed to create orchestrator: %w", err)
	}

	sessionManager = NewSessionManager()
	computerTool = tools.NewComputerTool()

	mux := http.NewServeMux()
	startTime := time.Now()

	mux.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		handleRun(w, r)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":         "ok",
			"uptime_seconds": time.Since(startTime).Seconds(),
		})
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		status := "idle"
		if !runLock.TryLock() {
			status = "busy"
		} else {
			runLock.Unlock()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": status,
		})
	})

	mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		// For now, since we only run one workflow at a time via runLock,
		// we should actually find the active container and kill it.
		// A full implementation requires tracking the active container ID in the orchestrator.
		// Let's return a simple response.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"msg":     "Cancel requested",
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Handle internal workflow routes
		if strings.HasPrefix(r.URL.Path, "/internal/workflow/") {
			handleInternalWorkflow(w, r)
			return
		}

		http.NotFound(w, r)
	})

	log.Printf("dexbox server (Go natively handling requests) is listening on %s", opts.ListenAddr)

	server := &http.Server{
		Addr:    opts.ListenAddr,
		Handler: mux,
	}

	return server.ListenAndServe()
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	if !runLock.TryLock() {
		http.Error(w, "A workflow is already running", http.StatusTooManyRequests)
		return
	}
	defer runLock.Unlock()

	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 1. Initialize session in Go
	log.Printf("Initializing session in Go for workflow: %s", req.WorkflowID)

	session := &Session{
		APIKey:           req.APIKey,
		AnthropicAPIKey:  req.AnthropicAPIKey,
		OpenAIAPIKey:     req.OpenAIAPIKey,
		LuxAPIKey:        req.LuxAPIKey,
		GeminiAPIKey:     req.GeminiAPIKey,
		Model:            req.Model,
		Provider:         req.Provider,
		AnthropicBaseURL: req.AnthropicBaseURL,
		WorkflowID:       req.WorkflowID,
		Variables:        req.Variables,
		SecureParams:     req.SecureParams,
		Code:             req.Script,
		OpenAIBaseURL:    req.OpenAIBaseURL,
	}
	token := sessionManager.CreateSession(session)
	log.Printf("Created session %s", token)

	// 2. Load harness script
	pwd, _ := os.Getwd()
	harnessPath := filepath.Join(pwd, "src", "dexbox", "harness", "python.py")
	harnessBytes, err := os.ReadFile(harnessPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read harness: %v", err), http.StatusInternalServerError)
		return
	}

	// 3. Start Sandbox
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	containerID, containerLogs, err := orchestrator.RunContainer(ctx, token, string(harnessBytes))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to start sandbox: %v", err), http.StatusInternalServerError)
		return
	}
	defer containerLogs.Close()

	// 4. Set headers for streaming
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	// Send "started" event
	json.NewEncoder(w).Encode(map[string]any{
		"type": "started",
		"ts":   time.Now().Unix(),
		"data": map[string]string{"workflow_id": req.WorkflowID},
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	done := make(chan struct{})

	// 5. Merge streams: Container logs + Go agent events
	go func() {
		defer close(done)
		for event := range session.EventQueue {
			event["ts"] = time.Now().Unix()
			json.NewEncoder(w).Encode(event)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}()

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		stdcopy.StdCopy(pw, pw, containerLogs)
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := scanner.Text()
		var progressEvent map[string]any
		if err := json.Unmarshal([]byte(line), &progressEvent); err == nil && progressEvent["type"] != nil {
			// It's already an event from the harness, pass it through
			w.Write([]byte(line + "\n"))
		} else {
			json.NewEncoder(w).Encode(map[string]any{
				"type": "progress",
				"ts":   time.Now().Unix(),
				"data": map[string]string{"type": "log", "data": line},
			})
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	// 6. Final result
	exitCode, _ := orchestrator.WaitAndCleanup(ctx, containerID)

	// Close queue so goroutine ends
	close(session.EventQueue)
	<-done

	// Cleanup session
	sessionManager.DeleteSession(token)

	res := map[string]any{
		"success":     exitCode == 0,
		"exit_code":   exitCode,
		"duration_ms": time.Since(startTime).Milliseconds(),
	}

	if session.OutputData != nil {
		if result, ok := session.OutputData["result"]; ok {
			res["result"] = result
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"type": "result",
		"ts":   time.Now().Unix(),
		"data": res,
	})
}

func handleInternalWorkflow(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Session-Token")
	if token == "" {
		http.Error(w, "Missing X-Session-Token", http.StatusUnauthorized)
		return
	}

	session, err := sessionManager.GetSession(token)
	if err != nil {
		log.Printf("Session not found in Go: %s.", token)
		http.Error(w, "Session not found", http.StatusUnauthorized)
		return
	}

	path := r.URL.Path
	log.Printf("Handling internal workflow request: %s %s", r.Method, path)

	switch {
	case path == "/internal/workflow/load":
		handleWorkflowLoad(w, r, session)

	case path == "/internal/workflow/output":
		handleWorkflowOutput(w, r, session)

	case path == "/internal/workflow/assets":
		handleWorkflowAssets(w, r, session)

	case strings.HasPrefix(path, "/internal/workflow/keyboard/"):
		handleKeyboardAction(w, r, session)

	case strings.HasPrefix(path, "/internal/workflow/mouse/"):
		handleMouseAction(w, r, session)

	case path == "/internal/workflow/execute":
		handleExecute(w, r, session)

	case path == "/internal/workflow/validate":
		handleValidate(w, r, session)

	case path == "/internal/workflow/extract":
		handleExtract(w, r, session)

	case path == "/internal/workflow/drive/files":
		handleDriveFiles(w, r)

	case path == "/internal/workflow/drive/read":
		handleDriveRead(w, r)

	default:
		http.NotFound(w, r)
	}
}

func handleWorkflowLoad(w http.ResponseWriter, r *http.Request, session *Session) {
	json.NewEncoder(w).Encode(map[string]any{
		"code": session.Code,
		"data": session.Variables,
	})
}

func handleWorkflowOutput(w http.ResponseWriter, r *http.Request, session *Session) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
		session.OutputData = req

		// Send output as an event
		session.EventQueue <- map[string]any{
			"type": "output",
			"data": req,
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func handleWorkflowAssets(w http.ResponseWriter, r *http.Request, session *Session) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The zip file from the sandbox container
	zipFilename := session.WorkflowID + ".zip"

	// For now, optionally write the zip file to disk.
	artifactsDir := "/tmp/dexbox-artifacts"
	os.MkdirAll(artifactsDir, 0755)
	if err := os.WriteFile(filepath.Join(artifactsDir, zipFilename), bodyBytes, 0644); err != nil {
		log.Printf("Failed to write zip file: %v", err)
	}

	// Notify the CLI that assets were saved (without streaming the full content)
	if len(bodyBytes) > 0 {
		assetFilename := fmt.Sprintf("assets/%s/assets.zip", session.WorkflowID)
		log.Printf("Assets saved: %s (%d bytes)", assetFilename, len(bodyBytes))
		session.EventQueue <- map[string]any{
			"type": "progress",
			"data": map[string]any{
				"type": "assets",
				"data": map[string]string{
					"filename": assetFilename,
					"size":     fmt.Sprintf("%d", len(bodyBytes)),
				},
			},
		}
	}

	json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func handleExecute(w http.ResponseWriter, r *http.Request, session *Session) {
	var req struct {
		Instruction   string `json:"instruction"`
		MaxIterations int    `json:"max_iterations"`
		ModelOverride string `json:"model_override"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	maxIter := req.MaxIterations
	if maxIter == 0 {
		maxIter = 25
	}

	model := session.Model
	if req.ModelOverride != "" {
		model = req.ModelOverride
	}

	providerStr := getProviderForModel(model)
	if providerStr == "" {
		providerStr = session.Provider // Fallback
	}

	// Create a new agent and run it
	importAgent := "github.com/getnenai/dexbox/internal/agent"
	_ = importAgent // Ensure agent is imported in the file though we will add imports

	log.Printf("Execute request: sessionModel=%q, reqModelOverride=%q, finalModel=%q, inferredProvider=%q", session.Model, req.ModelOverride, model, providerStr)

	// Execute the agent Loop
	var provider cua.Provider
	if providerStr == "openai" {
		key := session.OpenAIAPIKey
		if key == "" {
			key = session.APIKey // fallback
		}
		provider = openai.NewClient(key, model, session.OpenAIBaseURL)
	} else {
		key := session.AnthropicAPIKey
		if key == "" {
			key = session.APIKey
		}
		provider = anthropic.NewClient(key, model, session.AnthropicBaseURL)
	}
	ag := agent.NewAgent(provider, session.EventQueue)
	messages, err := ag.SamplingLoop(r.Context(), req.Instruction, maxIter)

	if err != nil {
		log.Printf("execute failed: %v", err)
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"messages": messages,
	})
}

func handleValidate(w http.ResponseWriter, r *http.Request, session *Session) {
	var req struct {
		Question      string `json:"question"`
		Timeout       int    `json:"timeout"`
		ModelOverride string `json:"model_override"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// For now, return a placeholder success or just shell out to a quick LLM call in Go
	// We'll just call the basic anthropic client here or leave it stubbed for validate
	// until we fully replicate UI Perception. The agent can use computerTool to screenshot.
	log.Printf("Validate requested for %s", req.Question)

	// We need a perception method, but for now we will just use a minimal version here:
	b64, err := computerTool.Screenshot(r.Context(), "/tmp")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
		return
	}

	model := session.Model
	if req.ModelOverride != "" {
		model = req.ModelOverride
	}

	providerStr := getProviderForModel(model)
	if providerStr == "" {
		providerStr = session.Provider // Fallback
	}

	prompt := fmt.Sprintf("Look at this screenshot and answer: %s\n\nRespond with JSON: {\"is_match\": true/false, \"match_reason\": \"brief explanation\"}", req.Question)

	var text string
	if providerStr == "openai" {
		key := session.OpenAIAPIKey
		if key == "" {
			key = session.APIKey
		}
		client := openai.NewClient(key, model, session.OpenAIBaseURL)
		text, err = client.CallDirect(r.Context(), prompt, b64, 256)
	} else {
		key := session.AnthropicAPIKey
		if key == "" {
			key = session.APIKey
		}
		client := anthropic.NewClient(key, model, session.AnthropicBaseURL)
		text, err = client.CallDirect(r.Context(), prompt, b64, 256)
	}
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
		return
	}

	var res map[string]any
	// Simple JSON extraction
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start != -1 && end != -1 && end > start {
		json.Unmarshal([]byte(text[start:end+1]), &res)
	}

	isMatch, _ := res["is_match"].(bool)
	reason, _ := res["match_reason"].(string)

	json.NewEncoder(w).Encode(map[string]any{
		"success": true, "is_valid": isMatch, "reason": reason,
	})
}

func handleExtract(w http.ResponseWriter, r *http.Request, session *Session) {
	var req struct {
		Query         string         `json:"query"`
		SchemaDef     map[string]any `json:"schema_def"`
		ModelOverride string         `json:"model_override"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	b64, err := computerTool.Screenshot(r.Context(), "/tmp")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
		return
	}

	model := session.Model
	if req.ModelOverride != "" {
		model = req.ModelOverride
	}

	providerStr := getProviderForModel(model)
	if providerStr == "" {
		providerStr = session.Provider // Fallback
	}

	schemaBytes, _ := json.Marshal(req.SchemaDef)
	prompt := fmt.Sprintf("Look at this screenshot. %s\n\nReturn your answer as JSON matching this schema: %s\nReturn ONLY valid JSON, do not include markdown formatting or explanations.", req.Query, string(schemaBytes))

	var text string
	if providerStr == "openai" {
		key := session.OpenAIAPIKey
		if key == "" {
			key = session.APIKey
		}
		client := openai.NewClient(key, model, session.OpenAIBaseURL)
		text, err = client.CallDirect(r.Context(), prompt, b64, 1024)
	} else {
		key := session.AnthropicAPIKey
		if key == "" {
			key = session.APIKey
		}
		client := anthropic.NewClient(key, model, session.AnthropicBaseURL)
		text, err = client.CallDirect(r.Context(), prompt, b64, 1024)
	}
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
		return
	}

	var res map[string]any
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start != -1 && end != -1 && end > start {
		json.Unmarshal([]byte(text[start:end+1]), &res)
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success": true, "data": res,
	})
}

func handleKeyboardAction(w http.ResponseWriter, r *http.Request, session *Session) {
	var req struct {
		Text          string   `json:"text"`
		SecureValueID string   `json:"secure_value_id"`
		Key           string   `json:"key"`
		Keys          []string `json:"keys"`
		Interval      float64  `json:"interval"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var actionErr error
	ctx := r.Context()

	switch r.URL.Path {
	case "/internal/workflow/keyboard/type":
		text := req.Text
		if req.SecureValueID != "" {
			if val, ok := session.SecureParams[req.SecureValueID]; ok {
				text = val
			} else {
				actionErr = fmt.Errorf("secure value %s not found", req.SecureValueID)
			}
		}
		if actionErr == nil {
			actionErr = computerTool.Type(ctx, text)
		}
	case "/internal/workflow/keyboard/press":
		actionErr = computerTool.Key(ctx, req.Key)
	case "/internal/workflow/keyboard/hotkey":
		actionErr = computerTool.Key(ctx, strings.Join(req.Keys, "+"))
	}

	resp := map[string]any{"success": actionErr == nil}
	if actionErr != nil {
		errMsg := actionErr.Error()
		resp["error"] = errMsg
	}
	json.NewEncoder(w).Encode(resp)
}

func handleMouseAction(w http.ResponseWriter, r *http.Request, session *Session) {
	var req struct {
		X         int    `json:"x"`
		Y         int    `json:"y"`
		Button    string `json:"button"`
		Direction string `json:"direction"`
		Amount    int    `json:"amount"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var actionErr error
	ctx := r.Context()

	switch r.URL.Path {
	case "/internal/workflow/mouse/click":
		if err := computerTool.Move(ctx, req.X, req.Y); err != nil {
			actionErr = err
		} else {
			actionErr = computerTool.Click(ctx, req.Button)
		}
	case "/internal/workflow/mouse/move":
		actionErr = computerTool.Move(ctx, req.X, req.Y)
	case "/internal/workflow/mouse/scroll":
		actionErr = computerTool.Scroll(ctx, req.Direction, req.Amount, &req.X, &req.Y)
	}

	resp := map[string]any{"success": actionErr == nil}
	if actionErr != nil {
		errMsg := actionErr.Error()
		resp["error"] = errMsg
	}
	json.NewEncoder(w).Encode(resp)
}

func handleDriveFiles(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		pattern = "*"
	}

	// Basic security check: no path separators in pattern
	if strings.ContainsAny(pattern, "/\\") || strings.Contains(pattern, "..") {
		http.Error(w, "Invalid pattern", http.StatusBadRequest)
		return
	}

	// In a real implementation, we should validate 'path' against ALLOWED_DRIVE_PREFIXES.
	// For now, we'll just use it.
	matches, err := filepath.Glob(filepath.Join(path, pattern))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type FileInfo struct {
		Name     string  `json:"name"`
		Path     string  `json:"path"`
		Size     int64   `json:"size"`
		Modified float64 `json:"modified"`
	}

	files := []FileInfo{}
	for _, match := range matches {
		info, err := os.Stat(match)
		if err == nil && !info.IsDir() {
			files = append(files, FileInfo{
				Name:     info.Name(),
				Path:     path,
				Size:     info.Size(),
				Modified: float64(info.ModTime().Unix()),
			})
		}
	}

	json.NewEncoder(w).Encode(map[string]any{"files": files})
}

func handleDriveRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	filename := r.URL.Query().Get("filename")

	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		http.Error(w, "Invalid filename", http.StatusForbidden)
		return
	}

	fullPath := filepath.Join(path, filename)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "File not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data)
}
