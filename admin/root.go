package admin

import (
	"errors"
	"log"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
)

func init() {
	router.PUT("/:addr/root", auth(rootSet))
}

// rootSet sets the new site root, with "root=<new root>" in the query string.
func rootSet(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	r.ParseForm()

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	vh := virtualHost(p.ByName("addr"))
	if vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	newRoot := r.Form.Get("root")
	if newRoot == "" {
		handleError(w, r, http.StatusBadRequest, errors.New("root cannot be empty"))
		return
	}

	vh.Config.Root = newRoot

	// We basically have to restart the whole virtualhost when something
	// as crucial as the root changes; several middlewares may have
	// copies of the root which need updating or services they need to
	// reconfigure.
	vh.Stop()
	err := vh.BuildStack()
	if err != nil {
		log.Println(err) // TODO
	}
	vh.Start()

	w.WriteHeader(http.StatusOK)
}
