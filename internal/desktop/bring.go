package desktop

import (
	"image"

	"github.com/deluan/bring"
)

// bringClient is the subset of *bring.Client that RDP depends on.
// Keeping it as a narrow interface allows unit tests to inject a mock
// without a real guacd connection; *bring.Client satisfies it in production.
type bringClient interface {
	Start()
	Stop()
	State() bring.SessionState
	// ConnectionID returns the guacd connection ID assigned during handshake.
	// Empty until the session reaches SessionActive.
	ConnectionID() string
	Screen() (image.Image, int64)
	SendMouse(p image.Point, pressedButtons ...bring.MouseButton) error
	SendText(sequence string) error
	SendKey(key bring.KeyCode, pressed bool) error
}
