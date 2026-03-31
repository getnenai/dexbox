module github.com/getnenai/dexbox

go 1.25.0

require (
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/deluan/bring v0.0.8
	github.com/gorilla/websocket v1.5.0
	github.com/joho/godotenv v1.5.1
	github.com/modelcontextprotocol/go-sdk v1.4.1
	github.com/spf13/cobra v1.10.2
	github.com/wwt/guac v1.3.3
	golang.org/x/image v0.24.0
)

require (
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/x/ansi v0.8.0 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.13-0.20250311204145-2c3ea96c31dd // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sirupsen/logrus v1.9.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tfriedel6/canvas v0.12.1 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)

// getnenai/guac mirrors wwt/guac with minor fixes needed for VRDE compatibility.
replace github.com/wwt/guac => github.com/getnenai/guac v1.3.3

// getnenai/bring forks deluan/bring to expose Client.ConnectionID(), which returns
// the guacd connection ID after handshake. This is required so the browser tunnel
// can join an active agent session read-only instead of opening a competing RDP
// connection. See: https://github.com/getnenai/bring
replace github.com/deluan/bring => github.com/getnenai/bring v0.0.8-nen-0.1
