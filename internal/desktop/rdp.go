package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deluan/bring"

	"github.com/getnenai/dexbox/internal/guacd"
)

// Type aliases so the Client interface doesn't leak bring's concrete types.
// Using aliases (not new types) means bring values satisfy them directly.
type (
	SessionState = bring.SessionState
	MouseButton  = bring.MouseButton
	KeyCode      = bring.KeyCode
)

// Client is the subset of *bring.Client that BringRDP depends on.
// Defined here to document the contract and enable passing a test double
// as a function argument where needed.
type Client interface {
	Start()
	Stop()
	State() SessionState
	// ConnectionID returns the guacd connection ID assigned during handshake.
	// Empty until the session reaches SessionActive.
	ConnectionID() string
	Screen() (image.Image, int64)
	SendMouse(p image.Point, pressedButtons ...MouseButton) error
	SendText(sequence string) error
	SendKey(key KeyCode, pressed bool) error
}

// defaultKeyDelayMs is the inter-keystroke delay applied when
// RDPConfig.KeyDelayMs is zero. 50 ms gives guacd enough time to drain its
// input queue between keydown and keyup events at any display resolution.
const defaultKeyDelayMs = 50

// resolveKeyDelay returns the inter-keystroke delay for a given RDPConfig.
// KeyDelayMs == 0 → default; > 0 → that many ms; < 0 → error.
func resolveKeyDelay(cfg RDPConfig) (time.Duration, error) {
	switch {
	case cfg.KeyDelayMs < 0:
		return 0, fmt.Errorf("invalid KeyDelayMs %d: must be >= 0 (0 = default %d ms)", cfg.KeyDelayMs, defaultKeyDelayMs)
	case cfg.KeyDelayMs > 0:
		return time.Duration(cfg.KeyDelayMs) * time.Millisecond, nil
	default:
		return defaultKeyDelayMs * time.Millisecond, nil
	}
}

// syncTracker counts Guacamole sync instructions received from guacd. It is
// used as a frame-boundary barrier between keydown and keyup: by waiting for
// the generation to advance past the value recorded just before keydown, we
// guarantee guacd has completed at least one full main-loop iteration (and
// therefore called guac_rdp_handle_input_events) after our keydown was sent.
//
// It also tracks frame timing: each signal() records the wall-clock timestamp
// and updates an exponential moving average of the inter-sync interval, which
// approximates the display frame duration and lets callers observe the live fps.
type syncTracker struct {
	gen          atomic.Uint64
	notify       chan struct{} // buffered 1; pinged on every sync
	lastSyncNano atomic.Int64 // unix nanos of last signal() call
	frameNanos   atomic.Int64 // EMA of inter-sync interval in nanos; 0 = unknown
}

func newSyncTracker() *syncTracker {
	return &syncTracker{notify: make(chan struct{}, 1)}
}

func (t *syncTracker) signal() {
	now := time.Now().UnixNano()
	if prev := t.lastSyncNano.Swap(now); prev != 0 {
		interval := now - prev
		if old := t.frameNanos.Load(); old == 0 {
			t.frameNanos.Store(interval)
		} else {
			// EMA with α=0.125 (8-sample window)
			t.frameNanos.Store(old + (interval-old)>>3)
		}
	}
	t.gen.Add(1)
	select {
	case t.notify <- struct{}{}:
	default: // drop if a signal is already pending; one is enough
	}
}

// FrameInterval returns the exponential moving average of the inter-sync
// interval (≈ display frame duration), or 0 if fewer than two syncs have
// been seen yet.
func (t *syncTracker) FrameInterval() time.Duration {
	if v := t.frameNanos.Load(); v > 0 {
		return time.Duration(v)
	}
	return 0
}

