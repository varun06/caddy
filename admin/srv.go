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
	router.GET("/:addr", auth(srvInfo))
	router.POST("/:addr", auth(srvCreate))
	router.DELETE("/:addr", auth(srvStop))
}

func srvInfo(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	vh := virtualHost(p.ByName("addr"))
	if vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}
	if err := json.NewEncoder(w).Encode(vh.Config); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
	}
}

func srvCreate(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
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
		s, err := server.New(addr, cfgs, cfgs[0].TLS.Enabled)
		if err != nil {
			handleError(w, r, http.StatusBadRequest, err)
			return
		}

		// See if there's a server that is already listening at the address
		var addressUsed bool
		for _, existingServer := range app.Servers {
			if addr == existingServer.Address {
				addressUsed = true

				// Okay, now the virtual host address must not exist already
				if _, vhostExists := existingServer.Vhosts[cfgs[0].Host]; vhostExists {
					handleError(w, r, http.StatusBadRequest, errors.New("Server already listening at "+p.ByName("addr")))
					return
				}

				// TODO: Run startup functions and do other startup tasks...
				existingServer.Vhosts[cfgs[0].Host] = s.Vhosts[cfgs[0].Host]
				break
			}
		}

		if !addressUsed {
			app.Servers = append(app.Servers, s)
			app.Wg.Add(1)
			go func() {
				s.Start()
				// TODO: Error handling here? Maybe use a channel?
			}()
		}
	}

	w.WriteHeader(http.StatusOK)
	// TODO: Response body?
}

func srvStop(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	// TODO
}
