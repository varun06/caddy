package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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
	if err := json.NewEncoder(w).Encode(app.Servers); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
	}
}

// serverInfo returns information about a specific server/virtualhost
func serverInfo(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	vh := virtualHost(p.ByName("addr"))
	if vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}
	if err := json.NewEncoder(w).Encode(vh.Config); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
	}
}

// serverCreate spins up a new server (or virtualhost)
func serverCreate(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	// Parse the configuration
	configHead := strings.NewReader(p.ByName("addr") + "\n")
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
	for addr, cfgs := range bindings {
		// Create a server that will build the virtual host
		s, err := server.New(addr.String(), cfgs, cfgs[0].TLS.Enabled)
		if err != nil {
			handleError(w, r, http.StatusBadRequest, err)
			return
		}

		// See if there's a server that is already listening at the address
		var addressUsed bool
		for _, existingServer := range app.Servers {
			if addr.String() == existingServer.Address {
				addressUsed = true

				// Okay, now the virtual host address must not exist already
				if _, vhostExists := existingServer.Vhosts[cfgs[0].Host]; vhostExists {
					handleError(w, r, http.StatusBadRequest, errors.New("Server already listening at "+p.ByName("addr")))
					return
				}

				vh := s.Vhosts[cfgs[0].Host]
				vh.Start()
				existingServer.Vhosts[cfgs[0].Host] = vh
				break
			}
		}

		if !addressUsed {
			app.Servers = append(app.Servers, s)
			app.Wg.Add(1)
			go func() {
				defer app.Wg.Done()
				s.Start() // TODO: Error handling here? Maybe use a channel?
				// TODO - Note that main.go does something similar but has the luxury
				// of shutting down the whole server because it's during startup.
				// What do we do in the case of errors here?
			}()
		}
	}

	w.WriteHeader(http.StatusCreated)
	// TODO: Response body?
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
		srv.Stop(app.ShutdownCutoff)
	}

	// TODO: write a proper response
	w.WriteHeader(http.StatusOK)
}
