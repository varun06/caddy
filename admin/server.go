package admin

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/server"
)

func init() {
	router.GET("/", auth(serverList))
	router.POST("/", auth(serversCreate))
	router.PUT("/", auth(serversReplace))
	router.GET("/:addr", auth(serverInfo))
	router.DELETE("/:addr", auth(serverStop))
}

var serverWg sync.WaitGroup

// serverList shows the list of servers and their information.
func serverList(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	respondJSON(w, r, app.Servers, http.StatusOK)
}

// serversCreate creates and starts servers using the contents of the request body.
// It does not shut down any server instances UNLESS "replace=true" is found in the
// query string and the input specifies a server with the same host:port as an existing
// server. In that case, only the overlapping server is (gracefully) shut down
// and will be restarted with the new configuration. If there is an error, not all
// the new servers may be started. This handler is non-blocking.
func serversCreate(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	r.ParseForm()
	replace := r.Form.Get("replace") == "true"

	err := InitializeReadConfig("HTTP_POST", r.Body, replace)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// serversReplace gracefully shuts down all listening servers and starts up
// new ones based on the contents of the configuration that is found in the
// response body. If there are any errors, the configuration is rolled back
// so the downtime is no more than a couple seconds. It is possible for the
// failover to fail, in which case the failing server will not launch. This
// handler is partially blocking.
func serversReplace(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	// Keep current configuration in case we need to roll back
	app.ServersMutex.Lock()
	backup := app.Servers
	app.ServersMutex.Unlock()

	// Delete all existing servers
	app.ServersMutex.Lock()
	StopAllServers()
	app.Servers = []*server.Server{}
	app.ServersMutex.Unlock()

	// Create and start new servers
	err := InitializeReadConfig("HTTP_POST", r.Body, false)
	// TODO: Health check is needed, in case binding the listening socket failed.
	if err != nil {
		// Oh no! Roll back.
		go func() {
			app.ServersMutex.Lock()
			StopAllServers()
			app.Servers = backup
			StartAllServers() // hopefully this works
			app.ServersMutex.Unlock()
		}()

		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// serverInfo returns information about a specific server/virtualhost.
func serverInfo(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	vh := virtualHost(p.ByName("addr"))
	if vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}
	respondJSON(w, r, vh.Config, http.StatusOK)
}

// serverStop stops a running server (or virtualhost) with a graceful shutdown and
// deletes the server. This function is non-blocking and safe for concurrent use.
func serverStop(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	host, port := safeSplitHostPort(p.ByName("addr"))
	srv, vh := getServerAndVirtualHost(host, port)
	if srv == nil || vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	if len(srv.Vhosts) == 1 {
		// Graceful shutdown
		srv.Stop(app.ShutdownCutoff)
	} else {
		// Stopping a whole server will automatically call Stop
		// on all its virtualhosts, but we only stop the server
		// if there are no more virtualhosts left on it. So
		// we must stop this virtualhost manually in that case.
		vh.Stop()
		delete(srv.Vhosts, host)
	}

	w.WriteHeader(http.StatusAccepted)
}

// InitializeReadConfig reads the configuration from body and starts new
// servers. If replace is true, a server that has the same host and port
// as a new one will be replaced with the new one, no questions asked.
// If replace is false, the same host:port will conflict and cause an
// error.
//
// This function is safe for concurrent use.
func InitializeReadConfig(filename string, body io.Reader, replace bool) error {
	// Parse and load all configurations
	configs, err := config.Load(filename, body)
	if err != nil {
		return err
	}

	// Arrange servers by bind address (resolves hostnames)
	bindings, err := config.ArrangeBindings(configs)
	if err != nil {
		return err
	}

	return InitializeWithBindings(filename, bindings, replace)
}

// InitializeWithBindings is like InitializeReadConfig except that it
// sets up servers using pre-made Config structs organized by net
// address, rather than reading and parsing the config from scratch.
// Call config.ArrangeBindings to get the proper 'bindings' input.
//
// This function is safe for concurrent use.
func InitializeWithBindings(filename string, bindings map[*net.TCPAddr][]*server.Config, replace bool) error {
	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	// If replacing is not allowed, make sure each virtualhost is unique
	// BEFORE we start the servers, so we don't end up with a partially
	// fulfilled request.
	if !replace {
		for addr, configs := range bindings {
			for _, existingServer := range app.Servers {
				for _, cfg := range configs {
					if _, vhostExists := existingServer.Vhosts[cfg.Host]; vhostExists {
						return errors.New(cfg.Host + " already listening at " + addr.String())
					}
				}
			}
		}
	}

	// For every listener address, we need to iterate its configs/virtualhosts.
	for addr, configs := range bindings {
		// Create a server that will build the virtual host
		s, err := server.New(addr.String(), configs, configs[0].TLS.Enabled)
		if err != nil {
			return err
		}
		s.HTTP2 = app.Http2 // TODO: This setting is temporary

		// See if there's a server that is already listening at the address
		// If so, we just add the virtualhost to that server.
		var hasListener bool
		for _, existingServer := range app.Servers {
			if addr.String() == existingServer.Address {
				hasListener = true
				for _, cfg := range configs {
					vh := s.Vhosts[cfg.Host]
					existingServer.Vhosts[cfg.Host] = vh
					err := vh.Start()
					if err != nil {
						return err
					}
				}
				break
			}
		}

		if hasListener {
			continue
		}

		// Initiate the new server that will operate the listener for this virtualhost
		app.Servers = append(app.Servers, s)
		StartServer(s)
	}

	return nil
}

// StartAllServers correctly starts all the servers in
// app.Servers. A call to this function is non-blocking.
//
// This function is NOT safe for concurrent use.
func StartAllServers() {
	for _, s := range app.Servers {
		StartServer(s)
	}
}

// StartServer starts s correctly. This function is non-blocking
// but will cause anything waiting on app.Wg (or serverWg) to
// block until s is terminated. It does NOT add s to the
// app.Servers slice, but it WILL delete s from the slice
// after the server shuts down (if it is in the slice).
//
// This function is safe for concurrent use.
func StartServer(s *server.Server) {
	app.Wg.Add(1)
	serverWg.Add(1)

	go func() {
		defer func() {
			app.Wg.Done()
			serverWg.Done()

			// Remove s from the list of servers
			app.ServersMutex.Lock()
			defer app.ServersMutex.Unlock()
			for i, srv := range app.Servers {
				if srv == s {
					app.Servers = append(app.Servers[:i], app.Servers[i+1:]...)
					break
				}
			}
		}()

		// Start the server; blocks until stopped completely
		err := s.Start()

		// Report the error, maybe
		if !server.IsIgnorableError(err) {
			log.Println(err)
		}
	}()
}

// StopAllServers stops all servers gracefully.
// This function is non-blocking.
//
// This function is NOT safe for concurrent use.
func StopAllServers() {
	for _, serv := range app.Servers {
		serv.Stop(app.ShutdownCutoff)
	}
	serverWg.Wait() // wait for servers to shut down
}
