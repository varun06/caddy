// Package websockets implements a WebSocket server by executing
// a command and piping its input and output through the WebSocket
// connection.
package websockets

import (
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/mholt/caddy/middleware"
)

type (
	// WebSockets is a type that holds configuration for the
	// websocket middleware generally, like a list of all the
	// websocket endpoints.
	WebSockets struct {
		// Next is the next HTTP handler in the chain for when the path doesn't match
		Next middleware.Handler

		// Sockets holds all the web socket endpoint configurations
		Sockets []Config
	}

	// Config holds the configuration for a single websocket
	// endpoint which may serve multiple websocket connections.
	Config struct {
		Path      string
		Command   string
		Arguments []string
		Respawn   bool // TODO: Not used, but parser supports it until we decide on it
	}
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

func wshandler(w http.ResponseWriter, r *http.Request) {
	conn, err := wsupgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Failed to set websocket upgrade: %+v", err)
		return
	}
	defer conn.Close()

	for {
		messageType, msg, err := conn.ReadMessage()
		if err != nil {
			fmt.Println("read error: %+v", err)
			break
		}
		err = conn.WriteMessage(messageType, msg)
		if err != nil {
			fmt.Println("write error: %+v", err)
			break
		}
	}
}

var (
	// GatewayInterface is the dialect of CGI being used by the server
	// to communicate with the script.  See CGI spec, 4.1.4
	GatewayInterface string

	// ServerSoftware is the name and version of the information server
	// software making the CGI request.  See CGI spec, 4.1.17
	ServerSoftware string
)
