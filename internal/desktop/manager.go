package desktop

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/getnenai/dexbox/internal/guacd"
	"github.com/getnenai/dexbox/internal/vbox"
)

// SessionEventType indicates whether an agent connected or disconnected.
type SessionEventType int

const (
	SessionUp   SessionEventType = iota
	SessionDown
)

// SessionEvent is sent on subscriber channels when an RDP agent for a named
// desktop transitions between connected and disconnected.
type SessionEvent struct {
	Type SessionEventType
	Name string
}

// DesktopStatus describes the state of any desktop (VM or RDP).
type DesktopStatus struct {
	Name      string `json:"name"`
	Type      string `json:"type"`      // "vm" or "rdp"
	State     string `json:"state"`     // "running", "connected", "disconnected", "poweroff", etc.
	Connected bool   `json:"connected"` // has active tool session
}

// Manager holds active desktop sessions of all types.
type Manager struct {
	vbox      *vbox.Manager
	store     *ConnectionStore
	guacdAddr string

	sessions map[string]Desktop
	mu       sync.Mutex

	subs   map[string][]chan SessionEvent
	subsMu sync.Mutex
}

// NewManager creates a unified desktop manager.
func NewManager(vboxMgr *vbox.Manager, store *ConnectionStore, guacdAddr string) *Manager {
	return &Manager{
		vbox:      vboxMgr,
		store:     store,
		guacdAddr: guacdAddr,
		sessions:  make(map[string]Desktop),
		subs:      make(map[string][]chan SessionEvent),
	}
}

// VBoxManager returns the underlying VBox manager (for VM-specific operations).
func (m *Manager) VBoxManager() *vbox.Manager {
	return m.vbox
}

// Store returns the RDP connection store.
func (m *Manager) Store() *ConnectionStore {
	return m.store
}

// Up brings a desktop online. For VMs: boots if needed + connects SOAP.
// For RDP: verifies guacd is running and the target is reachable.
//
// RDP sessions are ephemeral — each tool invocation (dexbox run) connects
// on demand. Up just confirms the target is reachable so that subsequent
// tool calls succeed. The in-memory session is used when Up is called from
// within the same process (e.g. dexbox run, which calls Up then uses the
// session immediately before the process exits).
func (m *Manager) Up(ctx context.Context, name string) error {
	m.mu.Lock()
	if _, ok := m.sessions[name]; ok {
		m.mu.Unlock()
		return nil // already connected within this process
	}
	// Keep lock held during connection setup to prevent races
	defer m.mu.Unlock()

	// Try as VM first
	if vbox.VMExists(ctx, name) {
		if err := m.vbox.Start(ctx, name); err != nil {
			return err
		}
		d := NewVBox(name, m.vbox)
		m.sessions[name] = d
		return nil
	}

	// Try as RDP connection
	cfg, ok := m.store.Get(name)
	if !ok {
		return fmt.Errorf("desktop %q not found (not a VM and not an RDP connection)", name)
	}

	if err := guacd.EnsureRunning(ctx); err != nil {
		return fmt.Errorf("guacd required for RDP: %w", err)
	}

	d := NewBringRDP(name, cfg, m.guacdAddr)
	if err := d.Connect(ctx); err != nil {
		return err
	}

	// m.mu is already held (acquired at entry, released via defer).
	// The VM path above stores its session the same way.
	m.sessions[name] = d
	m.notify(name, SessionUp)
	return nil
}

