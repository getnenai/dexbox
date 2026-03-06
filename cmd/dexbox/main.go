// Package main — dexbox CLI
//
// Commands:
//
//	dexbox start    — Start the desktop container (docker compose up)
//	dexbox stop     — Stop the desktop container  (docker compose down)
//	dexbox run      — Run a workflow script
//	dexbox logs     — Tail container logs
//	dexbox status   — Show container / workflow status
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/getnenai/dexbox/internal/logger"
)

const (
	defaultHost    = "http://localhost:8600"
	healthEndpoint = "/health"
	runEndpoint    = "/run"
	cancelEndpoint = "/cancel"
	statusEndpoint = "/status"
	logsService    = "desktop"
)

// dexboxHost is the base URL of the dexbox service.
// Tests may override this to point at an httptest.Server.
var dexboxHost = defaultHost

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	var envFile string

	root := &cobra.Command{
		Use:   "dexbox",
		Short: "dexbox — run computer-use workflows locally",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if envFile != "" {
				if err := godotenv.Load(envFile); err != nil {
					return fmt.Errorf("loading env file %s: %w", envFile, err)
				}
			} else {
				// Load .env file if it exists, but don't fail if it's missing
				_ = godotenv.Load()
			}
			return nil
		},
	}

	root.PersistentFlags().StringVarP(&envFile, "env-file", "e", "", "Path to .env file")

	root.AddCommand(cmdStart(), cmdStop(), cmdCancel(), cmdRun(), cmdLogs(), cmdStatus(), cmdServer())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// dexbox start
// ---------------------------------------------------------------------------

func cmdStart() *cobra.Command {
	var wait bool
	var debug bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the dexbox container",
		RunE: func(cmd *cobra.Command, args []string) error {
			if debug {
				os.Setenv("DEXBOX_LOG_LEVEL", "DEBUG")
			}
			fmt.Println("Starting dexbox…")
			if err := dockerCompose("up", "-d"); err != nil {
				return err
			}
			if wait {
				return waitReady(60 * time.Second)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait until service is healthy before returning")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging in the container")
	return cmd
}

// ---------------------------------------------------------------------------
// dexbox stop
// ---------------------------------------------------------------------------

func cmdStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the dexbox container",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Stopping dexbox…")
			return dockerCompose("down")
		},
	}
}

// ---------------------------------------------------------------------------
// dexbox cancel
// ---------------------------------------------------------------------------

