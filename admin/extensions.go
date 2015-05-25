package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/middleware/extensions"
)

func init() {
	router.GET("/:addr/ext/extensions", auth(extensionsGet))
	router.POST("/:addr/ext/extensions", auth(extensionsSet))
	router.PUT("/:addr/ext/extensions/:ext", auth(extensionsAdd))
	router.DELETE("/:addr/ext/extensions/:ext", auth(extensionsDel))
}

func extensionsGet(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	e := getExt(w, r, p)
	if e == nil {
		return
	}
	fmt.Fprintf(w, "%v", e.Extensions)
}

func extensionsSet(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	e := getExt(w, r, p)
	if e == nil {
		return
	}

	var extList []string
	err := json.NewDecoder(r.Body).Decode(&extList)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	app.ServersMutex.Lock()
	e.Extensions = extList
	app.ServersMutex.Unlock()

	// TODO - Json response?
	w.WriteHeader(http.StatusOK)
}

func extensionsAdd(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	e := getExt(w, r, p)
	if e == nil {
		return
	}
	app.ServersMutex.Lock()
	e.Extensions = append(e.Extensions, p.ByName("ext"))
	app.ServersMutex.Unlock()
}

func extensionsDel(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	e := getExt(w, r, p)
	if e == nil {
		return
	}
	extDel := p.ByName("ext")
	for i, extension := range e.Extensions {
		if extension == extDel {
			app.ServersMutex.Lock()
			e.Extensions = append(e.Extensions[:i], e.Extensions[i+1:]...)
			app.ServersMutex.Unlock()
		}
	}
}

// getExt gets the extensions middleware asked for by the request.
// This function handles errors if they occur, in which case return value is nil.
func getExt(w http.ResponseWriter, r *http.Request, p httprouter.Params) *extensions.Ext {
	vh := virtualHost(p.ByName("addr"))
	if vh == nil {
		handleError(w, r, http.StatusNotFound, nil)
		return nil
	}
	ext, ok := vh.Config.HandlerMap["ext"].(*extensions.Ext)
	if !ok {
		handleError(w, r, http.StatusInternalServerError, errors.New("Not ext middleware"))
		return nil
	}
	return ext
}
