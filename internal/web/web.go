// Package web provides the browser-based remote desktop viewer.
// It serves an HTML page with guacamole-common-js and a WebSocket
// tunnel endpoint that proxies the Guacamole protocol to guacd.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

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

// writeJSONError writes a JSON error response with the given status code.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	body, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		// Fallback: msg contains something json.Marshal cannot encode (should
		// never happen for plain strings, but be safe).
		log.Printf("writeJSONError: marshal failed: %v", err)
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

func serveViewer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(viewerHTML)
}

func serveTunnel(w http.ResponseWriter, r *http.Request, name string, mgr *desktop.Manager, guacdAddr string) {
	// Connect on demand: if no live session exists yet, establish one now.
	// This happens on first use (first viewer open or first agent Up()).
	ctx := r.Context()

	if _, ok := mgr.RDPConfig(name); !ok {
		http.NotFound(w, r)
		return
	}

	// Reserve the viewer slot before EnsureRDPConnected so idle teardown cannot
	// remove the session between dial and WebSocket upgrade. Roll back on any
	// path that exits before the client WebSocket is established.
	var wsUp atomic.Bool
	defer func() {
		if !wsUp.Load() {
			mgr.ViewerDisconnected(name)
		}
	}()
	mgr.ViewerConnected(name)

	if err := mgr.EnsureRDPConnected(ctx, name); err != nil {
		log.Printf("[tunnel %s] connect failed: %v", name, err)
		if errors.Is(err, desktop.ErrDesktopNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSONError(w, http.StatusServiceUnavailable, "rdp session not ready")
		return
	}

	// The web viewer always joins the server's persistent bring.Client session.
	// It never creates its own full RDP connection — that would compete with
	// the server's session and disconnect the agent.
	rdp, ok := mgr.ActiveRDP(name)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "rdp session not ready, connection ID unavailable")
		return
	}

	connectionID := rdp.GuacdConnectionID()
	if connectionID == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "rdp session not ready, connection ID unavailable")
		return
	}

	agentActive := mgr.IsAgentActive(name)
	log.Printf("[tunnel %s] joining session %s (agent_active=%v)", name, connectionID, agentActive)

	// Build Guacamole join configuration.
	config := guac.NewGuacamoleConfiguration()
	config.ConnectionID = connectionID
	if agentActive {
		// Tell guacd to enforce read-only at the protocol level so the
		// viewer's keyboard/mouse events are silently dropped.
		config.Parameters = map[string]string{"read-only": "true"}
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
		wsUp.Store(true)
		uuidIns := guac.NewInstruction(guac.InternalDataOpcode, id)
		log.Printf("[tunnel %s] sending UUID: %s", name, uuidIns.String())
		_ = ws.WriteMessage(websocket.TextMessage, uuidIns.Byte())
	}

	wsServer.OnDisconnectWs = func(id string, ws *websocket.Conn, r *http.Request, t guac.Tunnel) {
		mgr.ViewerDisconnected(name)
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
	if mgr.IsAgentActive(name) {
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