// waitFor blocks until the generation counter reaches wantGen (i.e. at least
// one sync has arrived since the caller's baseline was sampled), or until
// maxWait elapses. It then sleeps any remaining time up to minWait to ensure
// a minimum dwell even when a sync satisfies the condition immediately.
func (t *syncTracker) waitFor(wantGen uint64, minWait, maxWait time.Duration) {
	start := time.Now()
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()

	for t.gen.Load() < wantGen {
		select {
		case <-t.notify:
			// generation may have advanced; re-check loop condition
		case <-deadline.C:
			goto done
		}
	}
done:
	if elapsed := time.Since(start); elapsed < minWait {
		time.Sleep(minWait - elapsed)
	}
}

// BringRDP implements Desktop using deluan/bring to talk to a guacd daemon
// which proxies the actual RDP connection.
type BringRDP struct {
	name      string
	config    RDPConfig
	guacdAddr string

	client      *bring.Client
	connID      string // guacd connection ID from handshake; set after SessionActive
	live        bool   // true once Connect succeeds; false when session drops
	agentActive bool   // true when an agent has claimed control via Up()
	syncTracker *syncTracker
	mu          sync.Mutex
	connectMu   sync.Mutex // serializes concurrent Connect calls; allows retry on failure
	done        chan struct{} // closed when client.Start() returns
	kaStop      chan struct{} // closed to stop the keepAlive goroutine
}

// NewBringRDP creates an RDP desktop. Call Connect to establish the session.
func NewBringRDP(name string, cfg RDPConfig, guacdAddr string) *BringRDP {
	return &BringRDP{
		name:      name,
		config:    cfg,
		guacdAddr: guacdAddr,
	}
}

// buildGuacParams constructs the guacd parameter map from the RDP config.
func (r *BringRDP) buildGuacParams() map[string]string {
	security := r.config.Security
	if security == "" {
		security = "any"
	}
	params := map[string]string{
		"hostname":         r.config.Host,
		"port":             fmt.Sprintf("%d", r.config.Port),
		"username":         r.config.Username,
		"password":         r.config.Password,
		"width":            fmt.Sprintf("%d", r.config.Width),
		"height":           fmt.Sprintf("%d", r.config.Height),
		"security":         security,
		"client-name":      "Dexbox",
		"disable-audio":    "true",
		"enable-wallpaper": "false",
	}
	if r.config.IgnoreCert {
		params["ignore-cert"] = "true"
	}
	if r.config.DriveEnabled {
		driveName := strings.TrimSpace(r.config.DriveName)
		if driveName == "" {
			driveName = "Shared"
		}
		params["enable-drive"] = "true"
		params["drive-name"] = driveName
		params["drive-path"] = guacd.ContainerMount
		params["create-drive-path"] = "true"
	}
	return params
}

