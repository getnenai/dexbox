package web_test

import (
	"bufio"
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
	return desktop.NewManager(nil, store, "localhost:4822", "")
}

// collectSSE runs the SSE handler for the given desktop name until the context
// deadline expires, then returns the accumulated body. Using a timeout context
// avoids scheduler-sensitive sleeps: the initial state event is written
// synchronously before the handler blocks, so any reasonable deadline suffices.
func collectSSE(t *testing.T, mgr *desktop.Manager, name string, timeout time.Duration) string {
	t.Helper()
	h := web.Handler(mgr, "localhost:4822")
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req := httptest.NewRequest("GET", "/desktops/"+name+"/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // blocks until context times out
	return rec.Body.String()
}

// TestServeEvents_InitialDisconnected verifies that the first SSE event is
// "agent_disconnected" when no agent session is active.
func TestServeEvents_InitialDisconnected(t *testing.T) {
	mgr := newTestManager(t)
	body := collectSSE(t, mgr, "win", 100*time.Millisecond)

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
	rdp := desktop.NewBringRDP("win", desktop.RDPConfig{Host: "localhost", Port: 3389}, "localhost:4822")
	rdp.SetConnected(true) // simulate a live session
	mgr.SetSession("win", rdp)

	body := collectSSE(t, mgr, "win", 100*time.Millisecond)

	if !strings.Contains(body, "data: agent_connected") {
		t.Errorf("expected initial agent_connected, got %q", body)
	}
}

// TestServeEvents_ReceivesSessionDown verifies that when an active agent
// session is torn down via Down(), the SSE stream receives a dynamic
// "agent_disconnected" event after the initial "agent_connected".
//
// A real httptest.Server is used so the streaming body can be read
// line-by-line without data races. Blocking on the first event guarantees
// the handler has registered its mgr.Subscribe call before Down() fires.
func TestServeEvents_ReceivesSessionDown(t *testing.T) {
	mgr := newTestManager(t)
	rdp := desktop.NewBringRDP("win", desktop.RDPConfig{Host: "localhost", Port: 3389}, "localhost:4822")
	rdp.SetConnected(true) // simulate a live session
	mgr.SetSession("win", rdp)

	srv := httptest.NewServer(web.Handler(mgr, "localhost:4822"))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/desktops/win/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()

	// Stream non-empty SSE lines through a channel so we can select with a
	// timeout and avoid blocking the test goroutine indefinitely.
	lines := make(chan string, 4)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); line != "" {
				lines <- line
			}
		}
	}()

	// Block until the initial "agent_connected" frame; this is the sync
	// point that proves the handler has called mgr.Subscribe.
	select {
	case line := <-lines:
		if !strings.Contains(line, "agent_connected") {
			t.Fatalf("expected initial agent_connected, got %q", line)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initial agent_connected")
	}

	// Down() cannot race past the handler's subscription now.
	if err := mgr.Down(context.Background(), "win", false, false); err != nil {
		t.Fatalf("Down failed: %v", err)
	}

	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("SSE stream closed before agent_disconnected arrived")
		}
		if !strings.Contains(line, "agent_disconnected") {
			t.Errorf("expected agent_disconnected, got %q", line)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for agent_disconnected")
	}
}

// TestServeTunnel_UnknownDesktop_NotFound verifies that requesting a tunnel
// for a desktop that has no stored RDP config returns 404.
func TestServeTunnel_UnknownDesktop_NotFound(t *testing.T) {
	store := desktop.NewConnectionStore(filepath.Join(t.TempDir(), "conn.json"))
	mgr := desktop.NewManager(nil, store, "localhost:4822", "")

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
