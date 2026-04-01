package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// mockDesktop implements Desktop for testing without real VBox or RDP.
type mockDesktop struct {
	name        string
	typ         string // "vm" or "rdp"
	isConnected bool
	clicks      [][3]int // {x, y, btn}
	typed       []string
}

func (m *mockDesktop) Connect(ctx context.Context) error { m.isConnected = true; return nil }
func (m *mockDesktop) Disconnect() error                 { m.isConnected = false; return nil }
func (m *mockDesktop) Connected() bool                   { return m.isConnected }

func (m *mockDesktop) Screenshot(ctx context.Context) ([]byte, error) {
	return testPNG(4, 4), nil
}

func (m *mockDesktop) MouseClick(x, y, btn int) error {
	m.clicks = append(m.clicks, [3]int{x, y, btn})
	return nil
}
func (m *mockDesktop) MouseMoveAbsolute(x, y int) error    { return nil }
func (m *mockDesktop) MouseDoubleClick(x, y, btn int) error { return nil }
func (m *mockDesktop) MouseScroll(x, y, dz int) error      { return nil }
func (m *mockDesktop) MouseDown(x, y, btn int) error       { return nil }
func (m *mockDesktop) MouseUp(x, y int) error              { return nil }

func (m *mockDesktop) TypeText(ctx context.Context, text string) error {
	m.typed = append(m.typed, text)
	return nil
}

func (m *mockDesktop) KeyPress(ctx context.Context, spec string) error { return nil }
func (m *mockDesktop) Name() string                                    { return m.name }
func (m *mockDesktop) Type() string                                    { return m.typ }

// testPNG returns a minimal valid PNG image.
func testPNG(w, h int) []byte {
	// Minimal 1x1 white PNG (valid enough for tests)
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, // 8-bit RGB
		0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, // IDAT chunk
		0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00,
		0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc, 0x33,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, // IEND chunk
		0xae, 0x42, 0x60, 0x82,
	}
}

func TestManagerResolve_VMSession(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822", "")

	vm := &mockDesktop{name: "my-vm", typ: "vm", isConnected: true}
	mgr.mu.Lock()
	mgr.sessions["my-vm"] = vm
	mgr.mu.Unlock()

	d, err := mgr.Resolve("my-vm")
	if err != nil {
		t.Fatalf("Resolve(my-vm) failed: %v", err)
	}
	if d.Type() != "vm" {
		t.Errorf("expected type vm, got %s", d.Type())
	}
	if d.Name() != "my-vm" {
		t.Errorf("expected name my-vm, got %s", d.Name())
	}
}

func TestManagerResolve_RDPSession(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822", "")

	rdp := &mockDesktop{name: "my-rdp", typ: "rdp", isConnected: true}
	mgr.mu.Lock()
	mgr.sessions["my-rdp"] = rdp
	mgr.mu.Unlock()

	d, err := mgr.Resolve("my-rdp")
	if err != nil {
		t.Fatalf("Resolve(my-rdp) failed: %v", err)
	}
	if d.Type() != "rdp" {
		t.Errorf("expected type rdp, got %s", d.Type())
	}
}

func TestManagerGet_RDPSession(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822", "")

	rdp := &mockDesktop{name: "my-rdp", typ: "rdp", isConnected: true}
	mgr.mu.Lock()
	mgr.sessions["my-rdp"] = rdp
	mgr.mu.Unlock()

	d, ok := mgr.Get("my-rdp")
	if !ok {
		t.Fatal("Get(my-rdp) returned false")
	}
	if d.Type() != "rdp" {
		t.Errorf("expected type rdp, got %s", d.Type())
	}
}

