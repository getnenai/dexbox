package desktop

import (
	"context"
	"time"

	"github.com/getnenai/dexbox/internal/vbox"
)

// VBox implements Desktop by combining VBoxManage screenshots with SOAP
// mouse/keyboard input. It wraps the existing vbox.Manager session lifecycle.
type VBox struct {
	name    string
	manager *vbox.Manager
}

// NewVBox creates a VBox desktop backed by the given manager.
// The manager owns the SOAP session lifecycle.
func NewVBox(name string, manager *vbox.Manager) *VBox {
	return &VBox{name: name, manager: manager}
}

func (v *VBox) Connect(ctx context.Context) error {
	return v.manager.ConnectSOAP(ctx, v.name)
}

func (v *VBox) Disconnect() error {
	soap := v.manager.SOAPClient(v.name)
	if soap == nil {
		return nil
	}
	return soap.Disconnect()
}

func (v *VBox) Connected() bool {
	return v.manager.SOAPClient(v.name) != nil
}

func (v *VBox) Screenshot(ctx context.Context) ([]byte, error) {
	return vbox.Screenshot(ctx, v.name)
}

func (v *VBox) MouseClick(x, y, buttonMask int) error {
	return v.soap().MouseClick(x, y, buttonMask)
}

func (v *VBox) MouseMoveAbsolute(x, y int) error {
	return v.soap().MouseMoveAbsolute(x, y)
}

func (v *VBox) MouseDoubleClick(x, y, buttonMask int) error {
	return v.soap().MouseDoubleClick(x, y, buttonMask)
}

func (v *VBox) MouseScroll(x, y, dz int) error {
	return v.soap().MouseScroll(x, y, dz)
}

func (v *VBox) MouseDown(x, y, buttonMask int) error {
	return v.soap().MouseDown(x, y, buttonMask)
}

func (v *VBox) MouseUp(x, y int) error {
	return v.soap().MouseUp(x, y)
}

func (v *VBox) TypeText(ctx context.Context, text string) error {
	codes := vbox.TextToScancodes(text)
	// Send in batches to avoid oversized argument lists.
	const batchSize = 60
	for i := 0; i < len(codes); i += batchSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		end := i + batchSize
		if end > len(codes) {
			end = len(codes)
		}
		if err := v.soap().KeyboardPutScancodes(codes[i:end]); err != nil {
			return err
		}
		if end < len(codes) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	return nil
}

func (v *VBox) KeyPress(ctx context.Context, spec string) error {
	codes := vbox.KeyToScancodes(spec)
	return v.soap().KeyboardPutScancodes(codes)
}

func (v *VBox) Name() string { return v.name }
func (v *VBox) Type() string { return "vm" }

func (v *VBox) soap() *vbox.SOAPClient {
	return v.manager.SOAPClient(v.name)
}