// rdpReachable returns true if a TCP connection to host:port succeeds quickly.
// "host.docker.internal" is a Docker-internal alias for the host machine; when
// the check runs on the host itself that name won't resolve, so we also try
// localhost as a fallback.
func rdpReachable(host string, port int) bool {
	candidates := []string{host}
	if host == "host.docker.internal" {
		candidates = append(candidates, "localhost")
	}
	for _, h := range candidates {
		addr := fmt.Sprintf("%s:%d", h, port)
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// Down disconnects a desktop session. If shutdown is true and the desktop is a
// VM, it also powers off the VM (ACPI or force).
func (m *Manager) Down(ctx context.Context, name string, shutdown bool, force bool) error {
	m.mu.Lock()
	d, ok := m.sessions[name]
	if ok {
		delete(m.sessions, name)
	}
	m.mu.Unlock()

	if ok {
		_ = d.Disconnect()
		if d.Type() == "rdp" {
			m.notify(name, SessionDown)
		}
	}

	if !shutdown {
		return nil
	}

	// Shutdown is VM-only
	if !vbox.VMExists(ctx, name) {
		if ok && d.Type() == "rdp" {
			return fmt.Errorf("cannot shutdown an RDP target; use 'down' to disconnect")
		}
		return fmt.Errorf("desktop %q not found", name)
	}

	if force {
		return m.vbox.Poweroff(ctx, name)
	}
	return m.vbox.Stop(ctx, name)
}

// DownAll disconnects all sessions, shuts down all running VMs, and stops guacd.
func (m *Manager) DownAll(ctx context.Context, force bool) error {
	m.mu.Lock()
	sessions := make(map[string]Desktop, len(m.sessions))
	for k, v := range m.sessions {
		sessions[k] = v
	}
	m.sessions = make(map[string]Desktop)
	m.mu.Unlock()

	// Disconnect all sessions
	for name, d := range sessions {
		_ = d.Disconnect()
		if d.Type() == "rdp" {
			m.notify(name, SessionDown)
		}
	}

	// Shutdown all running VMs
	vms, err := m.vbox.List(ctx)
	if err == nil {
		for _, vm := range vms {
			if vm.State == "running" {
				if force {
					_ = m.vbox.Poweroff(ctx, vm.Name)
				} else {
					_ = m.vbox.Stop(ctx, vm.Name)
				}
			}
		}
	}

	// Stop guacd
	_ = guacd.Stop(ctx)

	return nil
}

// SetSession registers (or replaces) a desktop session by name.
// Primarily useful for testing and for callers that manage connections
// outside the normal Up() flow.
func (m *Manager) SetSession(name string, d Desktop) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[name] = d
}

// Get returns an active desktop session by name.
func (m *Manager) Get(name string) (Desktop, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.sessions[name]
	return d, ok
}

// Subscribe returns a channel that receives a SessionEvent whenever the named
// desktop's agent session transitions up or down. The returned cancel function
// must be called to release resources when the caller is done listening.
func (m *Manager) Subscribe(name string) (<-chan SessionEvent, func()) {
	ch := make(chan SessionEvent, 1)
	m.subsMu.Lock()
	m.subs[name] = append(m.subs[name], ch)
	m.subsMu.Unlock()
	cancel := func() {
		m.subsMu.Lock()
		defer m.subsMu.Unlock()
		chans := m.subs[name]
		for i, c := range chans {
			if c == ch {
				m.subs[name] = append(chans[:i], chans[i+1:]...)
				close(ch) // signal consumers that no more events will arrive
				return
			}
		}
	}
	return ch, cancel
}

// ActiveRDP returns the active *BringRDP session for the named desktop, if any.
func (m *Manager) ActiveRDP(name string) (*BringRDP, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.sessions[name]
	if !ok {
		return nil, false
	}
	r, ok := d.(*BringRDP)
	return r, ok
}

// RDPConfig returns the stored connection configuration for the named RDP desktop.
func (m *Manager) RDPConfig(name string) (RDPConfig, bool) {
	return m.store.Get(name)
}

// notify sends a SessionEvent to all subscribers for the named desktop.
// It is non-blocking: a slow subscriber that has not drained its channel
// will simply miss the event (the channel is capped at 1).
func (m *Manager) notify(name string, t SessionEventType) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for _, ch := range m.subs[name] {
		select {
		case ch <- SessionEvent{Type: t, Name: name}:
		default:
		}
	}
}

// Resolve returns an active, connected desktop for tool execution.
// If name is empty, auto-resolves when exactly one desktop is connected.
func (m *Manager) Resolve(name string) (Desktop, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if name != "" {
		d, ok := m.sessions[name]
		if !ok {
			return nil, fmt.Errorf("desktop %q is not connected; run 'dexbox up %s' first", name, name)
		}
		return d, nil
	}

	// Auto-resolve
	if len(m.sessions) == 0 {
		return nil, fmt.Errorf("no connected desktops")
	}
	if len(m.sessions) > 1 {
		names := make([]string, 0, len(m.sessions))
		for n := range m.sessions {
			names = append(names, n)
		}
		return nil, fmt.Errorf("multiple connected desktops, specify a name: %v", names)
	}
	for _, d := range m.sessions {
		return d, nil
	}
	return nil, fmt.Errorf("no connected desktops") // unreachable
}

// List returns the status of all known desktops (VMs + RDP connections).
func (m *Manager) List(ctx context.Context, typeFilter string) ([]DesktopStatus, error) {
	var result []DesktopStatus

	m.mu.Lock()
	sessions := make(map[string]Desktop, len(m.sessions))
	for k, v := range m.sessions {
		sessions[k] = v
	}
	m.mu.Unlock()

	// VMs
	if (typeFilter == "" || typeFilter == "vm") && m.vbox != nil {
		vms, err := m.vbox.List(ctx)
		if err == nil {
			for _, vm := range vms {
				_, connected := sessions[vm.Name]
				result = append(result, DesktopStatus{
					Name:      vm.Name,
					Type:      "vm",
					State:     vm.State,
					Connected: connected,
				})
			}
		}
	}

	// RDP connections
	if typeFilter == "" || typeFilter == "rdp" {
		for name, cfg := range m.store.List() {
			d, connected := sessions[name]
			var state string
			switch {
			case connected && d.Connected():
				state = "connected"
			case rdpReachable(cfg.Host, cfg.Port):
				state = "reachable"
			default:
				state = "unreachable"
			}
			result = append(result, DesktopStatus{
				Name:      name,
				Type:      "rdp",
				State:     state,
				Connected: connected,
			})
		}
	}

	return result, nil
}