func (r *BringRDP) Connect(ctx context.Context) error {
	// Fast path: already connected (no lock contention in the common case).
	r.mu.Lock()
	if r.client != nil {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	// Serialize concurrent Connect calls so only one goroutine calls dial
	// at a time. Unlike sync.Once this allows retries after a failed dial.
	r.connectMu.Lock()
	defer r.connectMu.Unlock()

	// Double-check: another goroutine may have connected while we waited.
	r.mu.Lock()
	if r.client != nil {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	if err := r.dial(ctx); err != nil {
		return err
	}

	// Start the auto-reconnect goroutine exactly once per successful dial.
	stop := make(chan struct{})
	r.mu.Lock()
	r.kaStop = stop
	r.mu.Unlock()

	go r.keepAlive(stop)
	return nil
}

// dial creates and starts a new bring.Client, waiting for SessionActive.
// Must NOT be called with r.mu held.
func (r *BringRDP) dial(ctx context.Context) error {
	guacConfig := r.buildGuacParams()

	client, err := bring.NewClient(r.guacdAddr, "rdp", guacConfig, &bring.DefaultLogger{Quiet: true})
	if err != nil {
		return fmt.Errorf("guacd connect: %w", err)
	}

	done := make(chan struct{})

	// Wire a sync tracker so typeText can use frame boundaries as a
	// barrier between keydown and keyup events.
	st := newSyncTracker()
	client.OnSync(func(_ image.Image, _ int64) {
		st.signal()
	})

	r.mu.Lock()
	r.client = client
	r.done = done
	r.syncTracker = st
	r.mu.Unlock()

	go func() {
		defer close(done)
		client.Start()
	}()

	// On failure, stop the client and reset all state so a subsequent dial
	// can start fresh. The <-done wait is bounded so a misbehaving bring
	// client (e.g. one stuck during a Guacamole handshake) cannot cause
	// cleanup to hang indefinitely.
	cleanup := func() {
		client.Stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			// bring client did not exit cleanly; continue anyway
		}
		r.mu.Lock()
		r.client = nil
		r.connID = ""
		r.live = false
		r.done = nil
		r.syncTracker = nil
		r.mu.Unlock()
	}

	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cleanup()
			return ctx.Err()
		case <-deadline:
			cleanup()
			return fmt.Errorf("timeout waiting for RDP session to become active")
		case <-done:
			cleanup()
			return fmt.Errorf("RDP session closed during connection")
		case <-ticker.C:
			if client.State() == bring.SessionActive {
				r.mu.Lock()
				r.connID = client.ConnectionID()
				r.live = true
				r.mu.Unlock()
				// Give the display a moment to receive the initial frame.
				time.Sleep(500 * time.Millisecond)
				return nil
			}
		}
	}
}

// keepAlive watches the bring.Client's done channel and reconnects as soon
// as the session is detected as dropped. This is much faster than polling
// because it reacts the moment client.Start() returns.
//
// A secondary 5-second polling loop also watches client.State() so that a
// hard-stopped VM (no TCP FIN) is detected before the OS-level TCP timeout.
func (r *BringRDP) keepAlive(stop <-chan struct{}) {
	var dialFailures int // consecutive dial errors; reset to 0 on success
	for {
		// Grab the current client/done while holding the lock.
		r.mu.Lock()
		doneCh := r.done
		client := r.client
		r.mu.Unlock()

		if doneCh != nil && client != nil {
			// Race the clean "done" signal against a 5 s state-poll tick.
			dropped := false
		watchLoop:
			for {
				select {
				case <-stop:
					return
				case <-doneCh:
					dropped = true
					break watchLoop
				case <-time.After(5 * time.Second):
					r.mu.Lock()
					cl := r.client
					lv := r.live
					r.mu.Unlock()
					if cl == nil {
						return // Disconnect() was called
					}
					if lv && cl.State() != bring.SessionActive {
						log.Printf("[rdp %s] state poll detected session drop", r.name)
						cl.Stop() // accelerate the done close
						dropped = true
						break watchLoop
					}
				}
			}

			if !dropped {
				return
			}

			// If Disconnect() already cleaned up (r.client == nil), exit.
			r.mu.Lock()
			explicit := r.client == nil
			r.mu.Unlock()
			if explicit {
				return
			}

			log.Printf("[rdp %s] session dropped, reconnecting in 5s", r.name)
			r.mu.Lock()
			r.live = false
			r.connID = ""
			r.done = nil
			r.mu.Unlock()
		}

		// Brief pause before reconnecting (or before retrying a failed reconnect).
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}

		if err := r.dial(context.Background()); err != nil {
			dialFailures++
			// Exponential back-off: 10s, 20s, 40s … capped at 5 minutes.
			backoff := time.Duration(10<<uint(dialFailures-1)) * time.Second
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			log.Printf("[rdp %s] reconnect failed (%d consecutive): %v; retrying in %v",
				r.name, dialFailures, err, backoff)
			select {
			case <-stop:
				return
			case <-time.After(backoff):
			}
		} else {
			if dialFailures > 0 {
				log.Printf("[rdp %s] reconnected after %d failed attempt(s), connID=%s",
					r.name, dialFailures, r.GuacdConnectionID())
				dialFailures = 0
			} else {
				log.Printf("[rdp %s] reconnected, connID=%s", r.name, r.GuacdConnectionID())
			}
		}
	}
}

