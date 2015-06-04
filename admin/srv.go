package admin

import (
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/server"
)

var router = httprouter.New()

// Serve starts the admin server. It blocks indefinitely.
func Serve(address string, tls server.TLSConfig) {
	if tls.Enabled {
		http.ListenAndServeTLS(address, tls.Certificate, tls.Key, router)
	} else {
		http.ListenAndServe(address, router)
	}
}

// auth is a middleware layer that authenticates a request to the server
// management API
func auth(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		// TODO: Authenticate
		w.Header().Set("X-Temp-Auth", "true")
		h(w, r, p)
	}
}

// handleError handles errors during API requests
func handleError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if err != nil {
		log.Println(err)
	}
	w.WriteHeader(status)
	// TODO: This'll need to be JSON or something
	// NOTE/SUGGESTION: If HTTP status code is in the 400s, write error text to client?
	fmt.Fprintf(w, "%d %s\n", status, http.StatusText(status))
}

// virtualHost gets only the VirtualHost only of the address
// addr. If nil, the address was not found.
func virtualHost(addr string) *server.VirtualHost {
	_, vh, _ := serverAndVirtualHost(addr)
	return vh
}

// serverAndVirtualHost gets the server and VirtualHost of the
// address addr. If either value is nil, the address could not
// be found in the list of servers, in which case the last arg
// will be false.
func serverAndVirtualHost(addr string) (*server.Server, *server.VirtualHost, bool) {
	// The addr passed in may contain a host and port, but the
	// server only arranges virtualhosts by host, not both, so
	// we have to split these to make sure we got the right port.
	srv, vh := getServerAndVirtualHost(safeSplitHostPort(addr))
	return srv, vh, srv != nil && vh != nil
}

// serverAndVirtualHost gets the server and VirtualHost by the
// host and port information. If either value is nil, the host
// and port combination could not be found.
func getServerAndVirtualHost(host, port string) (*server.Server, *server.VirtualHost) {
	for _, s := range app.Servers {
		_, sPort, err := net.SplitHostPort(s.Address)
		if err != nil || sPort != port {
			continue
		}
		if vh, ok := s.Vhosts[host]; ok {
			return s, vh
		}
	}
	return nil, nil
}

// safeSplitHostPort splits the host and port. It assumes
// that if there is an error splitting, the entire string
// s must be the host, and we resort to the default port.
func safeSplitHostPort(s string) (string, string) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host = s
		port = config.DefaultPort
	}
	return host, port
}