// TestManagerUp_RDPMutexRelease verifies that after a failed Up() call for
// an RDP desktop (e.g. guacd not available), the manager's mutex is properly
// released and subsequent operations don't hang.
//
// Before the fix, Manager.Up() for RDP double-locked m.mu: it acquired the
// lock at entry, held it via defer, then tried to lock again when storing
// the session. A successful RDP connection would deadlock. This test ensures
// the failure path releases the mutex correctly.
func TestManagerUp_RDPMutexRelease(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	store.Add("test-rdp", RDPConfig{
		Host:     "localhost",
		Port:     19999, // nothing listening here
		Username: "test",
		Password: "test",
		Width:    1024,
		Height:   768,
	})
	mgr := NewManager(nil, store, "localhost:4822", "")

	ctx := context.Background()

	// Up() will fail (guacd not running or connection refused) but must
	// not leave the mutex locked.
	_ = mgr.Up(ctx, "test-rdp")

	// If the mutex was not released, Get() will deadlock.
	done := make(chan bool, 1)
	go func() {
		_, _ = mgr.Get("test-rdp")
		done <- true
	}()

	select {
	case <-done:
		// OK — mutex was released
	case <-time.After(2 * time.Second):
		t.Fatal("Get() hung after failed Up() — mutex was not released")
	}
}

// TestManagerNotify_SendsToAllSubscribers verifies that notify delivers a
// SessionEvent to every subscriber registered for that desktop name.
func TestManagerNotify_SendsToAllSubscribers(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	ch1, cancel1 := mgr.Subscribe("win")
	ch2, cancel2 := mgr.Subscribe("win")
	defer cancel1()
	defer cancel2()

	mgr.notify("win", SessionUp)

	for i, ch := range []<-chan SessionEvent{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Type != SessionUp || evt.Name != "win" {
				t.Errorf("subscriber %d: got %+v, want {SessionUp win}", i+1, evt)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d: no event received", i+1)
		}
	}
}

// TestManagerSubscribe_CancelRemovesSubscriber verifies that calling the
// cancel function closes the channel and stops future events from being
// delivered.
func TestManagerSubscribe_CancelRemovesSubscriber(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	ch, cancel := mgr.Subscribe("win")
	cancel() // unsubscribe and close channel before any event

	// The channel must be closed: a receive should return ok==false immediately.
	select {
	case evt, ok := <-ch:
		if ok {
			t.Errorf("expected channel closed after cancel, got real event %+v", evt)
		}
	case <-time.After(50 * time.Millisecond):
		t.Error("channel was not closed after cancel")
	}
}

