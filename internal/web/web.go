// Package web provides the browser-based remote desktop viewer.
// It serves an HTML page with guacamole-common-js and a WebSocket
// tunnel endpoint that proxies the Guacamole protocol to guacd.
package web

import (
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/wwt/guac"

	"github.com/getnenai/dexbox/internal/desktop"
	"github.com/getnenai/dexbox/internal/vbox"
)

//go:embed static/viewer.html
var viewerHTML []byte

//go:embed static
var staticFiles embed.FS

// Handler returns an http.Handler that serves the browser UI routes:
//
//	GET /desktops/<name>/view   — serves the HTML viewer
//	GET /desktops/<name>/tunnel — wwt/guac WebSocket tunnel to guacd
func Handler(store *desktop.ConnectionStore, vboxMgr *vbox.Manager, guacdAddr string) http.Handler {
	mux := http.NewServeMux()

	// View route — serves the static HTML
	mux.HandleFunc("/desktops/", func(w http.ResponseWriter, r *http.Request) {
		// Parse /desktops/<name>/view or /desktops/<name>/tunnel
		path := strings.TrimPrefix(r.URL.Path, "/desktops/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}

		name := parts[0]
		action := parts[1]

		switch action {
		case "view":
			serveViewer(w, r)
		case "tunnel":
			serveTunnel(w, r, name, store, vboxMgr, guacdAddr)
		default:
			http.NotFound(w, r)
		}
	})

	return mux
}

// StaticHandler returns an http.Handler that serves embedded static assets
// under the /static/ prefix.
func StaticHandler() http.Handler {
	return http.FileServer(http.FS(staticFiles))
}

func serveViewer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(viewerHTML)
}

func serveTunnel(w http.ResponseWriter, r *http.Request, name string, store *desktop.ConnectionStore, vboxMgr *vbox.Manager, guacdAddr string) {
	cfg, ok := store.Get(name)
	if !ok {
		http.Error(w, fmt.Sprintf("desktop %q is not an RDP connection; browser view requires RDP or VRDE-enabled VMs", name), http.StatusNotFound)
		return
	}

	// Build Guacamole configuration.
	config := guac.NewGuacamoleConfiguration()
	config.Protocol = "rdp"
	security := cfg.Security
	if security == "" {
		security = "any"
	}
	config.Parameters = map[string]string{
		"hostname":         cfg.Host,
		"port":             fmt.Sprintf("%d", cfg.Port),
		"username":         cfg.Username,
		"password":         cfg.Password,
		"security":         security,
		"ignore-cert":      fmt.Sprintf("%t", cfg.IgnoreCert),
		"disable-audio":    "true",
		"enable-wallpaper": "false",
	}
	config.OptimalScreenWidth = cfg.Width
	config.OptimalScreenHeight = cfg.Height
	config.ImageMimetypes = []string{"image/png", "image/jpeg"}

	// Use the wwt/guac WebsocketServer for the WebSocket↔guacd proxy.
	// It correctly parses Guacamole protocol instructions in both directions
	// which is critical for VirtualBox VRDE which needs precise protocol
	// handling to deliver incremental display updates.
	//
	// We use the OnConnectWs callback to send the tunnel UUID to the
	// guacamole-common-js client (required for OPEN state transition).
	wsServer := guac.NewWebsocketServer(func(r *http.Request) (guac.Tunnel, error) {
		addr, err := net.ResolveTCPAddr("tcp", guacdAddr)
		if err != nil {
			return nil, fmt.Errorf("resolve guacd: %w", err)
		}
		conn, err := net.DialTCP("tcp", nil, addr)
		if err != nil {
			return nil, fmt.Errorf("connect guacd: %w", err)
		}

		stream := guac.NewStream(conn, guac.SocketTimeout)
		if err := stream.Handshake(config); err != nil {
			conn.Close()
			return nil, fmt.Errorf("guacd handshake: %w", err)
		}

		log.Printf("[tunnel %s] guacd connected, connID=%s", name, stream.ConnectionID)
		return guac.NewSimpleTunnel(stream), nil
	})

	wsServer.OnConnectWs = func(id string, ws *websocket.Conn, r *http.Request) {
		uuidIns := guac.NewInstruction(guac.InternalDataOpcode, id)
		log.Printf("[tunnel %s] sending UUID: %s", name, uuidIns.String())
		_ = ws.WriteMessage(websocket.TextMessage, uuidIns.Byte())
	}

	wsServer.OnDisconnectWs = func(id string, ws *websocket.Conn, r *http.Request, t guac.Tunnel) {
		log.Printf("[tunnel %s] disconnected", name)
	}

	wsServer.ServeHTTP(w, r)
}
