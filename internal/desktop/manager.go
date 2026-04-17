package desktop

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/getnenai/dexbox/internal/guacd"
	"github.com/getnenai/dexbox/internal/vbox"
)

// ErrDesktopNotFound is returned by EnsureRDPConnected when the named desktop
// has no stored RDP configuration. Callers can detect it with errors.Is to
// distinguish "config missing" (→ 404) from "connection failed" (→ 503).
// It is never returned proactively — only when a caller explicitly requests a
// named desktop that does not exist. If no RDP connections are configured,
// this error is never triggered.
var ErrDesktopNotFound = errors.New("desktop not found")

// SessionEventType indicates whether an agent connected or disconnected.
type SessionEventType int

const (
	SessionUp SessionEventType = iota
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
	sharedDir string

	idleDisconnectDelay time.Duration

	sessions map[string]Desktop
	mu       sync.Mutex

	subs   map[string][]chan SessionEvent
	subsMu sync.Mutex

	// Viewer ref-counting for idle-disconnect.
	viewerCount map[string]int
	idleTimers  map[string]idleTimer
	viewerMu    sync.Mutex

	// Per-desktop mutex serializes EnsureRDPConnected so concurrent tunnel
	// requests cannot each store a new *BringRDP and overwrite m.sessions[name]
	// before Connect completes.
	rdpConnectMu sync.Map // string -> *sync.Mutex
}

