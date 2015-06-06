package admin

import (
	"errors"
	"io"
	"log"
	"net/http"

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

// serverList shows the list of servers and their information.
func serverList(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	respondJSON(w, r, app.Servers, http.StatusOK)
}

// serversCreate creates servers using the contents of the request body. It does
// not shut down any server instances unless "replace=true" is found in the query
// string and the input specifies a server with the same host:port as an existing
// server. In that case, only the overlapping server is (gracefully) shut down
// and will be restarted with the new configuration.
func serversCreate(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	r.ParseForm()
	replace := r.Form.Get("replace") == "true"

	err := InitializeWithConfig("HTTP_POST", r.Body, replace)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// serversReplace gracefully shuts down all listening servers and starts up
// new ones based on the contents of the configuration that is found in the
// response body. If there are any errors, the configuration is rolled back
// so the downtime is minimal (inperceptably short). It is possible for the
// failover to fail, in which case that particular server will not launch.
func serversReplace(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	// Keep current configuration in case we need to roll back
	backup := app.Servers

	// Delete all existing servers
	for _, serv := range app.Servers {
		serv.Stop(app.ShutdownCutoff)
	}
	app.Servers = []*server.Server{}

	// Create and start new servers
	err := InitializeWithConfig("HTTP_POST", r.Body, false)
	if err != nil {
		// Oh no! Roll back.
		app.Servers = backup
		for _, serv := range app.Servers {
			app.Wg.Add(1)
			go func(serv *server.Server) {
				defer app.Wg.Done()
				serv.Start() // hopefully this works
			}(serv)
		}
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

// serverStop stops a running server (or virtualhost) with a graceful shutdown.
func serverStop(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	host, port := safeSplitHostPort(p.ByName("addr"))
	srv, vh := getServerAndVirtualHost(host, port)
	if srv == nil || vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	if len(srv.Vhosts) > 1 {
		// Stopping a whole server will automatically call Stop
		// on all its virtualhosts, but we only stop the server
		// if there are no more virtualhosts left on it. So
		// we must stop this virtualhost manually in that case.
		vh.Stop()
	}

	delete(srv.Vhosts, host)

	if len(srv.Vhosts) == 0 {
		// Graceful shutdown
		srv.Stop(app.ShutdownCutoff)

		// Remove it from the list of servers
		for i, s := range app.Servers {
			if s == srv {
				app.Servers = append(app.Servers[:i], app.Servers[i+1:]...)
				break
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

// InitializeWithConfig reads the configuration from body and starts new
// servers. If replace is true, a server that has the same host and port
// as a new one will be replaced with the new one, no questions asked.
// If replace is false, the same host:port will conflict and cause an
// error. It is NOT safe to call this concurrently. Use app.ServersMutex.
func InitializeWithConfig(configSource string, body io.Reader, replace bool) error {
	// Parse and load the configuration
	configs, err := config.Load(configSource, body)
	if err != nil {
		return err
	}

	// Arrange servers by bind address (resolves hostnames)
	bindings, err := config.ArrangeBindings(configs)
	if err != nil {
		return err
	}

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
						log.Println(err)
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
		app.Wg.Add(1)

		go func() {
			defer app.Wg.Done()

			// Start the server
			err := s.Start()

			// Report the error, maybe
			if !server.IsIgnorableError(err) {
				log.Println(err) // client is probably long gone by now, so... just log it
			}
		}()
	}

	return nil
}
