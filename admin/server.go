package admin

import (
	"errors"
	"log"
	"net"
	"net/http"
	"time"

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
// string and the input configures a server with the same host:port as an existing
// server. In that case, only the overlapping server is (gracefully) shut down
// and will be restarted with the new configuration.
//
// TODO: Much of this code is what main.go could/should be doing anyway. Maybe
// we could move this out of this package and into 'app' so both could share it.
func serversCreate(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	r.ParseForm()
	replace := r.Form.Get("replace") == "true"

	// Parse the configuration
	configs, err := config.Load("HTTP_POST", r.Body)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	// Arrange them by bind address (resolve hostname)
	bindings, err := config.ArrangeBindings(configs)
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	errChan := make(chan error)
	errTimeout := 2 * time.Second

	// For every listener address, we need to iterate its
	// configs, since each one represents a virtualhost start.
	for addr, configs := range bindings {
		// Create a server that will build the virtual host
		s, err := server.New(addr.String(), configs, configs[0].TLS.Enabled)
		if err != nil {
			handleError(w, r, http.StatusBadRequest, err)
			return
		}
		s.HTTP2 = app.Http2 // TODO: This setting is temporary

		// See if there's a server that is already listening at the address
		var hasListener bool
		for _, existingServer := range app.Servers {
			if addr.String() == existingServer.Address {
				hasListener = true

				// Okay, now the virtual host address must not exist already, or it must be replaced
				if _, vhostExists := existingServer.Vhosts[configs[0].Host]; vhostExists {
					if !replace {
						handleError(w, r, http.StatusBadRequest, errors.New("Server already listening at "+addr.String()))
						return
					}
					delete(existingServer.Vhosts, configs[0].Host)
				}

				vh := s.Vhosts[configs[0].Host]
				existingServer.Vhosts[configs[0].Host] = vh
				vh.Start()
				break
			}
		}

		if !hasListener {
			// Initiate the new server that will operate the listener for this virtualhost
			app.Servers = append(app.Servers, s)
			app.Wg.Add(1)

			go func() {
				defer app.Wg.Done()
				start := time.Now()

				// Start the server
				err := s.Start()

				// Report the error if it wasn't the usual 'error' on shutdown
				if opErr, ok := err.(*net.OpError); !ok || (ok && opErr.Op != "accept") {
					if time.Since(start) < errTimeout {
						errChan <- err // respond with error to client immediately
					} else {
						log.Println(err) // client is probably long gone by now, so... log it
					}
				}
			}()
		}
	}

	// Hang the request for just a moment to see if startup succeeded;
	// this way we can return a 201 Created instead of 202 Accepted
	// if all goes well.
	select {
	case err := <-errChan:
		handleError(w, r, http.StatusBadRequest, err)
		return
	case <-time.After(errTimeout):
	}

	w.WriteHeader(http.StatusCreated)
}

// serversReplace gracefully shuts down all listening servers and starts up
// new ones based on the contents of the configuration that is found in the
// response body.
func serversReplace(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	// Delete all existing servers
	app.ServersMutex.Lock()
	for _, serv := range app.Servers {
		serv.Stop(app.ShutdownCutoff)
	}
	app.Servers = []*server.Server{}
	app.ServersMutex.Unlock()

	// TODO: Check to make sure new config works!

	// Create new ones
	serversCreate(w, r, p)
}

// serverInfo returns information about a specific server/virtualhost
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
