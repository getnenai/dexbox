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
	mux.HandleFunc("GET /tools", s.handleGetToolSchema)
	mux.HandleFunc("POST /actions", s.handleAction)
	mux.HandleFunc("POST /actions/batch", s.handleBatchAction)

	// Desktop routes
	mux.HandleFunc("GET /desktops", s.handleListDesktops)
	mux.HandleFunc("POST /desktops", s.handleCreateDesktop)
	mux.HandleFunc("POST /desktops/down-all", s.handleDownAll)
	mux.HandleFunc("GET /desktops/{name}", s.handleGetDesktop)
	mux.HandleFunc("POST /desktops/{name}", s.handleDesktopAction)
	mux.HandleFunc("DELETE /desktops/{name}", s.handleDeleteDesktop)

	// Browser UI (view/tunnel).
	// Reuse the store from the desktop manager so API-created connections
	// are immediately visible in the browser UI.
	webHandler := web.Handler(s.desktops.Store(), s.manager, "localhost:4822")
	mux.HandleFunc("GET /desktops/{name}/view", func(w http.ResponseWriter, r *http.Request) {
		webHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /desktops/{name}/tunnel", func(w http.ResponseWriter, r *http.Request) {
		webHandler.ServeHTTP(w, r)
	})

	// Static assets
	mux.Handle("/static/", web.StaticHandler())

	// Health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"uptime_seconds": time.Since(startTime).Seconds(),
		})
	})

	// Auto-connect running VMs so agents can use them immediately.
	go func() {
		listCtx, listCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer listCancel()
		desktops, err := s.desktops.List(listCtx, "vm")
		if err != nil {
			log.Printf("auto-connect: failed to list VMs: %v", err)
			return
		}
		for _, d := range desktops {
			if d.State == "running" && !d.Connected {
				upCtx, upCancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := s.desktops.Up(upCtx, d.Name); err != nil {
					log.Printf("auto-connect %s: %v", d.Name, err)
				} else {
					log.Printf("auto-connected desktop %s", d.Name)
				}
				upCancel()
			}
		}
	}()

	log.Printf("dexbox tool server listening on %s", s.listen)
	return http.ListenAndServe(s.listen, mux)
}

// --- Tool routes ---