// NewManager creates a unified desktop manager.
func NewManager(vboxMgr *vbox.Manager, store *ConnectionStore, guacdAddr string, sharedDir string) *Manager {
	return &Manager{
		vbox:                vboxMgr,
		store:               store,
		guacdAddr:           guacdAddr,
		sharedDir:           sharedDir,
		idleDisconnectDelay: loadIdleDisconnectDelay(),
		sessions:            make(map[string]Desktop),
		subs:                make(map[string][]chan SessionEvent),
		viewerCount:         make(map[string]int),
		idleTimers:          make(map[string]idleTimer),
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

// Up brings a desktop online and, for RDP connections, claims agent control.
//
// For VMs: boots if needed and registers the session.
// For RDP: ensures a live guacd session exists (connecting on demand if needed),
// then marks the session as agent-active so the web viewer switches to read-only.
func (m *Manager) Up(ctx context.Context, name string) error {
	// --- VM path ---
	if vbox.VMExists(ctx, name) {
		m.mu.Lock()
		if _, ok := m.sessions[name]; ok {
			m.mu.Unlock()
			return nil // already up
		}
		m.mu.Unlock()

		if err := m.vbox.Start(ctx, name); err != nil {
			return err
		}
		d := NewVBox(name, m.vbox)
		m.mu.Lock()
		m.sessions[name] = d
		m.mu.Unlock()
		return nil
	}

	// --- RDP path ---
	if _, ok := m.store.Get(name); !ok {
		return fmt.Errorf("desktop %q not found (not a VM and not an RDP connection)", name)
	}

	// Connect on demand if not already live.
	if err := m.EnsureRDPConnected(ctx, name); err != nil {
		return err
	}

	m.mu.Lock()
	rdp, _ := m.sessions[name].(*BringRDP)
	m.mu.Unlock()

	if rdp == nil {
		return fmt.Errorf("desktop %q: session not available after connect", name)
	}

	rdp.SetAgentActive(true)

	// Cancel any pending idle-disconnect since an agent is now active.
	m.viewerMu.Lock()
	if it, ok := m.idleTimers[name]; ok {
		it.timer.Stop()
		delete(m.idleTimers, name)
	}
	m.viewerMu.Unlock()

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
		addr := net.JoinHostPort(h, fmt.Sprintf("%d", port))
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// Down releases agent control of a desktop.
//
// For RDP: clears the agent-active flag and fires SessionDown so the web
// viewer regains interactive control. The underlying guacd/RDP connection is
// kept alive — only explicit server shutdown (DownAll) tears it down.
//
// For VMs: disconnects and removes the session. If shutdown is true, also
// powers off the VM.
func (m *Manager) Down(ctx context.Context, name string, shutdown bool, force bool) error {
	// VM-only shutdown validation first: avoid mutating RDP session state on the
	// error path when the caller asked to power off a non-VM / missing desktop.
	if shutdown {
		if !vbox.VMExists(ctx, name) {
			m.mu.Lock()
			var hadRDP bool
			if d, ok := m.sessions[name]; ok {
				hadRDP = d.Type() == "rdp"
			}
			m.mu.Unlock()
			if hadRDP {
				return fmt.Errorf("cannot shutdown an RDP target; use 'down' to disconnect")
			}
			return fmt.Errorf("desktop %q not found", name)
		}
	}

	m.mu.Lock()
	d, ok := m.sessions[name]
	m.mu.Unlock()

	if ok {
		if d.Type() == "rdp" {
			if rdp, isBringRDP := d.(*BringRDP); isBringRDP {
				// Persistent RDP session: release agent control but keep the
				// guacd connection alive so the web viewer can rejoin.
				rdp.SetAgentActive(false)
				// Schedule an idle disconnect if no viewers are watching.
				// We call scheduleIdleDisconnectIfIdle directly rather than
				// ViewerDisconnected because no viewer actually left — only
				// the agent released control.
				m.scheduleIdleDisconnectIfIdle(name)
			} else {
				// Non-persistent RDP (e.g. test double): disconnect and remove.
				m.mu.Lock()
				delete(m.sessions, name)
				m.mu.Unlock()
				_ = d.Disconnect()
			}
			m.notify(name, SessionDown)
		} else {
			// VM: disconnect and remove from sessions.
			m.mu.Lock()
			delete(m.sessions, name)
			m.mu.Unlock()
			_ = d.Disconnect()
		}
	}

	if !shutdown {
		return nil
	}

	// Shutdown is VM-only — VMExists was checked at the top when shutdown is true.
	if force {
		return m.vbox.Poweroff(ctx, name)
	}
	return m.vbox.Stop(ctx, name)
}

// DownAll disconnects all sessions, shuts down all running VMs, and stops guacd.
// Unlike Down, this fully tears down RDP connections (used on server shutdown).
func (m *Manager) DownAll(ctx context.Context, force bool) error {
	m.mu.Lock()
	sessions := make(map[string]Desktop, len(m.sessions))
	for k, v := range m.sessions {
		sessions[k] = v
	}
	m.sessions = make(map[string]Desktop)
	m.mu.Unlock()

	// Cancel all pending idle-disconnect timers so they don't fire after
	// shutdown. Drain and clear the map under viewerMu, then stop each timer
	// outside the lock.
	m.viewerMu.Lock()
	pendingTimers := make([]idleTimer, 0, len(m.idleTimers))
	for _, it := range m.idleTimers {
		pendingTimers = append(pendingTimers, it)
	}
	m.idleTimers = make(map[string]idleTimer)
	m.viewerMu.Unlock()
	for _, it := range pendingTimers {
		it.timer.Stop()
	}

	// Fully disconnect all sessions (including RDP).
	for name, d := range sessions {
		if rdp, ok := d.(*BringRDP); ok {
			rdp.SetAgentActive(false)
		}
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

// rdpConnectLock returns a per-desktop mutex used to serialize Connect for a
// given RDP name so concurrent EnsureRDPConnected callers wait instead of
// overwriting m.sessions[name] before the first dial finishes.
func (m *Manager) rdpConnectLock(name string) *sync.Mutex {
	v, _ := m.rdpConnectMu.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// EnsureRDPConnected connects the named RDP desktop to guacd if a live session
// does not already exist. It is called on first use — either by the web viewer
// opening a tunnel or by Up() when the session is not yet live.
// Callers that need the connection ID should read it after this returns.
func (m *Manager) EnsureRDPConnected(ctx context.Context, name string) error {
	cl := m.rdpConnectLock(name)
	cl.Lock()
	defer cl.Unlock()

	// Fast path: already connected.
	if rdp, ok := m.ActiveRDP(name); ok && rdp.GuacdConnectionID() != "" {
		return nil
	}

	cfg, ok := m.store.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrDesktopNotFound, name)
	}

	if err := guacd.EnsureRunning(ctx, m.sharedDir); err != nil {
		return fmt.Errorf("guacd required for RDP: %w", err)
	}

	// Under rdpConnectLock, only one goroutine reaches here per name at a time.
	m.mu.Lock()
	if existing, exists := m.sessions[name]; exists {
		if rdp, ok := existing.(*BringRDP); ok && rdp.Connected() {
			m.mu.Unlock()
			return nil
		}
	}
	d := NewBringRDP(name, cfg, m.guacdAddr)
	m.sessions[name] = d
	m.mu.Unlock()

	if err := d.Connect(ctx); err != nil {
		m.mu.Lock()
		// Only remove our entry; a concurrent goroutine might have replaced it.
		if m.sessions[name] == d {
			delete(m.sessions, name)
		}
		m.mu.Unlock()
		return err
	}

	log.Printf("[manager] connected %s (on-demand), connID=%s", name, d.GuacdConnectionID())
	return nil
}

// idleTimer pairs the time.Timer with the *BringRDP session it was created
// for. disconnectIdle uses this to guard against a TOCTOU where a new session
// is created between the timer being scheduled and it firing: if the current
// session pointer no longer matches, the timer is a no-op for the new session.
type idleTimer struct {
	timer   *time.Timer
	session *BringRDP
}

// defaultIdleDisconnectDelay is how long the manager waits after the last
// viewer/agent has left before tearing down a persistent RDP session. Five
// minutes is chosen for agent-paced workflows: a CUA pausing for LLM
// generation can easily go 30-60s between tool calls, and even multi-minute
// analysis pauses. Shorter values (the previous 30s) caused the RDP session
// to drop between tool calls, which on Windows triggers the disconnect-lock
// behaviour and surfaces LogonUI on the next screenshot.
const defaultIdleDisconnectDelay = 300 * time.Second

// minIdleDisconnectDelay gates pathologically-low values configured via the
// environment. Anything below this floor would reintroduce the disconnect
// races the default is meant to avoid.
const minIdleDisconnectDelay = 30 * time.Second

func loadIdleDisconnectDelay() time.Duration {
	raw, ok := os.LookupEnv("DEXBOX_IDLE_DISCONNECT_SECONDS")
	if !ok {
		return defaultIdleDisconnectDelay
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		log.Printf("[manager] invalid DEXBOX_IDLE_DISCONNECT_SECONDS=%q; using default %v", raw, defaultIdleDisconnectDelay)
		return defaultIdleDisconnectDelay
	}
	d := time.Duration(secs) * time.Second
	if d < minIdleDisconnectDelay {
		log.Printf("[manager] DEXBOX_IDLE_DISCONNECT_SECONDS=%d below floor %v; clamping", secs, minIdleDisconnectDelay)
		return minIdleDisconnectDelay
	}
	return d
}

// ViewerConnected is called when a web viewer successfully opens a tunnel for
// the named desktop. It increments the viewer ref-count and cancels any pending
// idle-disconnect timer.
func (m *Manager) ViewerConnected(name string) {
	m.viewerMu.Lock()
	defer m.viewerMu.Unlock()

	m.viewerCount[name]++
	if it, ok := m.idleTimers[name]; ok {
		it.timer.Stop()
		delete(m.idleTimers, name)
	}
	log.Printf("[manager] viewer connected %s (viewers=%d)", name, m.viewerCount[name])
}

// ViewerDisconnected is called when a web viewer's tunnel closes. It decrements
// the viewer ref-count; if no viewers remain and no agent is active, it
// schedules a disconnect after idleDisconnectDelay.
func (m *Manager) ViewerDisconnected(name string) {
	m.viewerMu.Lock()
	if m.viewerCount[name] > 0 {
		m.viewerCount[name]--
	}
	count := m.viewerCount[name]
	log.Printf("[manager] viewer disconnected %s (viewers=%d)", name, count)
	m.viewerMu.Unlock()

	if count > 0 {
		return
	}
	m.scheduleIdleDisconnectIfIdle(name)
}

// scheduleIdleDisconnectIfIdle schedules an idle disconnect for the named
// desktop if no viewers are connected and no agent is active. It is called by
// ViewerDisconnected (after viewer count reaches zero) and by Down (after the
// agent releases a persistent RDP session). Separating it from
// ViewerDisconnected avoids confusing "viewer disconnected" semantics in the
// Down path.
func (m *Manager) scheduleIdleDisconnectIfIdle(name string) {
	// Check agent status without holding viewerMu to avoid a viewerMu→mu
	// lock-ordering dependency (IsAgentActive acquires mu).
	if m.IsAgentActive(name) {
		return
	}

	// Capture the current session pointer so disconnectIdle can guard against
	// a TOCTOU where a new session is established before the timer fires.
	m.mu.Lock()
	var rdpSession *BringRDP
	if d, ok := m.sessions[name]; ok {
		rdpSession, _ = d.(*BringRDP)
	}
	m.mu.Unlock()

	// Re-check viewer count and any existing timer under viewerMu. Agent state
	// is not re-checked here — disconnectIdle always re-checks before acting,
	// so a spuriously scheduled timer is harmless.
	m.viewerMu.Lock()
	defer m.viewerMu.Unlock()

	if m.viewerCount[name] > 0 {
		return
	}
	if _, pending := m.idleTimers[name]; pending {
		return
	}
	log.Printf("[manager] scheduling idle disconnect for %s in %v", name, m.idleDisconnectDelay)
	session := rdpSession
	m.idleTimers[name] = idleTimer{
		timer: time.AfterFunc(m.idleDisconnectDelay, func() {
			m.disconnectIdle(name)
		}),
		session: session,
	}
}

// disconnectIdle tears down the RDP connection for a desktop that has had no
// viewers and no active agent for idleDisconnectDelay.
// It guards against a TOCTOU by comparing the session pointer stored in the
// idleTimer against the current session: if a new session was established
// between scheduling and firing, the pointers differ and we do nothing.
func (m *Manager) disconnectIdle(name string) {
	m.viewerMu.Lock()
	it, ok := m.idleTimers[name]
	if !ok {
		m.viewerMu.Unlock()
		return
	}
	delete(m.idleTimers, name)
	count := m.viewerCount[name]
	m.viewerMu.Unlock()

	if count > 0 || m.IsAgentActive(name) {
		log.Printf("[manager] idle-disconnect cancelled for %s (still in use)", name)
		return
	}

	if it.session == nil {
		return
	}

	m.mu.Lock()
	d, ok := m.sessions[name]
	// Identity check: only disconnect if the current session is the same
	// instance the timer was created for. A new session created between
	// scheduling and firing will have a different pointer.
	if !ok || d != it.session {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, name)
	m.mu.Unlock()
	_ = it.session.Disconnect()
	log.Printf("[manager] idle-disconnected %s", name)
}

// ActiveRDP returns the *BringRDP for a desktop whose guacd session is live,
// regardless of whether an agent has claimed control. Used by the web viewer
// to obtain the connection ID for joining the session.
func (m *Manager) ActiveRDP(name string) (*BringRDP, bool) {
	m.mu.Lock()
	d, ok := m.sessions[name]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	r, ok := d.(*BringRDP)
	if !ok || !r.Connected() {
		return nil, false
	}
	return r, true
}

// IsAgentActive reports whether an agent has claimed control of the named
// desktop via Up(). Used to determine whether the web viewer should join
// in read-only mode.
func (m *Manager) IsAgentActive(name string) bool {
	m.mu.Lock()
	d, ok := m.sessions[name]
	m.mu.Unlock()
	if !ok {
		return false
	}
	r, ok := d.(*BringRDP)
	if !ok {
		return false
	}
	return r.AgentActive()
}

// RDPConfig returns the stored connection configuration for the named RDP desktop.
func (m *Manager) RDPConfig(name string) (RDPConfig, bool) {
	return m.store.Get(name)
}

// notify sends a SessionEvent to all subscribers for the named desktop.
// If a subscriber's channel is full, the stale event is replaced with the
// newest one (coalesce) so no transition is silently lost.
func (m *Manager) notify(name string, t SessionEventType) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	evt := SessionEvent{Type: t, Name: name}
	for _, ch := range m.subs[name] {
		select {
		case ch <- evt:
		default:
			// Channel full: drain stale event then deliver newest.
			select {
			case <-ch:
			default:
			}
			ch <- evt
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
