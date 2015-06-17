package admin

import (
	"bytes"
	"errors"
	"io/ioutil"
	"net/http"
	"sync"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
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

	app.ServersMutex.Lock()
	_, err := InitializeReadConfig("HTTP_POST", r.Body, replace)
	app.ServersMutex.Unlock()
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
// handler is not blocking (but it may take a moment to resolve hostnames.)
func serversReplace(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	if len(reqBody) == 0 {
		handleError(w, r, http.StatusBadRequest, errors.New("empty request body"))
		return
	}

	err = ReplaceAllServers("HTTP_POST", bytes.NewBuffer(reqBody))
	if err != nil {
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

	if len(srv.Vhosts) == 1 {
		// Graceful shutdown
		srv.Lock()
		srv.Stop(app.ShutdownCutoff)
		srv.Unlock()
	} else {
		// Stopping a whole server will automatically call Stop
		// on all its virtualhosts, but we only stop the server
		// if there are no more virtualhosts left on it. So
		// we must stop only this virtualhost in this case.
		vh.Stop()
		srv.Lock()
		delete(srv.Vhosts, host)
		srv.Unlock()
	}

	w.WriteHeader(http.StatusAccepted)
}
