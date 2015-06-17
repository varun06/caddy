package admin

import (
	"errors"
	"net/http"
	"os"
	"path"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
)

func init() {
	// TODO: Figure out the routes
	// router.POST("/cmd/reload", auth(reload))
}

// reload reloads the server's configuration from the same config file the
// process started with. It blocks long enough to shut down the existing
// servers.
func reload(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	if app.ConfigPath == "" {
		handleError(w, r, http.StatusForbidden, errors.New("no config file to reload"))
	}

	file, err := os.Open(app.ConfigPath)
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}

	defer file.Close()
	err = ReplaceAllServers(path.Base(app.ConfigPath), file)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}
