package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/config/parse"
	"github.com/mholt/caddy/config/setup"
	"github.com/mholt/caddy/server"
)

var router = httprouter.New()

// Serve starts the admin server. It blocks indefinitely.
func Serve(address string, tls server.TLSConfig) {
	if tls.Enabled {
		http.ListenAndServeTLS(address, tls.Certificate, tls.Key, router)
	} else {
		http.ListenAndServe(address, router)
	}
}

// auth is a middleware layer that authenticates a request to the server
// management API
func auth(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		// TODO: Authenticate
		w.Header().Set("X-Temp-Auth", "true")
		h(w, r, p)
	}
}

// handleError handles errors during API requests, including writing the response.
func handleError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if err != nil {
		log.Println(err)
	}

	type errInfo struct {
		Status  string `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message,omitempty"`
	}

	einfo := errInfo{
		Status: "error",
		Code:   status,
	}
	if status < 500 && err != nil {
		einfo.Message = err.Error()
	}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(einfo); err != nil {
		log.Println("Error handling error; could not marshal error info:", err)
		w.WriteHeader(status)
		fmt.Fprint(w, http.StatusText(status))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// respondJSON encodes data as JSON and writes it to w with the given status code.
// It handles any errors that may occur. handleError() should NOT call this function,
// since this function may call handleError().
func respondJSON(w http.ResponseWriter, r *http.Request, data interface{}, status int) {
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(data); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// virtualHost gets only the VirtualHost only of the address
// addr. If nil, the address was not found.
func virtualHost(addr string) *server.VirtualHost {
	_, vh, _ := serverAndVirtualHost(addr)
	return vh
}

// serverAndVirtualHost gets the server and VirtualHost of the
// address addr. If either value is nil, the address could not
// be found in the list of servers, in which case the last arg
// will be false.
func serverAndVirtualHost(addr string) (*server.Server, *server.VirtualHost, bool) {
	// The addr passed in may contain a host and port, but the
	// server only arranges virtualhosts by host, not both, so
	// we have to split these to make sure we got the right port.
	srv, vh := getServerAndVirtualHost(safeSplitHostPort(addr))
	return srv, vh, srv != nil && vh != nil
}

// serverAndVirtualHost gets the server and VirtualHost by the
// host and port information. If either value is nil, the host
// and port combination could not be found.
func getServerAndVirtualHost(host, port string) (*server.Server, *server.VirtualHost) {
	for _, s := range app.Servers {
		_, sPort, err := net.SplitHostPort(s.Address)
		if err != nil || sPort != port {
			continue
		}
		if vh, ok := s.Vhosts[host]; ok {
			return s, vh
		}
	}
	return nil, nil
}

// safeSplitHostPort splits the host and port. It assumes
// that if there is an error splitting, the entire string
// s must be the host, and we resort to the default port.
func safeSplitHostPort(s string) (string, string) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host = s
		port = config.DefaultPort
	}
	return host, port
}

// createMiddleware creates a new middleware using the input from the request r and the address
// found in p. The directive and setup function that are passed in will be used to create
// the middleware layer and chain it in at the proper place. It returns an HTTP status code
// and server-side error, if any. Note that the HTTP status code may still be an error code
// even though the error is nil. To make it easier to determine if there was an error, the third
// return value will be false if there was an error (true if all OK).
func createMiddleware(r *http.Request, p httprouter.Params, directive string, setupFunc config.SetupFunc) (int, error, bool) {
	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	// Get the virtualHost we will add the middleware to
	_, vh, ok := serverAndVirtualHost(p.ByName("addr"))
	if !ok {
		return http.StatusNotFound, nil, false
	}

	// Make sure the middleware doesn't already exist
	if _, ok := vh.Config.HandlerMap[directive]; ok {
		return http.StatusConflict, errors.New("Resource already exists"), false
	}

	// Get ready to parse the configuration
	c := &setup.Controller{
		Config:    &vh.Config,
		Dispenser: parse.NewDispenser("HTTP_POST", r.Body),
	}

	// Parse the input, which creates our middleware layer
	midware, err := setupFunc(c)
	if err != nil {
		return http.StatusBadRequest, err, false
	}

	vh.Config.MiddlewareMap[&midware] = directive

	// Chain our new middleware in at the right place
	_, handlerBefore := config.HandlerBefore(directive, vh.Config.HandlerMap)
	if handlerBefore == nil {
		// This is the first middleware handler!
		vh.Stack = midware(vh.Stack)
		vh.Config.HandlerMap[directive] = vh.Stack
	} else {
		// This is not the first middleware handler; splice it into the chain
		newNext := handlerBefore.GetNext()
		newHandler := midware(newNext)
		handlerBefore.SetNext(newHandler)
		vh.Config.HandlerMap[directive] = newHandler
	}

	return http.StatusOK, nil, true
}

// deleteMiddleware deletes a middleware from the chain/stack of the server
// indicated by the parameter in p, and the directive name that gets passed in.
// It returns a status code, server-side error (if any), and true if the action
// was succesful (false otherwise).
func deleteMiddleware(p httprouter.Params, directive string) (int, error, bool) {
	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	// Get the virtualHost we will remove the middleware from
	_, vh, ok := serverAndVirtualHost(p.ByName("addr"))
	if !ok {
		return http.StatusNotFound, nil, false
	}

	// Get the handler being deleted
	handler, ok := vh.Config.HandlerMap[directive]
	if !ok {
		return http.StatusNotFound, nil, false
	}

	// Get the handler before it so we can re-wire this part of the chain
	_, handlerBefore := config.HandlerBefore(directive, vh.Config.HandlerMap)
	next := handler.GetNext()

	if handlerBefore == nil {
		vh.Stack = next
	} else {
		handlerBefore.SetNext(next)
	}

	// Now that it's not in the chain anymore, delete all traces of this handler
	for key, dir := range vh.Config.MiddlewareMap {
		if dir == directive {
			delete(vh.Config.MiddlewareMap, key)
			break
		}
	}
	delete(vh.Config.HandlerMap, directive)

	return http.StatusOK, nil, true
}
