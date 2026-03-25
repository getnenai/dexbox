package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/getnenai/dexbox/internal/desktop"
	"github.com/getnenai/dexbox/internal/tools"
	"github.com/getnenai/dexbox/internal/vbox"
	"github.com/getnenai/dexbox/internal/web"
)

var errUnknownTool = errors.New("unknown tool")

// Server is the dexbox tool server.
type Server struct {
	desktops *desktop.Manager
	manager  *vbox.Manager // kept for VM-specific routes
	listen   string
	vmUser   string
	vmPass   string
	display  tools.DisplayConfig
	shared   string

	// Per-desktop tool instances (lazily created)
	toolsMu      sync.RWMutex
	computers    map[string]*tools.ComputerTool
	computerDskt map[string]desktop.Desktop // desktop used to create each cached ComputerTool
	bashes       map[string]*tools.BashTool

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
	Desktops   *desktop.Manager // optional; created from other opts if nil
}

// New creates a new tool server.
func New(opts Options) *Server {
	mgr := vbox.NewManager(opts.SOAPAddr, opts.VMUser, opts.VMPass)

	var desktops *desktop.Manager
	if opts.Desktops != nil {
		desktops = opts.Desktops
	} else {
		store := desktop.NewConnectionStore(desktop.DefaultStorePath())
		_ = store.Load()
		desktops = desktop.NewManager(mgr, store, "localhost:4822")
	}

	return &Server{
		desktops:  desktops,
		manager:   mgr,
		listen:    opts.ListenAddr,
		vmUser:    opts.VMUser,
		vmPass:    opts.VMPass,
		display:   tools.DisplayConfig{Width: opts.Width, Height: opts.Height},
		shared:    opts.SharedDir,
		computers:    make(map[string]*tools.ComputerTool),
		computerDskt: make(map[string]desktop.Desktop),
		bashes:       make(map[string]*tools.BashTool),

	}
}

// Manager returns the underlying VM manager.
func (s *Server) Manager() *vbox.Manager {
	return s.manager
}

