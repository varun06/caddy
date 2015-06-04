package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/config/parse"
	"github.com/mholt/caddy/config/setup"
	"github.com/mholt/caddy/middleware/extensions"
)

func init() {
	router.GET("/:addr/ext", auth(extensionsGet))
	router.POST("/:addr/ext", auth(extensionsCreate))
	router.DELETE("/:addr/ext", auth(extensionsDelete))

	router.POST("/:addr/ext/extensions", auth(extensionsSet))
	router.PUT("/:addr/ext/extensions/:ext", auth(extensionsAdd))
	router.DELETE("/:addr/ext/extensions/:ext", auth(extensionsDel))
}

func extensionsGet(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	e := getExt(w, r, p)
	if e == nil {
		return
	}
	json.NewEncoder(w).Encode(e)
}

func extensionsCreate(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	_, vh, ok := serverAndVirtualHost(p.ByName("addr"))
	if !ok {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	if _, ok := vh.Config.HandlerMap["ext"]; ok {
		handleError(w, r, http.StatusConflict, errors.New("Resource already exists"))
		return
	}

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	c := &setup.Controller{
		Config:    &vh.Config,
		Dispenser: parse.NewDispenser("HTTP_POST", r.Body),
	}

	midware, err := setup.Ext(c)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}

	vh.Config.MiddlewareMap[&midware] = "ext"

	// TODO: Can we make this chain-in part a function that can be reused?
	// Chain in at the right place
	_, handlerBefore := config.HandlerBefore("ext", vh.Config.HandlerMap)
	if handlerBefore == nil {
		// First one! Okay.
		vh.Stack = midware(vh.Stack)
		vh.Config.HandlerMap["ext"] = vh.Stack
	} else {
		newNext := handlerBefore.GetNext()
		newHandler := midware(newNext)
		handlerBefore.SetNext(newHandler)
		vh.Config.HandlerMap["ext"] = newHandler
	}
	w.WriteHeader(http.StatusOK)
}

func extensionsDelete(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	_, vh, ok := serverAndVirtualHost(p.ByName("addr"))
	if !ok {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	if _, ok := vh.Config.HandlerMap["ext"]; !ok {
		handleError(w, r, http.StatusNotFound, nil)
		return
	}

	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	for key, dir := range vh.Config.MiddlewareMap {
		if dir == "ext" {
			delete(vh.Config.MiddlewareMap, key)
			break
		}
	}

	_, handlerBefore := config.HandlerBefore("ext", vh.Config.HandlerMap)
	newNext := vh.Config.HandlerMap["ext"].GetNext()

	if handlerBefore == nil {
		vh.Stack = newNext
	} else {
		handlerBefore.SetNext(newNext)
	}

	delete(vh.Config.HandlerMap, "ext")

	w.WriteHeader(http.StatusOK)
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

// extensionsDel deletes an extension
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
		// TODO: The middleware might just not exist on this vhost
		handleError(w, r, http.StatusInternalServerError, errors.New("Nil or not ext middleware"))
		return nil
	}
	return ext
}
