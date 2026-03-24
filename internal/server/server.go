package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/getnenai/dexbox/internal/tools"
	"github.com/getnenai/dexbox/internal/vbox"
)

// Server is the dexbox tool server.
type Server struct {
	manager *vbox.Manager
	listen  string
	vmUser  string
	vmPass  string
	display tools.DisplayConfig
	shared  string

	// Per-VM tool instances (lazily created)
	mu        sync.RWMutex
	computers map[string]*tools.ComputerTool
	bashes    map[string]*tools.BashTool
	editors   map[string]*tools.EditorTool
}

// Options configures the tool server.
type Options struct {
	ListenAddr string
	SOAPAddr   string
	VMUser     string
	VMPass     string
	Width      int
	Height     int
	SharedDir  string
}

// New creates a new tool server.
func New(opts Options) *Server {
	return &Server{
		manager:   vbox.NewManager(opts.SOAPAddr, opts.VMUser, opts.VMPass),
		listen:    opts.ListenAddr,
		vmUser:    opts.VMUser,
		vmPass:    opts.VMPass,
		display:   tools.DisplayConfig{Width: opts.Width, Height: opts.Height},
		shared:    opts.SharedDir,
		computers: make(map[string]*tools.ComputerTool),
		bashes:    make(map[string]*tools.BashTool),
		editors:   make(map[string]*tools.EditorTool),
	}
}

// Manager returns the underlying VM manager.
func (s *Server) Manager() *vbox.Manager {
	return s.manager
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	mux := http.NewServeMux()
	startTime := time.Now()

	// Tool routes
	mux.HandleFunc("/tools", s.handleGetToolSchema)
	mux.HandleFunc("/actions", s.handleAction)
	mux.HandleFunc("/actions/batch", s.handleBatchAction)

	// VM lifecycle routes
	mux.HandleFunc("/vm", s.handleVM)
	mux.HandleFunc("/vm/", s.handleVMNamed)

	// Health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"uptime_seconds": time.Since(startTime).Seconds(),
		})
	})

	log.Printf("dexbox tool server listening on %s", s.listen)
	return http.ListenAndServe(s.listen, mux)
}

// --- Tool routes ---

func (s *Server) handleGetToolSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	schemas := tools.AllToolSchemas(s.display)
	writeJSON(w, http.StatusOK, schemas)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

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

	vmName, err := s.resolveVM(r)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "vm_unavailable",
			"message": err.Error(),
		})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": "failed to read request body",
		})
		return
	}

	action, err := adapter.ParseToolCall(json.RawMessage(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": err.Error(),
		})
		return
	}

	result, err := s.executeAction(r, vmName, action)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}

	// Content negotiation: Accept: image/png for raw screenshot bytes
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