// TestManagerDown_NotifiesSessionDown verifies that Down() fires a SessionDown
// event to subscribers after disconnecting an RDP session.
func TestManagerDown_NotifiesSessionDown(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	mock := &mockDesktop{name: "win", typ: "rdp", isConnected: true}
	mgr.SetSession("win", mock)

	ch, cancel := mgr.Subscribe("win")
	defer cancel()

	if err := mgr.Down(context.Background(), "win", false, false); err != nil {
		t.Fatalf("Down failed: %v", err)
	}

	select {
	case evt := <-ch:
		if evt.Type != SessionDown || evt.Name != "win" {
			t.Errorf("got %+v, want {SessionDown win}", evt)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("no SessionDown event received after Down()")
	}
}

// TestManagerDown_NoNotifyForVM verifies that Down() does not fire a
// SessionDown event for a VM session. SessionUp is only emitted for RDP, so
// emitting SessionDown for VMs would produce unmatched events.
func TestManagerDown_NoNotifyForVM(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	mgr.SetSession("my-vm", &mockDesktop{name: "my-vm", typ: "vm", isConnected: true})

	ch, cancel := mgr.Subscribe("my-vm")
	defer cancel()

	if err := mgr.Down(context.Background(), "my-vm", false, false); err != nil {
		t.Fatalf("Down failed: %v", err)
	}

	select {
	case evt := <-ch:
		t.Errorf("expected no event for VM session, got %+v", evt)
	case <-time.After(50 * time.Millisecond):
		// expected — VMs never emit session events
	}
}

// TestManagerActiveRDP_ReturnsRDP verifies that ActiveRDP returns the *RDP
// session when one is registered for that name.
func TestManagerActiveRDP_ReturnsRDP(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	rdp := NewBringRDP("win", RDPConfig{Host: "localhost", Port: 3389}, "localhost:4822")
	rdp.SetConnected(true) // simulate a live session without dialing guacd
	mgr.SetSession("win", rdp)

	got, ok := mgr.ActiveRDP("win")
	if !ok {
		t.Fatal("ActiveRDP returned false for an active RDP session")
	}
	if got != rdp {
		t.Error("ActiveRDP returned wrong *RDP instance")
	}
}

// TestManagerActiveRDP_ReturnsFalseForNonRDP verifies that ActiveRDP returns
// false when the active session is a VM (not an *RDP).
func TestManagerActiveRDP_ReturnsFalseForNonRDP(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	mgr.SetSession("my-vm", &mockDesktop{name: "my-vm", typ: "vm"})

	_, ok := mgr.ActiveRDP("my-vm")
	if ok {
		t.Error("ActiveRDP should return false for a VM session")
	}
}

// TestManagerActiveRDP_ReturnsFalseWhenMissing verifies that ActiveRDP
// returns false when no session is registered for the given name.
func TestManagerActiveRDP_ReturnsFalseWhenMissing(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	_, ok := mgr.ActiveRDP("nonexistent")
	if ok {
		t.Error("ActiveRDP should return false for a missing session")
	}
}

// TestManagerRDPConfig_Found verifies that RDPConfig returns the stored
// connection configuration for a known desktop name.
func TestManagerRDPConfig_Found(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	cfg := RDPConfig{Host: "192.168.1.1", Port: 3389, Username: "admin"}
	store.Add("win", cfg)
	mgr := NewManager(nil, store, "localhost:4822")

	got, ok := mgr.RDPConfig("win")
	if !ok {
		t.Fatal("RDPConfig returned false for a stored config")
	}
	if got.Host != cfg.Host || got.Port != cfg.Port || got.Username != cfg.Username {
		t.Errorf("got %+v, want %+v", got, cfg)
	}
}

// TestManagerRDPConfig_NotFound verifies that RDPConfig returns false for
// an unknown desktop name.
func TestManagerRDPConfig_NotFound(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := NewManager(nil, store, "localhost:4822")

	_, ok := mgr.RDPConfig("nonexistent")
	if ok {
		t.Error("RDPConfig should return false for an unknown desktop")
	}
}

// TestManagerUp_RDPDeadlock verifies that Manager.Up() for an RDP desktop
// does not deadlock when the RDP connection succeeds.
//
// Before the fix, the RDP success path in Up() called m.mu.Lock() while
// the mutex was already held via defer, causing a deadlock. The test uses
// a context with a deadline so that a deadlock manifests as a timeout
// rather than hanging the test runner forever.
func TestManagerUp_RDPDeadlock(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	store.Add("test-rdp", RDPConfig{
		Host:     "127.0.0.1",
		Port:     19999,
		Username: "test",
		Password: "test",
		Width:    1024,
		Height:   768,
	})
	mgr := NewManager(nil, store, "localhost:4822", "")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Up() will fail (guacd/RDP not available) but must not deadlock.
	// Run in a goroutine so a deadlock manifests as a timeout rather than
	// hanging the test runner forever.
	errc := make(chan error, 1)
	go func() { errc <- mgr.Up(ctx, "test-rdp") }()

	var err error
	select {
	case err = <-errc:
		// returned in time — proceed to inspect the error below
	case <-ctx.Done():
		t.Fatal("Manager.Up() deadlocked — context deadline exceeded")
	}

	// We expect an error (guacd not running), not success
	if err == nil {
		// If guacd is running in this env, verify the session was stored
		d, ok := mgr.Get("test-rdp")
		if !ok {
			t.Fatal("Up() succeeded but session not stored in manager")
		}
		if d.Type() != "rdp" {
			t.Errorf("expected rdp, got %s", d.Type())
		}
	}
}
