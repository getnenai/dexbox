package desktop

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/deluan/bring"
)

// --- mock client --------------------------------------------------------

// mockClient satisfies the Client interface for unit tests.
type mockClient struct {
	state       SessionState
	img         image.Image
	mouseEvents []mockMouseEvent
	keyEvents   []mockKeyEvent
	sendMouseFn func(p image.Point, btns ...MouseButton) error
	sendKeyFn   func(key KeyCode, pressed bool) error
}

type mockMouseEvent struct {
	p    image.Point
	btns []MouseButton
}

type mockKeyEvent struct {
	key     KeyCode
	pressed bool
}

func (m *mockClient) Start()                                  {}
func (m *mockClient) Stop()                                   {}
func (m *mockClient) State() SessionState                     { return m.state }
func (m *mockClient) ConnectionID() string                    { return "" }
func (m *mockClient) Screen() (image.Image, int64)            { return m.img, 0 }
func (m *mockClient) SendText(_ string) error                 { return nil }
func (m *mockClient) SendMouse(p image.Point, btns ...MouseButton) error {
	if m.sendMouseFn != nil {
		return m.sendMouseFn(p, btns...)
	}
	m.mouseEvents = append(m.mouseEvents, mockMouseEvent{p, btns})
	return nil
}
func (m *mockClient) SendKey(key KeyCode, pressed bool) error {
	if m.sendKeyFn != nil {
		return m.sendKeyFn(key, pressed)
	}
	m.keyEvents = append(m.keyEvents, mockKeyEvent{key, pressed})
	return nil
}

// --- GuacdConnectionID -------------------------------------------------

// TestRDP_GuacdConnectionID_BeforeConnect verifies that GuacdConnectionID
// returns an empty string before the client has reached SessionActive.
func TestRDP_GuacdConnectionID_BeforeConnect(t *testing.T) {
	r := NewBringRDP("test", RDPConfig{}, "localhost:4822")
	if id := r.GuacdConnectionID(); id != "" {
		t.Errorf("expected empty string before connect, got %q", id)
	}
}

// TestRDP_GuacdConnectionID_AfterActive verifies that GuacdConnectionID
// returns the connection ID stored once the session reaches SessionActive.
// We set connID directly (same package) to isolate the getter from the
// real guacd connection logic.
func TestRDP_GuacdConnectionID_AfterActive(t *testing.T) {
	r := NewBringRDP("test", RDPConfig{}, "localhost:4822")
	r.connID = "$abc-def-123"

	if id := r.GuacdConnectionID(); id != "$abc-def-123" {
		t.Errorf("expected $abc-def-123, got %q", id)
	}
}

// --- screenshot --------------------------------------------------------

func TestScreenshot_ReturnsPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	c := &mockClient{img: img}

	data, err := screenshot(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// PNG files start with the 8-byte PNG signature.
	if len(data) < 8 || string(data[:4]) != "\x89PNG" {
		t.Fatalf("expected PNG data, got %d bytes starting with %q", len(data), data[:4])
	}
}

func TestScreenshot_ContextCancelled(t *testing.T) {
	c := &mockClient{img: nil} // always returns nil frame
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := screenshot(ctx, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- mouseClick --------------------------------------------------------

func TestMouseClick_SendsTwoEvents(t *testing.T) {
	c := &mockClient{}
	if err := mouseClick(c, 10, 20, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.mouseEvents) != 2 {
		t.Fatalf("expected 2 SendMouse calls, got %d", len(c.mouseEvents))
	}
	// First event carries a button; second is a release (no buttons).
	if len(c.mouseEvents[0].btns) == 0 {
		t.Error("expected press event to carry a button")
	}
	if len(c.mouseEvents[1].btns) != 0 {
		t.Error("expected release event to carry no buttons")
	}
}

func TestMouseClick_PropagatesFirstSendMouseError(t *testing.T) {
	want := errors.New("send failed")
	c := &mockClient{sendMouseFn: func(_ image.Point, _ ...MouseButton) error { return want }}
	if err := mouseClick(c, 0, 0, 1); !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

// --- mouseScroll -------------------------------------------------------

func TestMouseScroll_PositiveDzUsesMouseUp(t *testing.T) {
	c := &mockClient{}
	if err := mouseScroll(c, 0, 0, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First call is the position move (no button). Then 2×(press+release) = 4.
	// Total: 1 + 4 = 5.
	if len(c.mouseEvents) != 5 {
		t.Fatalf("expected 5 events for dz=2, got %d", len(c.mouseEvents))
	}
	// Each press event must use MouseUp (scroll-up button).
	for _, ev := range c.mouseEvents[1:] {
		if len(ev.btns) == 1 && ev.btns[0] != bring.MouseUp {
			t.Errorf("expected MouseUp, got %v", ev.btns[0])
		}
	}
}

func TestMouseScroll_NegativeDzUsesMouseDown(t *testing.T) {
	c := &mockClient{}
	if err := mouseScroll(c, 0, 0, -1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, ev := range c.mouseEvents[1:] {
		if len(ev.btns) == 1 && ev.btns[0] != bring.MouseDown {
			t.Errorf("expected MouseDown, got %v", ev.btns[0])
		}
	}
}

// --- keyPress ----------------------------------------------------------

func TestKeyPress_UnknownKeyReturnsError(t *testing.T) {
	c := &mockClient{}
	if err := keyPress(c, "NOT_A_KEY"); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestKeyPress_SingleKeyPressRelease(t *testing.T) {
	c := &mockClient{}
	if err := keyPress(c, "enter"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.keyEvents) != 2 {
		t.Fatalf("expected press+release (2 events), got %d", len(c.keyEvents))
	}
	if !c.keyEvents[0].pressed {
		t.Error("first event should be a press")
	}
	if c.keyEvents[1].pressed {
		t.Error("second event should be a release")
	}
}

func TestKeyPress_ComboReleasedInReverseOrder(t *testing.T) {
	c := &mockClient{}
	// ctrl+c: press ctrl, press c, release c, release ctrl
	if err := keyPress(c, "ctrl+c"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.keyEvents) != 4 {
		t.Fatalf("expected 4 events for ctrl+c, got %d", len(c.keyEvents))
	}
	ctrlCode, _ := resolveKeyCode("ctrl")
	cCode, _ := resolveKeyCode("c")
	expected := []mockKeyEvent{
		{ctrlCode, true},
		{cCode, true},
		{cCode, false},
		{ctrlCode, false},
	}
	for i, ev := range c.keyEvents {
		if ev != expected[i] {
			t.Errorf("event[%d]: got {%v, %v}, want {%v, %v}", i, ev.key, ev.pressed, expected[i].key, expected[i].pressed)
		}
	}
}
