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

// rootSet sets the new site root.
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

	// Middleware stack must be rebuilt after any change to server config,
	// so the middlewares get the latest information
	err := vh.BuildStack()
	if err != nil {
		log.Fatal(err)
	}

	w.WriteHeader(http.StatusOK)
}
