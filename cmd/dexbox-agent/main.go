// Package main — dexbox-agent: a standalone tool server for Windows.
//
// This is the native Windows entry point. It creates a NativeDesktop
// (Win32 screenshots + SendInput) and serves the same HTTP tool API as
// the main dexbox server, but without VBox, SOAP, or guacd.
//
// Build:
//
//	GOOS=windows GOARCH=amd64 go build -o bin/dexbox-agent.exe ./cmd/dexbox-agent
//
// Then copy dexbox-agent.exe to the Windows machine and run it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/getnenai/dexbox/internal/desktop"
	"github.com/getnenai/dexbox/internal/tools"
)

func main() {
	listen := os.Getenv("DEXBOX_LISTEN")
	if listen == "" {
		listen = ":8600"
	}
	name := os.Getenv("DEXBOX_DESKTOP_NAME")
	if name == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "native"
		}
		name = hostname
	}

	width := 1024
	height := 768

	if runtime.GOOS != "windows" {
		log.Fatal("dexbox-agent requires Windows (native desktop backend)")
	}

	log.Printf("dexbox-agent starting on %s (desktop=%q)", listen, name)

	d := desktop.NewNative(name)
	_ = d.Connect(context.Background())

	mux := http.NewServeMux()
	startTime := time.Now()

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"backend":        "native",
			"desktop":        name,
			"uptime_seconds": time.Since(startTime).Seconds(),
		})
	})

	// Tool schema endpoint
	mux.HandleFunc("/tools", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		schemas := tools.AllToolSchemas(tools.DisplayConfig{Width: width, Height: height})
		writeJSON(w, http.StatusOK, schemas)
	})

	// Single action endpoint
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		handleAction(w, r, d, width, height)
	})

	// Desktop list (single native desktop)
	mux.HandleFunc("/desktops", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"desktops": []map[string]any{{
				"name":      name,
				"type":      "native",
				"state":     "running",
				"connected": true,
			}},
		})
	})

	// Handle shutdown signals
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: listen, Handler: mux}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	log.Printf("dexbox-agent ready on %s", listen)
	<-sigCtx.Done()
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func handleAction(w http.ResponseWriter, r *http.Request, d desktop.Desktop, width, height int) {
	modelID := r.URL.Query().Get("model")
	adapter, err := tools.AdapterForModel(modelID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":              "unknown_model",
			"message":            fmt.Sprintf("Unknown model %q", modelID),
			"supported_prefixes": tools.SupportedPrefixes(),
		})
		return
	}

	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}

	action, err := adapter.ParseToolCall(json.RawMessage(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": err.Error(),
		})
		return
	}

	// Execute the action
	var result *tools.CanonicalResult

	switch action.Tool {
	case "computer":
		ct := tools.NewComputerTool(d, width, height)
		result, err = ct.Execute(r.Context(), action)

	case "bash":
		var p tools.BashParams
		if err := action.UnmarshalParams(&p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "bad_request",
				"message": err.Error(),
			})
			return
		}
		out, execErr := localExec(r.Context(), p.Command)
		if execErr != nil {
			result = &tools.CanonicalResult{Output: fmt.Sprintf("error: %v", execErr)}
		} else {
			result = &tools.CanonicalResult{Output: out}
		}

	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": fmt.Sprintf("unknown tool %q", action.Tool),
		})
		return
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}

	// Content negotiation
	if result.Image != nil && r.Header.Get("Accept") == "image/png" {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.Write(result.Image)
		return
	}

	formatted, err := adapter.FormatResult(action, result)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(formatted)
}

// localExec runs a PowerShell command locally and returns its output.
func localExec(ctx context.Context, command string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("field 'command' is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		}
		return "", err
	}
	return sanitizeOutput(string(out)), nil
}

// sanitizeOutput cleans shell output (same logic as tools.BashTool).
func sanitizeOutput(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")

	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blanks++
			if blanks <= 1 {
				cleaned = append(cleaned, line)
			}
		} else {
			blanks = 0
			cleaned = append(cleaned, line)
		}
	}

	result := strings.TrimSpace(strings.Join(cleaned, "\n"))

	const maxLen = 10_000
	runes := []rune(result)
	if len(runes) > maxLen {
		half := maxLen / 2
		truncated := len(runes) - maxLen
		result = string(runes[:half]) +
			fmt.Sprintf("\n\n... (%d characters truncated) ...\n\n", truncated) +
			string(runes[len(runes)-half:])
	}

	return result
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}