func (r *BringRDP) Disconnect() error {
	// Stop the keepAlive goroutine before touching the client so it cannot
	// race to reconnect after we clean up.
	r.mu.Lock()
	kaStop := r.kaStop
	r.kaStop = nil
	r.mu.Unlock()

	if kaStop != nil {
		close(kaStop)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return nil
	}

	r.client.Stop()
	if r.done != nil {
		<-r.done
		r.done = nil
	}
	r.client = nil
	r.live = false
	r.connID = ""
	return nil
}

func (r *BringRDP) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live
}

// AgentActive reports whether an agent has claimed control of this session.
func (r *BringRDP) AgentActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agentActive
}

// SetAgentActive marks whether an agent currently holds this session.
// It is intended to be called by the manager when an agent acquires or
// releases the desktop.
func (r *BringRDP) SetAgentActive(active bool) {
	r.mu.Lock()
	r.agentActive = active
	r.mu.Unlock()
}

// SetConnected marks the session as live or not without dialing guacd.
// Intended for use in tests that need to simulate an active session.
func (r *BringRDP) SetConnected(b bool) {
	r.mu.Lock()
	r.live = b
	r.mu.Unlock()
}

func (r *BringRDP) Screenshot(ctx context.Context) ([]byte, error) {
	c := r.getClient()
	if c == nil {
		return nil, fmt.Errorf("RDP session not connected")
	}
	return screenshot(ctx, c)
}

// screenshot captures a PNG screenshot from a connected Guacamole client.
// It waits up to 10 s for the first non-empty frame before returning an error.
func screenshot(ctx context.Context, c Client) ([]byte, error) {
	var img image.Image
	deadline := time.Now().Add(10 * time.Second)
	for {
		img, _ = c.Screen()
		if img != nil && img.Bounds().Dx() > 0 && img.Bounds().Dy() > 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for first RDP frame")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode screenshot: %w", err)
	}
	return buf.Bytes(), nil
}

// Mouse input — bring uses different button constants than VBox:
//
//	VBox:  1=left, 2=right, 4=middle
//	bring: MouseLeft=1, MouseMiddle=2, MouseRight=4
//
// The Desktop interface uses VBox convention, so we translate here.
func (r *BringRDP) MouseClick(x, y, buttonMask int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return mouseClick(c, x, y, buttonMask)
}

func mouseClick(c Client, x, y, buttonMask int) error {
	p := image.Pt(x, y)
	btn := translateButton(buttonMask)
	if err := c.SendMouse(p, btn); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return c.SendMouse(p)
}

func (r *BringRDP) MouseMoveAbsolute(x, y int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return c.SendMouse(image.Pt(x, y))
}

func (r *BringRDP) MouseDoubleClick(x, y, buttonMask int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	if err := mouseClick(c, x, y, buttonMask); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return mouseClick(c, x, y, buttonMask)
}

func (r *BringRDP) MouseScroll(x, y, dz int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return mouseScroll(c, x, y, dz)
}

// mouseScroll sends scroll events. Positive dz scrolls up, negative scrolls down.
func mouseScroll(c Client, x, y, dz int) error {
	p := image.Pt(x, y)
	if err := c.SendMouse(p); err != nil {
		return err
	}
	var btn MouseButton
	if dz > 0 {
		btn = bring.MouseUp
	} else {
		btn = bring.MouseDown
		dz = -dz
	}
	for i := 0; i < dz; i++ {
		if err := c.SendMouse(p, btn); err != nil {
			return err
		}
		if err := c.SendMouse(p); err != nil {
			return err
		}
	}
	return nil
}

func (r *BringRDP) MouseDown(x, y, buttonMask int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return c.SendMouse(image.Pt(x, y), translateButton(buttonMask))
}

