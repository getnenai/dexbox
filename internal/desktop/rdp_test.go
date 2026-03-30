package desktop

import (
	"image"
	"sync"
	"testing"
	"time"

	"github.com/deluan/bring"
)

// mockBringClient implements bringClient for unit tests, without a real
// guacd connection.
type mockBringClient struct {
	mu       sync.Mutex
	startFn  func()
	stopFn   func()
	stateFn  func() bring.SessionState
	screenFn func() (image.Image, int64)
}

func (m *mockBringClient) Start() {
	if m.startFn != nil {
		m.startFn()
	}
}

func (m *mockBringClient) Stop() {
	if m.stopFn != nil {
		m.stopFn()
	}
}

func (m *mockBringClient) State() bring.SessionState {
	if m.stateFn != nil {
		return m.stateFn()
	}
	return bring.SessionActive
}

func (m *mockBringClient) Screen() (image.Image, int64) {
	if m.screenFn != nil {
		return m.screenFn()
	}
	return nil, 0
}

func (m *mockBringClient) SendMouse(_ image.Point, _ ...bring.MouseButton) error { return nil }
func (m *mockBringClient) SendText(_ string) error                                { return nil }
func (m *mockBringClient) SendKey(_ bring.KeyCode, _ bool) error                  { return nil }

// TestDisconnect_stopsStartGoroutine shows that Disconnect() must call
// client.Stop() and wait for the Start() goroutine to exit. Without that,
// the goroutine stays parked on session.In and keeps the client from being GC'd,
// leaking the TCP connection across repeated connect/disconnect cycles.
func TestDisconnect_stopsStartGoroutine(t *testing.T) {
	stopCh := make(chan struct{})

	mock := &mockBringClient{
		startFn: func() {
			// Simulate client.Start() blocking, as it does in production.
			<-stopCh
		},
		stopFn: func() {
			// Simulate client.Stop() unblocking Start().
			close(stopCh)
		},
	}

	rdp := &RDP{done: make(chan struct{})}
	rdp.client = mock

	// Simulate the goroutine Connect() launches.
	go func() {
		defer close(rdp.done)
		mock.Start()
	}()

	// Give the goroutine a moment to reach its blocking point.
	time.Sleep(20 * time.Millisecond)

	rdp.Disconnect() //nolint:errcheck

	// After Disconnect() returns the Start() goroutine must have exited,
	// i.e. rdp.done must be closed. Without calling Stop() + waiting,
	// rdp.done is still open here and the select hits the default case.
	select {
	case <-rdp.done:
		// pass — goroutine exited cleanly
	default:
		t.Error("Start() goroutine is still running after Disconnect(): connection leak")
	}
}