// DesktopManager returns the unified desktop manager.
func (s *Server) DesktopManager() *desktop.Manager {
	return s.desktops
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	mux := http.NewServeMux()
	startTime := time.Now()

	// Tool routes
	mux.HandleFunc("/tools", s.handleGetToolSchema)
	mux.HandleFunc("/actions", s.handleAction)
	mux.HandleFunc("/actions/batch", s.handleBatchAction)

	// VM lifecycle routes (kept for backward compat)
	mux.HandleFunc("/vm", s.handleVM)
	mux.HandleFunc("/vm/", s.handleVMNamed)

	// Unified desktop routes
	mux.HandleFunc("/desktops", s.handleDesktops)

	// Browser UI (view/tunnel) and desktop API share the /desktops/ prefix
	store := desktop.NewConnectionStore(desktop.DefaultStorePath())
	_ = store.Load()
	webHandler := web.Handler(store, s.manager, "localhost:4822")

	mux.HandleFunc("/desktops/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/desktops/")
		parts := strings.SplitN(path, "/", 2)
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}
		// Route view/tunnel to web handler, everything else to desktop API
		if action == "view" || action == "tunnel" {
			webHandler.ServeHTTP(w, r)
		} else {
			s.handleDesktopNamed(w, r)
		}
	})

	// Static assets
	mux.Handle("/static/", web.StaticHandler())

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
		status := http.StatusInternalServerError
		code := "tool_error"
		if errors.Is(err, errUnknownTool) {
			status = http.StatusBadRequest
			code = "bad_request"
		}
		writeJSON(w, status, map[string]any{
			"error":   code,
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
			code := "tool_error"
			if errors.Is(err, errUnknownTool) {
				code = "bad_request"
			}
			errJSON, _ := json.Marshal(map[string]any{
				"error":   code,
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
	// Support both ?desktop= (new) and ?vm= (backward compat)
	name := r.URL.Query().Get("desktop")
	if name == "" {
		name = r.URL.Query().Get("vm")
	}

	d, err := s.desktops.Resolve(name)
	if err == nil {
		return d.Name(), nil
	}

	// If a specific name was requested but not found in desktop manager,
	// try VBox directly (for VMs not yet connected via desktop manager).
	if name != "" {
		state, verr := vbox.VMState(r.Context(), name)
		if verr != nil {
			return "", fmt.Errorf("desktop %q not found", name)
		}
		if state != "running" {
			return "", fmt.Errorf("desktop %q is not running (state: %s). Start it with POST /desktops/%s/up", name, state, name)
		}
		return name, nil
	}

	// No name specified and desktop manager has no desktops.
	// Auto-discover running VMs via VBoxManage as a last resort.
	vms, listErr := vbox.ListVMs(r.Context())
	if listErr == nil {
		var running []string
		for _, vm := range vms {
			state, stateErr := vbox.VMState(r.Context(), vm)
			if stateErr == nil && state == "running" {
				running = append(running, vm)
			}
		}
		switch len(running) {
		case 1:
			log.Printf("[resolveVM] auto-discovered running VM %q", running[0])
			return running[0], nil
		case 0:
			// fall through to return the original error
		default:
			return "", fmt.Errorf("multiple running VMs found (%s); specify ?vm=", strings.Join(running, ", "))
		}
	}

	return "", err
}

func (s *Server) executeAction(r *http.Request, vmName string, action *tools.CanonicalAction) (*tools.CanonicalResult, error) {
	switch action.Tool {
	case "computer":
		ct, err := s.getComputerTool(r, vmName)
		if err != nil {
			return nil, err
		}
		result, err := ct.Execute(r.Context(), action)
		if err != nil && vbox.IsStaleRefError(err) {
			// SOAP session is stale (e.g. VM rebooted, session timeout). Reconnect and retry once.
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

	default:
		return nil, fmt.Errorf("%w %q", errUnknownTool, action.Tool)
	}
}

func (s *Server) getComputerTool(r *http.Request, name string) (*tools.ComputerTool, error) {
	// Try to get from Desktop Manager first (supports both VM and RDP)
	d, ok := s.desktops.Get(name)
	if !ok {
		if s.manager == nil {
			return nil, fmt.Errorf("desktop %q is not connected; run 'dexbox up %s' first", name, name)
		}
		// Fall back to creating a VBox desktop (backward compat for VMs
		// that were started outside the desktop manager).
		vd := desktop.NewVBox(name, s.manager)
		if !vd.Connected() {
			if err := vd.Connect(r.Context()); err != nil {
				return nil, fmt.Errorf("desktop %q is not connected and SOAP fallback failed: %w", name, err)
			}
		}
		d = vd
	}

	// Reuse cached tool only if it was built for the same desktop instance.
	s.toolsMu.RLock()
	ct, cached := s.computers[name]
	cachedDskt := s.computerDskt[name]
	s.toolsMu.RUnlock()

	if cached && cachedDskt == d {
		return ct, nil
	}

	ct = tools.NewComputerTool(d, s.display.Width, s.display.Height)
	if d.Type() == "vm" {
		ct.GuestScroll = s.buildGuestScrollFunc(name)
	}
	s.toolsMu.Lock()
	s.computers[name] = ct
	s.computerDskt[name] = d
	s.toolsMu.Unlock()
	return ct, nil
}

// buildGuestScrollFunc returns a GuestScrollFunc that scrolls via
// PowerShell mouse_event inside the guest, bypassing broken SOAP mouse scroll.
//
// VBox's SOAP putMouseEvent/putMouseEventAbsolute dz parameter is silently
// dropped when Guest Additions is installed (known VBox bug). This workaround
// injects scroll events directly via Win32 mouse_event from inside the guest.
//
// VBoxManage guestcontrol spawns processes in a non-interactive window station,
// so we must first switch to WinSta0 (the interactive desktop) before calling
// SetCursorPos and mouse_event.
func (s *Server) buildGuestScrollFunc(vmName string) tools.GuestScrollFunc {
	return func(ctx context.Context, x, y, delta int) error {
		bt := s.getBashTool(vmName)

		// This script is copied verbatim from the tested debug_scroll3.py.
		// Do NOT change the formatting — the exact line breaks and quoting matter
		// for PowerShell's Add-Type -MemberDefinition parsing.
		script := fmt.Sprintf(`
Add-Type -MemberDefinition '
[DllImport("user32.dll", SetLastError = true)]
public static extern IntPtr OpenWindowStation(string lpszWinSta, bool fInherit, uint dwDesiredAccess);
[DllImport("user32.dll", SetLastError = true)]
public static extern bool SetProcessWindowStation(IntPtr hWinSta);
[DllImport("user32.dll", SetLastError = true)]
public static extern bool SetCursorPos(int X, int Y);
[DllImport("user32.dll")]
public static extern void mouse_event(uint dwFlags, int dx, int dy, int dwData, IntPtr dwExtraInfo);
' -Name W32 -Namespace Scroll -ErrorAction SilentlyContinue

$winsta = [Scroll.W32]::OpenWindowStation("WinSta0", $false, 0x37F)
if ($winsta -eq [IntPtr]::Zero) {
  throw "OpenWindowStation('WinSta0') failed (returned NULL)"
}
$ok = [Scroll.W32]::SetProcessWindowStation($winsta)
if (-not $ok) {
  $code = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
  throw "SetProcessWindowStation failed (Win32 error $code)"
}
$ok = [Scroll.W32]::SetCursorPos(%d, %d)
if (-not $ok) {
  $code = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
  throw "SetCursorPos(%d, %d) failed (Win32 error $code)"
}
Start-Sleep -Milliseconds 200
[Scroll.W32]::mouse_event(0x0800, 0, 0, %d, [IntPtr]::Zero)
`, x, y, x, y, delta)

		log.Printf("[scroll] sending script (%d bytes) to VM %q at (%d,%d) delta=%d", len(script), vmName, x, y, delta)
		out, err := bt.Execute(ctx, script, 15*time.Second)
		if err != nil {
			log.Printf("[scroll] error: %v", err)
		} else {
			log.Printf("[scroll] OK: %q", out)
		}
		return err
	}
}

// reconnectComputerTool forces a fresh SOAP session and invalidates the cached tool.
// The caller should use getComputerTool to rebuild it (which also runs SetVideoMode).
func (s *Server) reconnectComputerTool(r *http.Request, vmName string) error {
	s.toolsMu.Lock()
	delete(s.computers, vmName)
	delete(s.computerDskt, vmName)
	s.toolsMu.Unlock()
	return s.manager.ReconnectSOAP(r.Context(), vmName)
}

func (s *Server) getBashTool(vmName string) *tools.BashTool {
	s.toolsMu.RLock()
	bt, ok := s.bashes[vmName]
	s.toolsMu.RUnlock()
	if ok {
		return bt
	}
	bt = tools.NewBashTool(vmName, s.vmUser, s.vmPass)
	s.toolsMu.Lock()
	if existing, ok := s.bashes[vmName]; ok {
		s.toolsMu.Unlock()
		return existing
	}
	s.bashes[vmName] = bt
	s.toolsMu.Unlock()
	return bt
}



// --- Unified desktop routes ---

func (s *Server) handleDesktops(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	typeFilter := r.URL.Query().Get("type")
	desktops, err := s.desktops.List(r.Context(), typeFilter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"desktops": desktops})
}

func (s *Server) handleDesktopNamed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	path := strings.TrimPrefix(r.URL.Path, "/desktops/")
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

	// Special routes that don't require an existing desktop
	if name == "down-all" && r.Method == http.MethodPost {
		force := r.URL.Query().Get("force") == "true"
		if err := s.desktops.DownAll(ctx, force); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "all_down"})
		return
	}

	switch {
	case r.Method == http.MethodPost && action == "up":
		if err := s.desktops.Up(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "status": "up"})

	case r.Method == http.MethodPost && action == "down":
		shutdown := r.URL.Query().Get("shutdown") == "true"
		force := r.URL.Query().Get("force") == "true"
		if err := s.desktops.Down(ctx, name, shutdown, force); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "status": "down"})

	case r.Method == http.MethodPost && action == "shutdown":
		force := r.URL.Query().Get("force") == "true"
		if err := s.desktops.Down(ctx, name, true, force); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "status": "shutdown"})

	// Forward VM-only power commands
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func randomSuffix() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()&0xffffff)
}
