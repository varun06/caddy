package server

import (
	"log"
	"net/http"

	"github.com/mholt/caddy/middleware"
)

// VirtualHost represents a virtual host/server. While a Server
// is what actually binds to the address, a user may want to serve
// multiple sites on a single address, and this is what a
// virtualHost allows us to do.
type VirtualHost struct {
	Config     Config
	FileServer middleware.Handler
	Stack      middleware.Handler
}

// BuildStack builds the server's middleware stack based
// on its config.
func (vh *VirtualHost) BuildStack() error {
	vh.FileServer = FileServer(http.Dir(vh.Config.Root), []string{vh.Config.ConfigFile})

	// TODO: We only compile middleware for the "/" scope.
	// Partial support for multiple location contexts already
	// exists at the parser and config levels, but until full
	// support is implemented, this is all we do right here.
	vh.compile(vh.Config.Middleware["/"])

	return nil
}

// compile is an elegant alternative to nesting middleware function
// calls like handler1(handler2(handler3(finalHandler))).
func (vh *VirtualHost) compile(layers []*middleware.Middleware) {
	vh.Stack = vh.FileServer // core app layer
	for i := len(layers) - 1; i >= 0; i-- {
		vh.Stack = (*layers[i])(vh.Stack)
		dir, ok := vh.Config.MiddlewareMap[layers[i]]
		if !ok {
			// TODO
			log.Fatal("No middleware pointer")
		}
		vh.Config.HandlerMap[dir] = vh.Stack
	}
}

// Start means the vh is starting to be used, so it makes preparations
// like running startup functions or anything else it needs to do.
func (vh *VirtualHost) Start() error {
	// Execute startup functions
	for _, start := range vh.Config.Startup {
		err := start()
		if err != nil {
			return err
		}
	}
	return nil
}

// Stop means that vh is being terminated, so the vh cleans up after itself.
func (vh *VirtualHost) Stop() {
	// Execute shutdown functions
	for _, shutdownFunc := range vh.Config.Shutdown {
		err := shutdownFunc()
		if err != nil {
			log.Println(err)
		}
	}
}