func cmdCancel() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel",
		Short: "Cancel the currently-running workflow",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Post(dexboxHost+cancelEndpoint, "application/json", nil) //nolint:noctx
			if err != nil {
				return fmt.Errorf("could not reach service: %w", err)
			}
			defer resp.Body.Close()

			switch resp.StatusCode {
			case http.StatusOK:
				fmt.Println("✓  Workflow cancelled")
			case http.StatusNotFound:
				fmt.Println("ℹ  No workflow is currently running")
			default:
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("cancel failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// dexbox run
// ---------------------------------------------------------------------------

func cmdRun() *cobra.Command {
	var (
		paramsStr        string
		secureParamsStr  string
		artifactsDir     string
		timeout          int
		model            string
		anthropicBaseURL string
		noBrowser        bool
	)

	cmd := &cobra.Command{
		Use:   "run <workflow.py>",
		Short: "Run a workflow script",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scriptPath := args[0]
			script, err := os.ReadFile(scriptPath)
			if err != nil {
				return fmt.Errorf("reading script: %w", err)
			}

			// Parse --params from JSON string
			variables := make(map[string]interface{})
			if paramsStr != "" {
				if err := json.Unmarshal([]byte(paramsStr), &variables); err != nil {
					cmd.SilenceUsage = true
					return fmt.Errorf("parsing params JSON: %w", err)
				}
			}

			// Parse --secure-params from JSON string
			secureParams := make(map[string]string)
			if secureParamsStr != "" {
				if err := json.Unmarshal([]byte(secureParamsStr), &secureParams); err != nil {
					cmd.SilenceUsage = true
					return fmt.Errorf("parsing secure params JSON: %w", err)
				}
			}

			apiKey := os.Getenv("ANTHROPIC_API_KEY")
			if apiKey == "" {
				return fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
			}
			if model == "" {
				model = os.Getenv("DEXBOX_MODEL")
				if model == "" {
					model = "claude-haiku-4-5-20251001"
				}
			}

			workflowID := filepath.Base(scriptPath)

			payload := map[string]interface{}{
				"script":        string(script),
				"api_key":       apiKey,
				"model":         model,
				"workflow_id":   workflowID,
				"variables":     variables,
				"secure_params": secureParams,
			}
			if anthropicBaseURL != "" {
				payload["anthropic_base_url"] = anthropicBaseURL
			}

			body, err := json.Marshal(payload)
			if err != nil {
				return err
			}

			client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
			resp, err := client.Post(dexboxHost+runEndpoint, "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("connecting to dexbox: %w\n(Is it running? Try: dexbox start)", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == 429 {
				return fmt.Errorf("a workflow is already running (try: dexbox cancel)")
			}
			if resp.StatusCode != 200 {
				b, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("server error %d: %s", resp.StatusCode, b)
			}

			streamURL := "http://localhost:6080/vnc_lite.html"
			fmt.Fprintf(os.Stdout, "▶  Live dexbox stream available at: %s\n", streamURL)
			if !noBrowser {
				_ = openBrowser(streamURL)
			}

			// Stream NDJSON output
			code, err := streamRun(resp.Body, workflowID, os.Stdout, os.Stderr)
			if code != 0 {
				os.Exit(code)
			}

			// Copy artifacts if requested
			if artifactsDir != "" {
				fmt.Printf("Artifacts saved to: %s\n", artifactsDir)
			}

			return err
		},
	}

	cmd.Flags().StringVar(&paramsStr, "params", "", "Workflow parameters as JSON string (e.g. --params '{\"foo\":\"bar\"}')")
	cmd.Flags().StringVar(&secureParamsStr, "secure-params", "", "Sensitive parameters as JSON string (e.g. --secure-params '{\"token\":\"sekret\"}')")
	cmd.Flags().StringVar(&artifactsDir, "artifacts", "", "Directory to save workflow artifacts")
	cmd.Flags().IntVar(&timeout, "timeout", 600, "Workflow timeout in seconds")
	cmd.Flags().StringVar(&model, "model", "", "LLM model override")
	cmd.Flags().StringVar(&anthropicBaseURL, "anthropic-base-url", "", "Override the base URL for Anthropic API requests")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Do not launch the browser for the dexbox stream")
	return cmd
}

// ---------------------------------------------------------------------------
// dexbox logs
// ---------------------------------------------------------------------------

func cmdLogs() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show container logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			dargs := []string{"compose", "logs"}
			if follow {
				dargs = append(dargs, "-f")
			}
			dargs = append(dargs, logsService)
			c := exec.Command("docker", dargs...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	return cmd
}

// ---------------------------------------------------------------------------
// dexbox status
// ---------------------------------------------------------------------------

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service and workflow status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get(dexboxHost + statusEndpoint)
			if err != nil {
				fmt.Fprintf(os.Stderr, "dexbox not reachable: %v\n", err)
				fmt.Println("Status: STOPPED")
				return nil
			}
			defer resp.Body.Close()
			var status map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
				return err
			}
			running, _ := status["running"].(bool)
			if running {
				wid, _ := status["workflow_id"].(string)
				fmt.Printf("Status: RUNNING  (workflow: %s)\n", wid)
			} else {
				fmt.Println("Status: IDLE")
			}
			uptime, _ := status["uptime_seconds"].(float64)
			fmt.Printf("Uptime: %.0fs\n", uptime)
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func dockerCompose(args ...string) error {
	dargs := append([]string{"compose"}, args...)
	c := exec.Command("docker", dargs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// streamRun reads a newline-delimited JSON event stream from r and writes
// human-readable progress to stdout/stderr.
// It returns (exitCode, scannerError). exitCode is 1 when the server signals
// a workflow failure or error event.
func streamRun(r io.Reader, workflowID string, stdout, stderr io.Writer) (int, error) {
	scanner := bufio.NewScanner(r)
	// Increase buffer size to 10 MB to handle large VLM responses with images.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var downloadedAssets []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !gjson.Valid(line) {
			fmt.Fprintln(stdout, line)
			continue
		}
		e := gjson.Parse(line)
		switch e.Get("type").String() {
		case "started":
			fmt.Fprintf(stdout, "▶  Workflow started: %s\n", workflowID)
		case "progress":
			switch e.Get("data.type").String() {
			case "text":
				if text := strings.TrimSpace(e.Get("data.data").String()); text != "" {
					// Use JSON structure matching standard logs so it passes RenderLogLine cleanly
					logStr := fmt.Sprintf(`{"time":"%s", "level":"INFO", "name":"dexbox.agent", "message":%q}`, time.Now().Format(time.RFC3339Nano), "Agent thought: "+text)
					if formatted := logger.RenderLogLine(logStr); formatted != "" {
						fmt.Fprintln(stdout, formatted)
					}
				}
			case "tool_call":
				name := e.Get("data.data.name").String()
				inputRaw := e.Get("data.data.input").Raw

				callStr := formatToolCall(name, inputRaw)
				msg := fmt.Sprintf("Executing %s", callStr)

				logStr := fmt.Sprintf(`{"time":"%s", "level":"INFO", "name":"dexbox.tool", "message":%q, "tool":%q}`,
					time.Now().Format(time.RFC3339Nano), msg, name)
				if formatted := logger.RenderLogLine(logStr); formatted != "" {
					fmt.Fprintln(stdout, formatted)
				}
			case "tool_result":
				name := e.Get("data.data.name").String()
				if errMsg := e.Get("data.data.error").String(); errMsg != "" {
					logStr := fmt.Sprintf(`{"time":"%s", "level":"WARN", "name":"dexbox.tool", "message":%q, "tool":%q}`,
						time.Now().Format(time.RFC3339Nano), "Tool "+name+" failed: "+errMsg, name)
					if formatted := logger.RenderLogLine(logStr); formatted != "" {
						fmt.Fprintln(stdout, formatted)
					}
				} else {
					logStr := fmt.Sprintf(`{"time":"%s", "level":"DEBUG", "name":"dexbox.tool", "message":%q, "tool":%q}`,
						time.Now().Format(time.RFC3339Nano), "Tool "+name+" succeeded", name)
					if formatted := logger.RenderLogLine(logStr); formatted != "" {
						fmt.Fprintln(stdout, formatted)
					}
				}
			case "log":
				if logLine := e.Get("data.data").String(); logLine != "" {
					if formatted := logger.RenderLogLine(logLine); formatted != "" {
						fmt.Fprintln(stdout, formatted)
					}
				}
			case "assets":
				filename := e.Get("data.data.filename").String()
				contentB64 := e.Get("data.data.content_b64").String()
				if filename == "" || contentB64 == "" {
					break
				}
				data, err := base64.StdEncoding.DecodeString(contentB64)
				if err != nil {
					fmt.Fprintf(stderr, "✗  Failed to decode assets: %v\n", err)
					break
				}
				if err := os.WriteFile(filename, data, 0644); err != nil {
					fmt.Fprintf(stderr, "✗  Failed to save %s: %v\n", filename, err)
				} else {
					downloadedAssets = append(downloadedAssets, filename)
				}
			}
		case "result":
			if e.Get("data.success").Bool() {
				for _, asset := range downloadedAssets {
					fmt.Fprintf(stdout, "ℹ  Assets saved to: ./%s\n", asset)
				}
				duration := e.Get("data.duration_ms").Int()
				fmt.Fprintf(stdout, "✓  Workflow completed in %d ms\n", duration)

				if assetsURL := e.Get("data.assets_url").String(); assetsURL != "" {
					fmt.Fprintf(stdout, "🔗  Assets URL: %s\n", assetsURL)
				}

				if result := e.Get("data.result"); result.Exists() && result.Raw != "" {
					fmt.Fprintln(stdout, result.Raw)
				} else {
					fmt.Fprintln(stdout, "(No result returned)")
				}
			} else {
				fmt.Fprintf(stderr, "✗  Workflow failed: %s\n", e.Get("data.error").String())
				return 1, nil
			}
		case "error":
			fmt.Fprintf(stderr, "✗  Error: %s\n", e.Get("data.error").String())
			return 1, nil
		}
	}
	return 0, scanner.Err()
}