func (r *BringRDP) MouseUp(x, y int) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return c.SendMouse(image.Pt(x, y)) // no buttons = release
}

func (r *BringRDP) TypeText(ctx context.Context, text string) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	delay, err := resolveKeyDelay(r.config)
	if err != nil {
		return err
	}
	r.mu.Lock()
	st := r.syncTracker
	r.mu.Unlock()
	return typeText(c, text, delay, st)
}

// typeText dispatches text character-by-character using explicit key-down and
// key-up events for every character.
//
// For printable ASCII we send the character's keysym (its Unicode code point)
// directly and let guacd's RDP keyboard engine translate it to the correct
// RDP scancode and modifier combination. We must NOT manually hold Shift and
// send the unshifted key: guacd tracks modifier state internally and will
// release Shift to ensure a lowercase keysym arrives as lowercase, so sending
// keysym 97 ('a') while holding shift produces 'a', not 'A'. Instead, we send
// keysym 65 ('A') and guacd adds shift automatically.
//
// A one-time releaseModifiers call at the start clears any stale state left
// by a previous keyPress (e.g. ctrl+a) before we begin typing.
//
// Between keydown and keyup we use a sync-barrier: we wait for the guacd
// sync generation to advance past the value recorded before the keydown was
// sent. A guacd "sync" instruction is emitted after every rendered frame,
// which means it only arrives after guacd's RDP main thread has finished one
// complete iteration — including draining all pending FreeRDP display events
// and calling guac_rdp_handle_input_events. Waiting for this signal guarantees
// that our keydown has actually been dispatched to FreeRDP before we send the
// keyup, eliminating the batching-under-load failure mode.
//
// The barrier is resolution-independent: if no frame interval has been
// observed yet (e.g. first TypeText call after connecting at a high
// resolution), we warm up by waiting for 2 syncs before the first keystroke.
// maxWait is then derived as max(600ms, 4×FrameInterval()) so the timeout
// scales automatically with however fast guacd can render frames at the
// current resolution — slow at 1920×1080, fast at 1280×800, always correct.
//
// Note: a post-keyup sync wait is NOT used. Keyup events rarely trigger a
// display change in the remote application, so guacd does not reliably emit
// a sync after them; waiting would hit the maxWait timeout on nearly every
// character, making typing several times slower. The baseline+2 barrier
// before keyup is sufficient: by the time keyup is sent, the drain that
// processed keydown has provably completed, and the next keydown is sent
// immediately after keyup with no gap needed.
//
// When st is nil (unit tests) we fall back to a plain time.Sleep(keyHold)
// between keydown and keyup, and sleep(delay) between characters.
func typeText(c Client, text string, delay time.Duration, st *syncTracker) error {
	// Normalize Windows line endings so each line break is a single Enter.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// keyHold is the fallback dwell time used only when st is nil (tests).
	// In production st provides a frame-boundary barrier instead.
	keyHold := 10 * time.Millisecond
	if delay == 0 {
		keyHold = 0
	}

	if st != nil {
		// Warm-up: if no frame interval has been observed yet (e.g. this is
		// the first TypeText call after connecting, or the session just
		// established at a high resolution where the first frame is slow),
		// wait for 2 syncs with a generous timeout before typing begins.
		// This populates FrameInterval() so maxWait below is well-calibrated.
		if st.FrameInterval() == 0 {
			warmupBase := st.gen.Load()
			st.waitFor(warmupBase+2, 0, 10*time.Second)
		}
	}

	// Clear any stale modifier state once before we start typing.
	releaseModifiers(c)

	for _, r := range []rune(text) {

		var code bring.KeyCode

		if ctrlCode, ok := ctrlKeyCodes[r]; ok {
			code = ctrlCode
		} else {
			// For all printable ASCII 32–126, KeyCode(r) == the keysym.
			// guacd maps keysym 65 ('A') → Shift+scancode(a) automatically.
			// We must not try to hold Shift ourselves.
			code = bring.KeyCode(r)
		}

		if err := c.SendKey(code, true); err != nil {
			return err
		}

		if st != nil {
			// Record the baseline AFTER the keydown TCP send completes.
			// We then wait for gen=baseline+2 (two syncs).
			//
			// Each guacd frame cycle is: drain → render → sync.
			// The sync at gen=baseline+1 is emitted by the frame whose
			// drain started at essentially the same instant as the sync.
			// If our keyup arrived while that drain was still running,
			// keydown and keyup would end up in the same drain call and
			// Windows would drop the character.
			//
			// The sync at gen=baseline+2 is emitted only after the
			// following complete drain+render+sync cycle. By the time we
			// receive it the drain that processed our keydown is provably
			// finished, so our keyup cannot land in the same drain.
			// Keydown and keyup are guaranteed to be in different drains.
			//
			// maxWait scales with the observed frame interval so the
			// barrier works correctly at any resolution. 4× gives margin
			// for two back-to-back slow frames with headroom to spare.
			baseline := st.gen.Load()
			fi := st.FrameInterval()
			maxWait := 4 * fi
			if maxWait < 600*time.Millisecond {
				maxWait = 600 * time.Millisecond
			}
			st.waitFor(baseline+2, 0, maxWait)
		} else if keyHold > 0 {
			time.Sleep(keyHold)
		}

		if err := c.SendKey(code, false); err != nil {
			return err
		}

		// Any configured extra delay after keyup (0 by default).
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return nil
}

// modifierKeys is the set of modifier keys released by releaseModifiers.
// Defined at package level so tests can derive the expected count from the
// live list rather than hardcoding a magic number.
var modifierKeys = []bring.KeyCode{
	bring.KeyLeftShift, bring.KeyRightShift,
	bring.KeyLeftControl, bring.KeyRightControl,
	bring.KeyLeftAlt, bring.KeyRightAlt,
	bring.KeyMeta,
}

// releaseModifiers fires a keyup for every modifier key on both sides of the
// keyboard. This resets Windows' key state to a known-clean baseline.
// Errors are ignored: if a key is already up the event is a harmless no-op.
func releaseModifiers(c Client) {
	for _, k := range modifierKeys {
		_ = c.SendKey(k, false)
	}
}



func (r *BringRDP) KeyPress(ctx context.Context, spec string) error {
	c := r.getClient()
	if c == nil {
		return fmt.Errorf("RDP session not connected")
	}
	return keyPress(c, spec)
}

// keyPress parses a key spec (e.g. "ctrl+c", "shift+F4") and sends the
// press/release sequence to the client.
func keyPress(c Client, spec string) error {
	parts := strings.Split(spec, "+")
	keys := make([]KeyCode, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key, ok := resolveKeyCode(part)
		if !ok {
			return fmt.Errorf("unknown key %q", part)
		}
		keys = append(keys, key)
	}
	for _, key := range keys {
		if err := c.SendKey(key, true); err != nil {
			return err
		}
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if err := c.SendKey(keys[i], false); err != nil {
			return err
		}
	}
	return nil
}

func (r *BringRDP) Name() string { return r.name }
func (r *BringRDP) Type() string { return "rdp" }

// GuacdConnectionID returns the guacd connection ID assigned during handshake.
// Returns an empty string if the session is not yet active.
func (r *BringRDP) GuacdConnectionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connID
}