func (s *Server) handleGetToolSchema(w http.ResponseWriter, r *http.Request) {
	schemas := tools.AllToolSchemas(s.display)
	writeJSON(w, http.StatusOK, schemas)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {

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
			// Return the error as output (like a real shell) instead of
			// propagating it as an HTTP 500. This lets the agent see the
			// error message and decide how to recover.
			log.Printf("[bash] command failed: %v", err)
			return &tools.CanonicalResult{Output: fmt.Sprintf("error: %v", err)}, nil
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

func (s *Server) handleListDesktops(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleCreateDesktop(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Type     string `json:"type"` // "vm" or "rdp"
		Name     string `json:"name,omitempty"`
		Host     string `json:"host,omitempty"`
		Port     int    `json:"port,omitempty"`
		Username string `json:"username,omitempty"`
		Password string `json:"password,omitempty"`
		Width    int    `json:"width,omitempty"`
		Height   int    `json:"height,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": "invalid JSON body",
		})
		return
	}

	switch req.Type {
	case "rdp", "":
		s.createRDP(w, ctx, req.Name, desktop.RDPConfig{
			Host:     req.Host,
			Port:     req.Port,
			Username: req.Username,
			Password: req.Password,
			Width:    req.Width,
			Height:   req.Height,
		})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": fmt.Sprintf("unknown desktop type %q (use: rdp)", req.Type),
		})
	}
}

// createRDP handles POST /desktops with type=rdp.
func (s *Server) createRDP(w http.ResponseWriter, ctx context.Context, name string, cfg desktop.RDPConfig) {
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": "name is required for RDP connections",
		})
		return
	}
	if cfg.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": "host is required for RDP connections",
		})
		return
	}

	// Check for name collision with VMs.
	if vbox.VMExists(ctx, name) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "name_exists",
			"message": fmt.Sprintf("name %q is already used by a VM; choose a different name", name),
		})
		return
	}

	store := s.desktops.Store()
	if err := store.Add(name, cfg); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "name_exists",
			"message": err.Error(),
		})
		return
	}
	if err := store.Save(); err != nil {
		// Rollback: remove the entry we just added to keep memory
		// consistent with the on-disk state.
		_ = store.Remove(name)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}

	port := cfg.Port
	if port == 0 {
		port = 3389
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"name": name,
		"type": "rdp",
		"host": fmt.Sprintf("%s:%d", cfg.Host, port),
	})
}

func (s *Server) handleDownAll(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if err := s.desktops.DownAll(r.Context(), force); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "all_down"})
}

func (s *Server) handleGetDesktop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.getDesktopStatus(w, r.Context(), name)
}

func (s *Server) handleDesktopAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")
	action := r.URL.Query().Get("action")

	switch action {
	case "up":
		if err := s.desktops.Up(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "status": "up"})

	case "down":
		shutdown := r.URL.Query().Get("shutdown") == "true"
		force := r.URL.Query().Get("force") == "true"
		if err := s.desktops.Down(ctx, name, shutdown, force); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		s.clearToolCache(name)
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "status": "down"})

	case "pause":
		if !s.requireVBox(w) {
			return
		}
		if err := s.manager.Pause(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "paused"})

	case "suspend":
		if !s.requireVBox(w) {
			return
		}
		if err := s.manager.Suspend(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		_ = s.desktops.Down(ctx, name, false, false)
		s.clearToolCache(name)
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "saved"})

	case "resume":
		if !s.requireVBox(w) {
			return
		}
		if err := s.manager.Resume(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "state": "running"})

	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": fmt.Sprintf("unknown action %q; supported: up, down, pause, suspend, resume", action),
		})
	}
}

func (s *Server) handleDeleteDesktop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.deleteDesktop(w, r.Context(), name)
}

// getDesktopStatus returns the status of a single desktop.
func (s *Server) getDesktopStatus(w http.ResponseWriter, ctx context.Context, name string) {
	desktops, err := s.desktops.List(ctx, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}
	for _, d := range desktops {
		if d.Name == name {
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error":   "not_found",
		"message": fmt.Sprintf("desktop %q not found", name),
	})
}

// deleteDesktop removes a desktop (VM or RDP) and disconnects any active session.
func (s *Server) deleteDesktop(w http.ResponseWriter, ctx context.Context, name string) {
	// Disconnect any active session first.
	_ = s.desktops.Down(ctx, name, false, false)

	// Clean up cached tools for this desktop.
	s.clearToolCache(name)

	// Try as VM first.
	if vbox.VMExists(ctx, name) {
		if !s.requireVBox(w) {
			return
		}
		if err := s.manager.Delete(ctx, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "tool_error",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "type": "vm", "state": "deleted"})
		return
	}

	// Try as RDP connection.
	store := s.desktops.Store()
	// Capture the config before removing so we can rollback on Save failure.
	oldCfg, existed := store.Get(name)
	if err := store.Remove(name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":   "not_found",
			"message": fmt.Sprintf("desktop %q not found (not a VM and not an RDP connection)", name),
		})
		return
	}
	if err := store.Save(); err != nil {
		// Rollback: re-insert the removed entry.
		if existed {
			_ = store.Add(name, oldCfg)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "tool_error",
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "type": "rdp", "state": "deleted"})
}

// requireVBox writes a 503 response and returns false when the VBox manager
// is not configured. Callers should return immediately when it returns false.
func (s *Server) requireVBox(w http.ResponseWriter) bool {
	if s.manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "no_vbox_manager",
			"message": "VM control is not available",
		})
		return false
	}
	return true
}

// clearToolCache removes cached ComputerTool and BashTool instances for a desktop.
func (s *Server) clearToolCache(name string) {
	s.toolsMu.Lock()
	delete(s.computers, name)
	delete(s.computerDskt, name)
	delete(s.bashes, name)
	s.toolsMu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func randomSuffix() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()&0xffffff)
}