func waitReady(timeout time.Duration) error {
	fmt.Print("Waiting for service to be ready")
	client := &http.Client{Timeout: 2 * time.Second}
	_, err := backoff.Retry(
		context.Background(),
		backoff.Operation[struct{}](func() (struct{}, error) {
			resp, err := client.Get(dexboxHost + healthEndpoint)
			if err != nil {
				fmt.Print(".")
				return struct{}{}, fmt.Errorf("not ready")
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Println(" ✓")
				return struct{}{}, nil
			}
			fmt.Print(".")
			return struct{}{}, fmt.Errorf("not ready")
		}),
		backoff.WithMaxElapsedTime(timeout),
	)
	return err
}

var openBrowser = func(url string) error {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	return err
}

func formatToolCall(name string, inputRaw string) string {
	var inputMap map[string]interface{}
	if err := json.Unmarshal([]byte(inputRaw), &inputMap); err == nil {
		if name == "computer" {
			action, _ := inputMap["action"].(string)
			switch action {
			case "key":
				text, _ := inputMap["text"].(string)
				return fmt.Sprintf("key(%q)", text)
			case "type":
				text, _ := inputMap["text"].(string)
				return fmt.Sprintf("type(%q)", text)
			case "mouse_move":
				coords, ok := inputMap["coordinate"].([]interface{})
				if ok && len(coords) >= 2 {
					return fmt.Sprintf("mouse_move(%v, %v)", coords[0], coords[1])
				}
				return "mouse_move(?, ?)"
			case "left_click", "right_click", "middle_click", "double_click", "left_click_drag":
				return fmt.Sprintf("%s()", action)
			case "screenshot":
				return "screenshot()"
			default:
				var args []string
				for k, v := range inputMap {
					if k != "action" {
						args = append(args, fmt.Sprintf("%s=%v", k, v))
					}
				}
				if len(args) > 0 {
					return fmt.Sprintf("%s(%s)", action, strings.Join(args, ", "))
				}
				return fmt.Sprintf("%s()", action)
			}
		} else {
			// Generic formatting for other tools: tool(arg1=val1, ...)
			var args []string
			for k, v := range inputMap {
				args = append(args, fmt.Sprintf("%s=%v", k, v))
			}
			return fmt.Sprintf("%s(%s)", name, strings.Join(args, ", "))
		}
	}
	return fmt.Sprintf("%s(%s)", name, inputRaw)
}
