package desktop

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"testing"
	"time"

	"github.com/deluan/bring"
)

// --- mock client --------------------------------------------------------

// mockClient satisfies the Client interface for unit tests.
type mockClient struct {
	state       SessionState
	img         image.Image
	mouseEvents []mockMouseEvent
	keyEvents   []mockKeyEvent
	textEvents  []string
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
func (m *mockClient) SendText(s string) error {
	m.textEvents = append(m.textEvents, s)
	return nil
}
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

// --- buildGuacParams ----------------------------------------------------

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
				"client-name":      "Dexbox",
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
			name:       "IgnoreCert false omits ignore-cert param",
			cfg:        withIgnoreCert(base, false),
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
			name: "DriveEnabled with empty DriveName defaults to Shared",
			cfg:  withDrive(base, ""),
			wantPresent: map[string]string{
				"drive-name": "Shared",
				"drive-path": "/guacd-shared",
			},
		},
		{
			name: "DriveEnabled with whitespace-only DriveName defaults to Shared",
			cfg:  withDrive(base, "   "),
			wantPresent: map[string]string{
				"drive-name": "Shared",
				"drive-path": "/guacd-shared",
			},
		},
		{
			name: "DriveDisabled omits drive params even with non-empty DriveName",
			cfg: func() RDPConfig {
				c := base
				c.DriveEnabled = false
				c.DriveName = "disabled-drive"
				return c
			}(),
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
		t.Fatalf("expected PNG data, got %d bytes starting with %q", len(data), data[:4])
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
	// First call is the position move (no button). Then 2×(press+release) = 4.
	// Total: 1 + 4 = 5.
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

// --- typeText helpers ---------------------------------------------------

func isModifierKey(k bring.KeyCode) bool {
	switch k {
	case bring.KeyLeftShift, bring.KeyRightShift,
		bring.KeyLeftControl, bring.KeyRightControl,
		bring.KeyLeftAlt, bring.KeyRightAlt,
		bring.KeyMeta:
		return true
	}
	return false
}

// isModifierRelease returns true if the event is a keyup for a modifier key —
// i.e. one of the hard-reset events that releaseModifiers fires before each
// character. Filtering these out lets tests focus on the intent of each stroke.
func isModifierRelease(e mockKeyEvent) bool {
	return !e.pressed && isModifierKey(e.key)
}

// intentKeyEvents strips orphaned modifier-keyup events (those from
// releaseModifiers that have no matching preceding keydown) from the slice,
// leaving only the events that encode actual character intent — including the
// intentional shift up/down pairs from sendWithShift.
func intentKeyEvents(events []mockKeyEvent) []mockKeyEvent {
	pressed := make(map[bring.KeyCode]bool)
	var out []mockKeyEvent
	for _, e := range events {
		if !isModifierKey(e.key) {
			out = append(out, e)
			continue
		}
		if e.pressed {
			pressed[e.key] = true
			out = append(out, e)
		} else if pressed[e.key] {
			// Intentional release of a modifier we explicitly pressed — keep it.
			delete(pressed, e.key)
			out = append(out, e)
		}
		// else: orphaned release from releaseModifiers — drop it.
	}
	return out
}

// --- typeText -----------------------------------------------------------

func TestTypeText_PlainASCII(t *testing.T) {
	c := &mockClient{}
	if err := typeText(c, "hello", 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.textEvents) != 0 {
		t.Errorf("expected no textEvents for plain ASCII, got %v", c.textEvents)
	}
	got := intentKeyEvents(c.keyEvents)
	want := []mockKeyEvent{
		{bring.KeyCode('h'), true}, {bring.KeyCode('h'), false},
		{bring.KeyCode('e'), true}, {bring.KeyCode('e'), false},
		{bring.KeyCode('l'), true}, {bring.KeyCode('l'), false},
		{bring.KeyCode('l'), true}, {bring.KeyCode('l'), false},
		{bring.KeyCode('o'), true}, {bring.KeyCode('o'), false},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d intent key events, got %d: %v", len(want), len(got), got)
	}
	for i, ev := range want {
		if got[i] != ev {
			t.Errorf("keyEvents[%d]: got %+v, want %+v", i, got[i], ev)
		}
	}
}

func TestTypeText_ControlChars(t *testing.T) {
	c := &mockClient{}
	if err := typeText(c, "\n\t\b\x1b", 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.textEvents) != 0 {
		t.Errorf("expected no text events for control chars, got %d", len(c.textEvents))
	}
	want := []mockKeyEvent{
		{bring.KeyEnter, true}, {bring.KeyEnter, false},
		{bring.KeyTab, true}, {bring.KeyTab, false},
		{bring.KeyBackspace, true}, {bring.KeyBackspace, false},
		{bring.KeyEscape, true}, {bring.KeyEscape, false},
	}
	got := intentKeyEvents(c.keyEvents)
	if len(got) != len(want) {
		t.Fatalf("expected %d intent key events, got %d: %v", len(want), len(got), got)
	}
	for i, ev := range want {
		if got[i] != ev {
			t.Errorf("keyEvents[%d]: got {%v, %v}, want {%v, %v}", i, got[i].key, got[i].pressed, ev.key, ev.pressed)
		}
	}
}

func TestTypeText_CRLFNormalized(t *testing.T) {
	c := &mockClient{}
	if err := typeText(c, "a\r\nb\rc", 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// \r\n -> single Enter, \r -> single Enter
	enterCount := 0
	for _, ev := range c.keyEvents {
		if ev.key == bring.KeyEnter && ev.pressed {
			enterCount++
		}
	}
	if enterCount != 2 {
		t.Errorf("expected 2 Enter presses after CRLF normalization, got %d", enterCount)
	}
}

func TestTypeText_UppercaseUsesShift(t *testing.T) {
	c := &mockClient{}
	if err := typeText(c, "A", 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Uppercase 'A' is keysym 65 — sent directly, guacd adds shift internally.
	// No manual shift events from our side.
	got := intentKeyEvents(c.keyEvents)
	if len(got) != 2 {
		t.Fatalf("expected 2 intent key events (down/up) for uppercase, got %d: %v", len(got), got)
	}
	if got[0] != (mockKeyEvent{bring.KeyCode('A'), true}) {
		t.Errorf("event[0]: want KeyCode('A') down, got %+v", got[0])
	}
	if got[1] != (mockKeyEvent{bring.KeyCode('A'), false}) {
		t.Errorf("event[1]: want KeyCode('A') up, got %+v", got[1])
	}
	if len(c.textEvents) != 0 {
		t.Errorf("expected no textEvents, got %v", c.textEvents)
	}
}

func TestTypeText_ShiftSymbolsUseShift(t *testing.T) {
	symbols := "!@#$%^&*()~_+{}|:\"<>?"
	for _, sym := range symbols {
		c := &mockClient{}
		if err := typeText(c, string(sym), 0, nil); err != nil {
			t.Fatalf("unexpected error for %q: %v", sym, err)
		}
		// Shifted symbols are sent as their own keysym; guacd handles shift.
		got := intentKeyEvents(c.keyEvents)
		if len(got) != 2 {
			t.Errorf("symbol %q: expected 2 intent key events (down/up), got %d: %v", sym, len(got), got)
			continue
		}
		if got[0] != (mockKeyEvent{bring.KeyCode(sym), true}) {
			t.Errorf("symbol %q: event[0]: want KeyCode down, got %+v", sym, got[0])
		}
		if got[1] != (mockKeyEvent{bring.KeyCode(sym), false}) {
			t.Errorf("symbol %q: event[1]: want KeyCode up, got %+v", sym, got[1])
		}
	}
}

func TestTypeText_UnknownControlCharReturnsError(t *testing.T) {
	// Control characters below 0x20 that are not in ctrlKeyCodes should
	// return an error rather than being cast to a raw keysym.
	for _, r := range []rune{'\x01', '\x02', '\x03', '\x04', '\x05', '\x10', '\x1c', '\x1f'} {
		c := &mockClient{}
		err := typeText(c, string(r), 0, nil)
		if err == nil {
			t.Errorf("expected error for control char U+%04X, got nil", r)
		}
	}
}

func TestTypeText_PlainCharUsesExplicitKeyEvents(t *testing.T) {
	c := &mockClient{}
	if err := typeText(c, "v", 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.textEvents) != 0 {
		t.Errorf("expected no textEvents for plain char, got %v", c.textEvents)
	}
	got := intentKeyEvents(c.keyEvents)
	if len(got) != 2 {
		t.Fatalf("expected 2 intent key events for plain char, got %d: %v", len(got), got)
	}
	if got[0] != (mockKeyEvent{bring.KeyCode('v'), true}) {
		t.Errorf("event[0]: want KeyCode('v') down, got %+v", got[0])
	}
	if got[1] != (mockKeyEvent{bring.KeyCode('v'), false}) {
		t.Errorf("event[1]: want KeyCode('v') up, got %+v", got[1])
	}
}

// TestTypeText_ModifierResetBeforeEachChar verifies that releaseModifiers is
// called once at the start (not per character), and that plain chars produce
// exactly 2 intent events each (keydown + keyup).
func TestTypeText_ModifierResetBeforeEachChar(t *testing.T) {
	numModifiers := len(modifierKeys) // derived from the live releaseModifiers list
	c := &mockClient{}
	if err := typeText(c, "abc", 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// One-time reset at the start: exactly numModifiers orphaned releases.
	releaseCount := 0
	for _, e := range c.keyEvents {
		if isModifierRelease(e) {
			releaseCount++
		}
	}
	if releaseCount != numModifiers {
		t.Errorf("expected %d modifier release events (one-time reset), got %d", numModifiers, releaseCount)
	}
	// Intent events: 3 chars × 2 (down+up) = 6.
	got := intentKeyEvents(c.keyEvents)
	if len(got) != 6 {
		t.Errorf("expected 6 intent key events for 3 plain chars, got %d: %v", len(got), got)
	}
}

func TestTypeText_DelayApplied(t *testing.T) {
	c := &mockClient{}
	delay := 10 * time.Millisecond
	start := time.Now()
	if err := typeText(c, "ab", delay, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)
	// With st == nil and delay = 10 ms, typeText sleeps twice per character:
	// once as keyHold after keydown and once as delay after keyup.
	// Two characters → at least 40 ms of intentional sleep; we assert
	// ≥ 40 ms. Slow CI only makes elapsed longer, never shorter, so this
	// threshold is reliable without any artificial safety haircut.
	if elapsed < 4*delay {
		t.Errorf("expected at least %v elapsed with delay, got %v", 4*delay, elapsed)
	}
}

func TestTypeText_ZeroDelayNoSleep(t *testing.T) {
	c := &mockClient{}
	start := time.Now()
	if err := typeText(c, "abcde", 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)
	// With no delay there are no intentional sleeps; 100 ms is generous
	// enough for slow CI machines while still catching accidental sleeps.
	if elapsed > 100*time.Millisecond {
		t.Errorf("expected near-instant completion with zero delay, got %v", elapsed)
	}
}

// --- resolveKeyDelay ---------------------------------------------------

func TestResolveKeyDelay_Unset(t *testing.T) {
	d, err := resolveKeyDelay(RDPConfig{KeyDelayMs: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != defaultKeyDelayMs*time.Millisecond {
		t.Errorf("expected default %v, got %v", defaultKeyDelayMs*time.Millisecond, d)
	}
}

func TestResolveKeyDelay_Positive(t *testing.T) {
	d, err := resolveKeyDelay(RDPConfig{KeyDelayMs: 25})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 25*time.Millisecond {
		t.Errorf("expected 25ms, got %v", d)
	}
}

func TestResolveKeyDelay_Negative(t *testing.T) {
	_, err := resolveKeyDelay(RDPConfig{KeyDelayMs: -1})
	if err == nil {
		t.Error("expected error for negative KeyDelayMs, got nil")
	}
}

// --- typeText sync-wait cap --------------------------------------------

// TestTypeText_SyncWaitCappedOnStaticDesktop is the regression test for the
// 50–115 s-per-character stall observed in the field when typing onto a
// static Windows desktop. The EMA of the inter-sync interval grows without
// bound (the cursor blink can be the only display change, ~29 s apart), and
// without a ceiling on 4×FrameInterval a single keystroke would wait more
// than 100 s for a sync that is never coming. The cap should bound each
// per-character wait to ~keySyncWaitCap regardless of how stale the EMA is.
func TestTypeText_SyncWaitCappedOnStaticDesktop(t *testing.T) {
	c := &mockClient{}
	st := newSyncTracker()

	// Simulate a static desktop: warm-up has already completed (so the
	// warm-up branch is skipped), and the EMA has been dragged to 30 s per
	// frame by long quiet periods. With the bug, maxWait = 4 × 30 s = 120 s
	// per character; with the fix, maxWait is bounded by keySyncWaitCap
	// (1.5 s by default). No sync() is ever signalled — the tracker is
	// deliberately silent to emulate a display that never changes.
	st.gen.Store(1)
	st.frameNanos.Store(int64(30 * time.Second))

	start := time.Now()
	if err := typeText(c, "x", 0, st); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)

	// With the cap at 1.5 s there's exactly one per-character barrier wait
	// and no warm-up wait (FrameInterval() is already non-zero), so the
	// upper bound is keySyncWaitCap plus a small slack for scheduling on
	// slow CI hardware. 2 s is comfortably above 1.5 s without letting the
	// original bug (≥50 s) slip through.
	if elapsed > 2*time.Second {
		t.Errorf("expected typeText to complete within 2s under a stale FrameInterval, got %v (cap=%v)", elapsed, keySyncWaitCap)
	}
	// The wait must still actually happen — otherwise the barrier is broken
	// and we've lost the drain guarantee. With a never-signalling tracker
	// the wait should hit its full budget; assert at least a few hundred ms
	// to catch accidental no-op regressions.
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected typeText to block on the sync barrier for at least 100ms, got %v", elapsed)
	}
}

// --- loadKeySyncWaitCap -------------------------------------------------

// TestLoadKeySyncWaitCap covers the DEXBOX_KEY_SYNC_WAIT_MS env var parsing:
// default when unset, clamp when below floor, honor valid values, and fall
// back on garbage. Mirrors the shape of TestLoadIdleDisconnectDelay.
func TestLoadKeySyncWaitCap(t *testing.T) {
	cases := []struct {
		name  string
		set   bool
		value string
		want  time.Duration
	}{
		{"unset uses default", false, "", defaultKeySyncWaitCap},
		{"valid value honoured", true, "3000", 3 * time.Second},
		{"below floor is clamped", true, "10", minKeySyncWaitCap},
		{"zero falls back to default", true, "0", defaultKeySyncWaitCap},
		{"negative falls back to default", true, "-10", defaultKeySyncWaitCap},
		{"garbage falls back to default", true, "notanumber", defaultKeySyncWaitCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("DEXBOX_KEY_SYNC_WAIT_MS", tc.value)
			} else {
				t.Setenv("DEXBOX_KEY_SYNC_WAIT_MS", "")
				_ = os.Unsetenv("DEXBOX_KEY_SYNC_WAIT_MS")
			}
			got := loadKeySyncWaitCap()
			if got != tc.want {
				t.Errorf("loadKeySyncWaitCap() = %v, want %v", got, tc.want)
			}
		})
	}
}

