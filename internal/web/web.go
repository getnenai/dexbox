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
)

//go:embed static/viewer.html
var viewerHTML []byte

//go:embed static
var staticFiles embed.FS

// Handler returns an http.Handler that serves the browser UI routes:
//
//	GET /desktops/<name>/view   — serves the HTML viewer
//	GET /desktops/<name>/tunnel — wwt/guac WebSocket tunnel to guacd
//	GET /desktops/<name>/events — SSE stream of agent session events
func Handler(mgr *desktop.Manager, guacdAddr string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/desktops/", func(w http.ResponseWriter, r *http.Request) {
		// Parse /desktops/<name>/view, /desktops/<name>/tunnel, or /desktops/<name>/events
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
			serveTunnel(w, r, name, mgr, guacdAddr)
		case "events":
			serveEvents(w, r, name, mgr)
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

func serveTunnel(w http.ResponseWriter, r *http.Request, name string, mgr *desktop.Manager, guacdAddr string) {
	cfg, ok := mgr.RDPConfig(name)
	if !ok {
		http.Error(w, fmt.Sprintf("desktop %q is not an RDP connection; browser view requires RDP or VRDE-enabled VMs", name), http.StatusNotFound)
		return
	}

	// If an agent RDP session is active, join it read-only by prepending "$"
	// to the connection ID. This reuses the existing guacd↔RDP connection
	// instead of opening a competing session (which would kick out the agent).
	connectionID := ""
	if rdp, active := mgr.ActiveRDP(name); active {
		if id := rdp.GuacdConnectionID(); id != "" {
			connectionID = "$" + id
			log.Printf("[tunnel %s] joining existing agent session %s read-only", name, id)
		}
	}

	// Build Guacamole configuration.
	config := guac.NewGuacamoleConfiguration()
	if connectionID != "" {
		// Joining an existing session: only the connection ID is needed.
		config.ConnectionID = connectionID
	} else {
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
	}

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

// serveEvents streams agent session events (agent_connected / agent_disconnected)
// as Server-Sent Events. The browser viewer subscribes to know when to switch
// between read-only (agent active) and interactive (agent gone) tunnel modes.
func serveEvents(w http.ResponseWriter, r *http.Request, name string, mgr *desktop.Manager) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, cancel := mgr.Subscribe(name)
	defer cancel()

	// Send the current state immediately so the browser can start in the
	// right mode without waiting for the next transition.
	if _, active := mgr.ActiveRDP(name); active {
		fmt.Fprintf(w, "data: agent_connected\n\n")
	} else {
		fmt.Fprintf(w, "data: agent_disconnected\n\n")
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			var data string
			switch evt.Type {
			case desktop.SessionUp:
				data = "agent_connected"
			case desktop.SessionDown:
				data = "agent_disconnected"
			default:
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
