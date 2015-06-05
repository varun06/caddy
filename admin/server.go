package admin

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/server"
)

func init() {
	router.GET("/", auth(serverList))
	router.GET("/:addr", auth(serverInfo))
	router.POST("/:addr", auth(serverCreate))
	router.DELETE("/:addr", auth(serverStop))
}

// serverList shows the list of servers and their information
func serverList(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	respondJSON(w, r, app.Servers, http.StatusOK)
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

// serverCreate spins up a new server or virtualhost (or both, if needed)
func serverCreate(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	addr := p.ByName("addr")

	// Parse the configuration (after prepending the address)
	configHead := strings.NewReader(addr + "\n")
	configs, err := config.Load("HTTP_POST", io.MultiReader(configHead, r.Body))
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	// Arrange it by bind address (resolve hostname)
	bindings, err := config.ArrangeBindings(configs)
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	// There should only be one binding and one config,
	// but we don't know what the bind address is, so we
	// range over the map.
	for address, cfgs := range bindings {
		// Create a server that will build the virtual host
		s, err := server.New(address.String(), cfgs, cfgs[0].TLS.Enabled)
		if err != nil {
			handleError(w, r, http.StatusBadRequest, err)
			return
		}

		// See if there's a server that is already listening at the address
		var hasListener bool
		for _, existingServer := range app.Servers {
			if address.String() == existingServer.Address {
				hasListener = true

				// Okay, now the virtual host address must not exist already
				if _, vhostExists := existingServer.Vhosts[cfgs[0].Host]; vhostExists {
					handleError(w, r, http.StatusBadRequest, errors.New("Server already listening at "+addr))
					return
				}

				vh := s.Vhosts[cfgs[0].Host]
				existingServer.Vhosts[cfgs[0].Host] = vh
				vh.Start()
				break
			}
		}

		if !hasListener {
			// Initiate the new server that will operate the listener for this virtualhost
			app.Servers = append(app.Servers, s)
			app.Wg.Add(1)

			errChan := make(chan error)
			errTimeout := 1500 * time.Millisecond

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
						log.Println(err) // client is probably long gone by now, so... log I guess
					}
				}
			}()

			// Hang the request for just a moment to see if startup succeeded
			select {
			case err := <-errChan:
				handleError(w, r, http.StatusBadRequest, err)
				return
			case <-time.After(errTimeout):
			}
		}

		break // there must only be one server/virtualhost
	}

	w.WriteHeader(http.StatusCreated)
}

// serverStop stops a running server (or virtualhost) with a graceful shutdown.
func serverStop(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	host, port := safeSplitHostPort(p.ByName("addr"))
	srv, vh := getServerAndVirtualHost(host, port)
	if srv == nil || vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	vh.Stop()

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

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
