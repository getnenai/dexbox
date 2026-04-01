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

func TestRDP_GuacdConnectionID_BeforeConnect(t *testing.T) {
	r := NewBringRDP("test", RDPConfig{}, "localhost:4822")
	if id := r.GuacdConnectionID(); id != "" {
		t.Errorf("expected empty string before connect, got %q", id)
	}
}

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
	if len(data) < 8 || string(data[:4]) != "\x89PNG" {
		prefix := data
		if len(prefix) > 4 {
			prefix = prefix[:4]
		}
		t.Fatalf("expected PNG data, got %d bytes starting with %q", len(data), string(prefix))
	}
}

func TestScreenshot_ContextCancelled(t *testing.T) {
	c := &mockClient{img: nil}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

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
	if len(c.mouseEvents) != 5 {
		t.Fatalf("expected 5 events for dz=2, got %d", len(c.mouseEvents))
	}
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

// --- buildGuacParams ---------------------------------------------------

func TestRDP_buildGuacParams(t *testing.T) {
	base := RDPConfig{
		Host:     "10.0.0.1",
		Port:     3389,
		Username: "user",
		Password: "pass",
		Width:    1920,
		Height:   1080,
	}

	tests := []struct {
		name        string
		cfg         RDPConfig
		wantPresent map[string]string
		wantAbsent  []string
	}{
		{
			name: "defaults: security=any, no optional params",
			cfg:  base,
			wantPresent: map[string]string{
				"hostname":         "10.0.0.1",
				"port":             "3389",
				"username":         "user",
				"password":         "pass",
				"width":            "1920",
				"height":           "1080",
				"security":         "any",
				"disable-audio":    "true",
				"enable-wallpaper": "false",
			},
			wantAbsent: []string{"ignore-cert", "drive-name", "drive-path"},
		},
		{
			name: "explicit security mode preserved",
			cfg:  withSecurity(base, "nla"),
			wantPresent: map[string]string{
				"security": "nla",
			},
		},
		{
			name: "IgnoreCert adds ignore-cert param",
			cfg:  withIgnoreCert(base, true),
			wantPresent: map[string]string{
				"ignore-cert": "true",
			},
		},
		{
			name: "IgnoreCert false omits ignore-cert param",
			cfg:  withIgnoreCert(base, false),
			wantAbsent: []string{"ignore-cert"},
		},
		{
			name: "DriveEnabled wires drive-name and fixed drive-path",
			cfg:  withDrive(base, "SharedDrive"),
			wantPresent: map[string]string{
				"drive-name": "SharedDrive",
				"drive-path": "/guacd-shared",
			},
		},
		{
			name: "DriveEnabled with empty DriveName defaults to guac-drive",
			cfg:  withDrive(base, ""),
			wantPresent: map[string]string{
				"drive-name": "guac-drive",
				"drive-path": "/guacd-shared",
			},
		},
		{
			name: "DriveEnabled with whitespace-only DriveName defaults to guac-drive",
			cfg:  withDrive(base, "   "),
			wantPresent: map[string]string{
				"drive-name": "guac-drive",
				"drive-path": "/guacd-shared",
			},
		},
		{
			name:       "DriveDisabled omits drive params",
			cfg:        func() RDPConfig { c := base; c.DriveEnabled = false; return c }(),
			wantAbsent: []string{"drive-name", "drive-path"},
		},
		{
			name: "DriveName trimmed of surrounding whitespace",
			cfg:  withDrive(base, "  SharedDrive  "),
			wantPresent: map[string]string{
				"drive-name": "SharedDrive",
				"drive-path": "/guacd-shared",
			},
		},
		{
			name: "DriveEnabled preserves other params",
			cfg:  withDrive(withIgnoreCert(base, true), "D"),
			wantPresent: map[string]string{
				"ignore-cert": "true",
				"drive-name":  "D",
				"drive-path":  "/guacd-shared",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewBringRDP("test", tt.cfg, "localhost:4822")
			params := r.buildGuacParams()

			for k, want := range tt.wantPresent {
				got, ok := params[k]
				if !ok {
					t.Errorf("param %q missing", k)
					continue
				}
				if got != want {
					t.Errorf("param %q = %q, want %q", k, got, want)
				}
			}

			for _, k := range tt.wantAbsent {
				if v, ok := params[k]; ok {
					t.Errorf("param %q should be absent, got %q", k, v)
				}
			}
		})
	}
}

func withSecurity(cfg RDPConfig, sec string) RDPConfig {
	cfg.Security = sec
	return cfg
}

func withIgnoreCert(cfg RDPConfig, v bool) RDPConfig {
	cfg.IgnoreCert = v
	return cfg
}

func withDrive(cfg RDPConfig, name string) RDPConfig {
	cfg.DriveEnabled = true
	cfg.DriveName = name
	return cfg
}