func (s *Server) handleBatchAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

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

	vmName, err := s.resolveVM(r)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "vm_unavailable",
			"message": err.Error(),
		})
		return
	}

	var batch []json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": "expected JSON array",
		})
		return
	}

	results := make([]json.RawMessage, 0, len(batch))
	for _, raw := range batch {
		action, err := adapter.ParseToolCall(raw)
		if err != nil {
			errJSON, _ := json.Marshal(map[string]any{
				"error":   "bad_request",
				"message": err.Error(),
			})
			results = append(results, errJSON)
			continue
		}

		result, err := s.executeAction(r, vmName, action)
		if err != nil {
			errJSON, _ := json.Marshal(map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			results = append(results, errJSON)
			continue
		}

		formatted, err := adapter.FormatResult(action, result)
		if err != nil {
			errJSON, _ := json.Marshal(map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			results = append(results, errJSON)
			continue
		}

		results = append(results, formatted)
	}

	writeJSON(w, http.StatusOK, results)
}

// --- VM lifecycle routes ---

func (s *Server) handleVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	switch r.Method {
	case http.MethodGet:
		// List VMs
		vms, err := s.manager.List(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		if vms == nil {
			vms = []vbox.VMStatus{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"vms": vms})

	case http.MethodPost:
		// Create VM
		var req struct {
			Name string `json:"name,omitempty"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		name := req.Name
		if name == "" {
			name = fmt.Sprintf("dexbox-%s", randomSuffix())
		}

		if vbox.VMExists(ctx, name) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "vm_exists",
				"message": fmt.Sprintf("VM %q already exists. Use a different name or DELETE /vm/%s first.", name, name),
			})
			return
		}

		if err := s.manager.Create(ctx, name, vbox.DefaultVMConfig()); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}

		if err := s.manager.Start(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"name":  name,
			"state": "running",
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVMNamed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse /vm/{name}[/action]
	path := strings.TrimPrefix(r.URL.Path, "/vm/")
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	if name == "" {
		http.NotFound(w, r)
		return
	}

	if !vbox.VMExists(ctx, name) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":   "vm_not_found",
			"message": fmt.Sprintf("VM %q does not exist", name),
		})
		return
	}

	switch {
	case r.Method == http.MethodDelete && action == "":
		if err := s.manager.Delete(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "deleted"})

	case r.Method == http.MethodGet && action == "status":
		status, err := s.manager.Status(ctx, name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, status)

	case r.Method == http.MethodPost && action == "start":
		if err := s.manager.Start(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "running"})

	case r.Method == http.MethodPost && action == "stop":
		if err := s.manager.Stop(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "poweroff"})

	case r.Method == http.MethodPost && action == "pause":
		if err := s.manager.Pause(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "paused"})

	case r.Method == http.MethodPost && action == "suspend":
		if err := s.manager.Suspend(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "saved"})

	case r.Method == http.MethodPost && action == "resume":
		if err := s.manager.Resume(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "running"})

	default:
		http.NotFound(w, r)
	}
}

// --- Internal helpers ---

func (s *Server) resolveVM(r *http.Request) (string, error) {
	vmName := r.URL.Query().Get("vm")
	if vmName != "" {
		state, err := vbox.VMState(r.Context(), vmName)
		if err != nil {
			return "", fmt.Errorf("VM %q not found", vmName)
		}
		if state != "running" {
			return "", fmt.Errorf("VM %q is not running (state: %s). Start it with POST /vm/%s/start", vmName, state, vmName)
		}
		return vmName, nil
	}

	// Auto-resolve: find running VMs
	vms, err := s.manager.List(r.Context())
	if err != nil {
		return "", fmt.Errorf("failed to list VMs: %w", err)
	}

	var running []string
	for _, vm := range vms {
		if vm.State == "running" {
			running = append(running, vm.Name)
		}
	}

	if len(running) == 0 {
		return "", fmt.Errorf("no running VMs. Create one with POST /vm")
	}
	if len(running) > 1 {
		return "", fmt.Errorf("multiple running VMs found (%s), specify ?vm=<name>", strings.Join(running, ", "))
	}
	return running[0], nil
}

func (s *Server) executeAction(r *http.Request, vmName string, action *tools.CanonicalAction) (*tools.CanonicalResult, error) {
	switch action.Tool {
	case "computer":
		ct, err := s.getComputerTool(r, vmName)
		if err != nil {
			return nil, err
		}
		result, err := ct.Execute(r.Context(), action)
		if err != nil && strings.Contains(err.Error(), "Invalid managed object reference") {
			// SOAP session is stale (e.g. VM rebooted). Reconnect and retry once.
			log.Printf("SOAP session stale for VM %q, reconnecting...", vmName)
			if reconnErr := s.reconnectComputerTool(r, vmName); reconnErr != nil {
				return nil, fmt.Errorf("reconnect failed: %w (original: %v)", reconnErr, err)
			}
			ct, err = s.getComputerTool(r, vmName)
			if err != nil {
				return nil, err
			}
			return ct.Execute(r.Context(), action)
		}
		return result, err
	case "bash":
		var p tools.BashParams
		if err := action.UnmarshalParams(&p); err != nil {
			return nil, fmt.Errorf("invalid bash params: %w", err)
		}
		bt := s.getBashTool(vmName)
		out, err := bt.Execute(r.Context(), p.Command, 2*time.Minute)
		if err != nil {
			return nil, err
		}
		return &tools.CanonicalResult{Output: out}, nil
	case "text_editor":
		et := s.getEditorTool(vmName)
		return et.Execute(r.Context(), action)
	default:
		return nil, fmt.Errorf("unknown tool %q", action.Tool)
	}
}

func (s *Server) getComputerTool(r *http.Request, vmName string) (*tools.ComputerTool, error) {
	s.mu.RLock()
	if ct, ok := s.computers[vmName]; ok {
		s.mu.RUnlock()
		return ct, nil
	}
	s.mu.RUnlock()

	if s.manager.SOAPClient(vmName) == nil {
		if err := s.manager.ConnectSOAP(r.Context(), vmName); err != nil {
			return nil, fmt.Errorf("SOAP connect failed: %w", err)
		}
	}

	// Sync VM display resolution to match the tool's coordinate space.
	// This ensures Claude's click coordinates map 1:1 to VM pixels.
	if err := vbox.SetVideoMode(r.Context(), vmName, s.display.Width, s.display.Height, 32); err != nil {
		log.Printf("Warning: failed to set VM display resolution to %dx%d: %v", s.display.Width, s.display.Height, err)
	}

	ct := tools.NewComputerTool(vmName, s.display.Width, s.display.Height, s.manager.SOAPClient(vmName))
	s.mu.Lock()
	s.computers[vmName] = ct
	s.mu.Unlock()
	return ct, nil
}

// reconnectComputerTool forces a fresh SOAP session and invalidates the cached tool.
// The caller should use getComputerTool to rebuild it (which also runs SetVideoMode).
func (s *Server) reconnectComputerTool(r *http.Request, vmName string) error {
	s.mu.Lock()
	delete(s.computers, vmName)
	s.mu.Unlock()
	return s.manager.ReconnectSOAP(r.Context(), vmName)
}

func (s *Server) getBashTool(vmName string) *tools.BashTool {
	s.mu.RLock()
	if bt, ok := s.bashes[vmName]; ok {
		s.mu.RUnlock()
		return bt
	}
	s.mu.RUnlock()

	bt := tools.NewBashTool(vmName, s.vmUser, s.vmPass)
	s.mu.Lock()
	s.bashes[vmName] = bt
	s.mu.Unlock()
	return bt
}

func (s *Server) getEditorTool(vmName string) *tools.EditorTool {
	s.mu.RLock()
	if et, ok := s.editors[vmName]; ok {
		s.mu.RUnlock()
		return et
	}
	s.mu.RUnlock()

	et := tools.NewEditorTool(vmName, s.vmUser, s.vmPass, s.shared)
	s.mu.Lock()
	s.editors[vmName] = et
	s.mu.Unlock()
	return et
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func randomSuffix() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()&0xffffff)
}