func (r *BringRDP) getClient() *bring.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// translateButton converts VBox button mask to bring MouseButton.
// VBox: 1=left, 2=right, 4=middle
// bring: MouseLeft=1, MouseMiddle=2, MouseRight=4
func translateButton(vboxMask int) bring.MouseButton {
	switch vboxMask {
	case 1:
		return bring.MouseLeft
	case 2:
		return bring.MouseRight
	case 4:
		return bring.MouseMiddle
	default:
		return bring.MouseLeft
	}
}

// resolveKeyCode maps a key name (as used by model adapters and the existing
// VBox scancode tables) to a bring.KeyCode.
func resolveKeyCode(name string) (bring.KeyCode, bool) {
	lower := strings.ToLower(name)

	// Check alias map first
	if canonical, ok := keyAliases[lower]; ok {
		lower = canonical
	}

	// Check special key map
	if code, ok := keyMap[lower]; ok {
		return code, true
	}

	// Single ASCII character
	if len(name) == 1 {
		ch := rune(name[0])
		if ch >= 32 && ch < 127 {
			return bring.KeyCode(ch), true
		}
	}

	return 0, false
}

// keyAliases maps alternate key names to canonical names used in keyMap.
// Mirrors the aliases in vbox/scancodes.go for consistency.
var keyAliases = map[string]string{
	"enter":      "return",
	"esc":        "escape",
	"lshift":     "shift",
	"rshift":     "shift_r",
	"lctrl":      "ctrl",
	"rctrl":      "ctrl_r",
	"lalt":       "alt",
	"ralt":       "alt_r",
	"lsuper":     "super",
	"rsuper":     "super_r",
	"win":        "super",
	"windows":    "super",
	"meta":       "super",
	"cmd":        "super",
	"del":        "delete",
	"ins":        "insert",
	"pgup":       "pageup",
	"pgdn":       "pagedown",
	"page_up":    "pageup",
	"page_down":  "pagedown",
	"arrowup":    "up",
	"arrowdown":  "down",
	"arrowleft":  "left",
	"arrowright": "right",
	"bksp":       "backspace",
}

