package admin

import (
	"errors"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config/parse"
	"github.com/mholt/caddy/config/setup"
)

func init() {
	router.POST("/:addr/tls", auth(tlsEnable))    // turns on or changes TLS with caddyfile line using POST body
	router.DELETE("/:addr/tls", auth(tlsDisable)) // turns off TLS, if possible
}

// tlsEnable enables TLS on a server, if possible. If TLS is already enabled, it
// updates the configuration. Expects 'tls' Caddyfile line in request body.
func tlsEnable(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	srv, vh, ok := serverAndVirtualHost(p.ByName("addr"))
	if !ok {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	if !srv.TLS && len(srv.Vhosts) > 1 {
		// Cannot enable TLS on just this vhost, because http and https can't be served from the same port
		// TODO: Query string param to provide new address in this case?
		handleError(w, r, http.StatusBadRequest, errors.New("other servers on same socket are not HTTPS"))
		return
	}

	// Backup in case rollback is needed
	// Subtle race condition: locking the config is required since its mutex is copied
	vh.Config.Lock()
	backup := *srv
	vh.Config.Unlock()

	// By this point, TLS is already enabled for the server or it's the only virtualhost, so it's okay to proceed.
	c := &setup.Controller{
		Config:    vh.Config,
		Dispenser: parse.NewDispenser("HTTP_POST", r.Body),
	}

	vh.Config.Lock()
	_, err := setup.TLS(c)
	vh.Config.Unlock()

	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	go func() {
		srv.Lock() // TODO: I hate this lock - can we get rid of it, plz?
		stopchan := srv.StopChan()
		srv.Stop(app.ShutdownCutoff)
		srv.Unlock()
		<-stopchan

		// Mark the server as an HTTPS server
		srv.Lock()
		srv.TLS = true
		srv.Unlock()

		// Save and start start the server, then do health check for potential rollback
		app.ServersMutex.Lock()
		app.Servers = append(app.Servers, srv)
		StartServer(srv)
		app.ServersMutex.Unlock()
		go healthCheckRollback(srv, &backup)
	}()

	w.WriteHeader(http.StatusAccepted)
}

func tlsDisable(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	// TODO
}
