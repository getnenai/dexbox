//go:build !windows

package desktop

import (
	"context"
	"fmt"
)

// NewNative creates a NativeDesktop. On non-Windows platforms the desktop
// cannot capture the screen or inject input, so every method returns an error.
func NewNative(name string) *NativeDesktop {
	return &NativeDesktop{name: name}
}

// NativeDesktop is a stub for non-Windows builds. It satisfies the Desktop
// interface at compile time but all operations return errNotWindows.
type NativeDesktop struct {
	name string
}

var errNotWindows = fmt.Errorf("native desktop backend is only available on Windows")

func (n *NativeDesktop) Connect(context.Context) error               { return errNotWindows }
func (n *NativeDesktop) Disconnect() error                           { return errNotWindows }
func (n *NativeDesktop) Connected() bool                             { return false }
func (n *NativeDesktop) Screenshot(context.Context) ([]byte, error)  { return nil, errNotWindows }
func (n *NativeDesktop) MouseClick(x, y, buttonMask int) error       { return errNotWindows }
func (n *NativeDesktop) MouseMoveAbsolute(x, y int) error            { return errNotWindows }
func (n *NativeDesktop) MouseDoubleClick(x, y, buttonMask int) error { return errNotWindows }
func (n *NativeDesktop) MouseScroll(x, y, dz int) error              { return errNotWindows }
func (n *NativeDesktop) MouseDown(x, y, buttonMask int) error        { return errNotWindows }
func (n *NativeDesktop) MouseUp(x, y int) error                     { return errNotWindows }
func (n *NativeDesktop) TypeText(context.Context, string) error      { return errNotWindows }
func (n *NativeDesktop) KeyPress(context.Context, string) error      { return errNotWindows }
func (n *NativeDesktop) Name() string                                { return n.name }
func (n *NativeDesktop) Type() string                                { return "native" }