// keyMap maps canonical key names to bring.KeyCode constants.
var keyMap = map[string]bring.KeyCode{
	"return":      bring.KeyEnter,
	"escape":      bring.KeyEscape,
	"backspace":   bring.KeyBackspace,
	"tab":         bring.KeyTab,
	"space":       bring.KeyCode(' '),
	"delete":      bring.KeyDelete,
	"insert":      bring.KeyInsert,
	"home":        bring.KeyHome,
	"end":         bring.KeyEnd,
	"pageup":      bring.KeyPageUp,
	"pagedown":    bring.KeyPageDown,
	"up":          bring.KeyArrowUp,
	"down":        bring.KeyArrowDown,
	"left":        bring.KeyArrowLeft,
	"right":       bring.KeyArrowRight,
	"shift":       bring.KeyLeftShift,
	"shift_r":     bring.KeyRightShift,
	"ctrl":        bring.KeyLeftControl,
	"ctrl_r":      bring.KeyRightControl,
	"alt":         bring.KeyLeftAlt,
	"alt_r":       bring.KeyRightAlt,
	"super":       bring.KeySuper,
	"super_r":     bring.KeySuper,
	"capslock":    bring.KeyCapsLock,
	"numlock":     bring.KeyNumLock,
	"scrolllock":  bring.KeyScroll,
	"f1":          bring.KeyF1,
	"f2":          bring.KeyF2,
	"f3":          bring.KeyF3,
	"f4":          bring.KeyF4,
	"f5":          bring.KeyF5,
	"f6":          bring.KeyF6,
	"f7":          bring.KeyF7,
	"f8":          bring.KeyF8,
	"f9":          bring.KeyF9,
	"f10":         bring.KeyF10,
	"f11":         bring.KeyF11,
	"f12":         bring.KeyF12,
	"menu":        bring.KeyContextMenu,
	"pause":       bring.KeyPause,
	"printscreen": bring.KeyPrintScreen,
}

// ctrlKeyCodes maps control/whitespace characters to bring key codes so they
// are sent as explicit key press/release events rather than raw Unicode, which
// Windows RDP ignores or mishandles.
var ctrlKeyCodes = map[rune]bring.KeyCode{
	'\n': bring.KeyEnter,
	'\t': bring.KeyTab,
	'\b': bring.KeyBackspace,
	'\x1b': bring.KeyEscape,
}
