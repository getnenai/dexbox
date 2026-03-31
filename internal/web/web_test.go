package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getnenai/dexbox/internal/desktop"
	"github.com/getnenai/dexbox/internal/web"
)

// newTestManager returns a Manager pre-loaded with a "win" RDP config and
// no active sessions.
func newTestManager(t *testing.T) *desktop.Manager {
	t.Helper()
	store := desktop.NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	store.Add("win", desktop.RDPConfig{Host: "localhost", Port: 3389})
	return desktop.NewManager(nil, store, "localhost:4822")
}

// collectSSE starts the SSE handler for the given desktop name, waits for
// extraWait to allow in-flight events to arrive, then cancels the request
// context and returns the accumulated body.
func collectSSE(t *testing.T, mgr *desktop.Manager, name string, extraWait time.Duration) string {
	t.Helper()
	h := web.Handler(mgr, "localhost:4822")
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/desktops/"+name+"/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(extraWait)
	cancel()
	<-done
	return rec.Body.String()
}

// TestServeEvents_InitialDisconnected verifies that the first SSE event is
// "agent_disconnected" when no agent session is active.
func TestServeEvents_InitialDisconnected(t *testing.T) {
	mgr := newTestManager(t)
	body := collectSSE(t, mgr, "win", 30*time.Millisecond)

	if !strings.Contains(body, "data: agent_disconnected") {
		t.Errorf("expected initial agent_disconnected, got %q", body)
	}
}

// TestServeEvents_InitialConnected verifies that the first SSE event is
// "agent_connected" when an agent RDP session is already active.
func TestServeEvents_InitialConnected(t *testing.T) {
	mgr := newTestManager(t)

	// Inject an active RDP session without dialing guacd. Disconnect() is a
	// no-op when client is nil, so this is safe.
	rdp := desktop.NewRDP("win", desktop.RDPConfig{Host: "localhost", Port: 3389}, "localhost:4822")
	mgr.SetSession("win", rdp)

	body := collectSSE(t, mgr, "win", 30*time.Millisecond)

	if !strings.Contains(body, "data: agent_connected") {
		t.Errorf("expected initial agent_connected, got %q", body)
	}
}

// TestServeEvents_ReceivesSessionDown verifies that when an active agent
// session is torn down via Down(), the SSE stream receives a dynamic
// "agent_disconnected" event after the initial "agent_connected".
func TestServeEvents_ReceivesSessionDown(t *testing.T) {
	mgr := newTestManager(t)
	rdp := desktop.NewRDP("win", desktop.RDPConfig{Host: "localhost", Port: 3389}, "localhost:4822")
	mgr.SetSession("win", rdp)

	h := web.Handler(mgr, "localhost:4822")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/desktops/win/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()

	// Let the handler subscribe and write the initial state.
	time.Sleep(30 * time.Millisecond)

	// Disconnect the agent, which fires SessionDown → "agent_disconnected".
	_ = mgr.Down(context.Background(), "win", false, false)

	// Let the SSE write propagate.
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, "data: agent_connected") {
		t.Errorf("expected initial agent_connected in body, got %q", body)
	}
	if !strings.Contains(body, "data: agent_disconnected") {
		t.Errorf("expected agent_disconnected after Down(), got %q", body)
	}
}

// TestServeTunnel_UnknownDesktop_NotFound verifies that requesting a tunnel
// for a desktop that has no stored RDP config returns 404.
func TestServeTunnel_UnknownDesktop_NotFound(t *testing.T) {
	store := desktop.NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := desktop.NewManager(nil, store, "localhost:4822")

	h := web.Handler(mgr, "localhost:4822")
	req := httptest.NewRequest("GET", "/desktops/unknown/tunnel", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown desktop, got %d", rec.Code)
	}
}

// TestServeTunnel_UnknownAction_NotFound verifies that an unrecognised action
// suffix returns 404.
func TestServeTunnel_UnknownAction_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	h := web.Handler(mgr, "localhost:4822")
	req := httptest.NewRequest("GET", "/desktops/win/badaction", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown action, got %d", rec.Code)
	}
}

// TestServeViewer_OK verifies that the /view action returns a 200 with HTML.
func TestServeViewer_OK(t *testing.T) {
	mgr := newTestManager(t)
	h := web.Handler(mgr, "localhost:4822")
	req := httptest.NewRequest("GET", "/desktops/win/view", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for view, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content-type, got %q", ct)
	}
}
