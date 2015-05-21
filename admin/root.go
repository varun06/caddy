package admin

import (
	"fmt"
	"log"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
)

func init() {
	router.GET("/:addr/root", auth(rootGet))
	router.POST("/:addr/root/:root", auth(rootSet))
}

func rootGet(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	vh := virtualHost(p.ByName("addr"))
	if vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}
	fmt.Fprintf(w, "%s", vh.Config.Root)
}

func rootSet(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	vh := virtualHost(p.ByName("addr"))
	if vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	app.ServersMutex.Lock()
	vh.Config.Root = p.ByName("root")

	// Middleware stack must be rebuilt after any change to server config,
	// so the middlewares get the latest information
	err := vh.BuildStack()
	app.ServersMutex.Unlock()
	if err != nil {
		log.Fatal(err)
	}
}
