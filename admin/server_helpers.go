package admin

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/server"
)

// InitializeReadConfig reads the configuration from body and starts new
// servers. If replace is true, a server that has the same host and port
// as a new one will be replaced with the new one, no questions asked.
// If replace is false, the same host:port will conflict and cause an
// error. It returns a list of the servers that are to be started so that
// a health check may be performed. Note that this function may return
// the servers have started.
//
// This function is NOT safe for concurrent use. Use app.ServersMutex.
var InitializeReadConfig = initializeReadConfig

func initializeReadConfig(filename string, body io.Reader, replace bool) ([]*server.Server, error) {
	// Parse and load all configurations
	configs, err := config.Load(filename, body)
	if err != nil {
		return nil, err
	}

	// Arrange servers by bind address (resolves hostnames)
	bindings, err := config.ArrangeBindings(configs)
	if err != nil {
		return nil, err
	}

	return InitializeWithBindings(bindings, replace)
}

// InitializeWithBindings is like InitializeReadConfig except that it
// sets up servers using pre-made Config structs organized by net
// address, rather than reading and parsing the config from scratch.
// Call config.ArrangeBindings to get the proper 'bindings' input.
// It returns the list of servers that were created, but remember that
// this function may return before the servers are finished starting.
// You may use the list of new servers to perform a health check.
//
// This function is NOT safe for concurrent use. Use app.ServersMutex.
var InitializeWithBindings = initializeWithBindings

func initializeWithBindings(bindings map[*net.TCPAddr][]*server.Config, replace bool) ([]*server.Server, error) {
	// If replacing is not allowed, make sure each virtualhost is unique
	// BEFORE we start the servers, so we don't end up with a partially
	// fulfilled request.
	if !replace {
		for addr, configs := range bindings {
			for _, existingServer := range app.Servers {
				for _, cfg := range configs {
					if _, vhostExists := existingServer.Vhosts[cfg.Host]; vhostExists {
						return nil, errors.New(cfg.Host + " already listening at " + addr.String())
					}
				}
			}
		}
	}

	var newServers []*server.Server

	// For every listener address, we need to iterate its configs/virtualhosts.
	for addr, configs := range bindings {
		// Create a server that will build the virtual host
		s, err := server.New(addr.String(), configs, configs[0].TLS.Enabled)
		if err != nil {
			return nil, err
		}
		s.HTTP2 = app.Http2 // TODO: This setting is temporary

		// See if there's a server that is already listening at the address
		// If so, we just add the virtualhost to that server.
		var hasListener bool
		for _, existingServer := range app.Servers {
			if addr.String() == existingServer.Address {
				hasListener = true
				for _, cfg := range configs {
					vh := s.Vhosts[cfg.Host]
					existingServer.Vhosts[cfg.Host] = vh
					err := vh.Start()
					if err != nil {
						return newServers, err
					}
				}
				break
			}
		}

		if hasListener {
			continue
		}

		// Initiate the new server that will operate the listener for this virtualhost
		app.Servers, newServers = append(app.Servers, s), append(newServers, s)
		StartServer(s)
	}

	return newServers, nil
}

// StartServer starts s correctly. This function is non-blocking
// but will cause anything waiting on app.Wg (or serverWg) to
// block until s is terminated. It does NOT add s to the
// app.Servers slice, but it WILL delete s from the slice
// after the server shuts down (if it is in the slice).
//
// This function is safe for concurrent use.
var StartServer = startServer

func startServer(s *server.Server) {
	app.Wg.Add(1)
	serverWg.Add(1)

	go func() {
		defer func() {
			app.Wg.Done()
			serverWg.Done()

			// Remove s from the list of servers
			app.ServersMutex.Lock()
			defer app.ServersMutex.Unlock()
			for i, srv := range app.Servers {
				if srv == s {
					app.Servers = append(app.Servers[:i], app.Servers[i+1:]...)
					break
				}
			}
		}()

		// Start the server; blocks until stopped completely
		err := s.Start()

		// Report the error, maybe
		if !server.IsIgnorableError(err) {
			log.Println(err)
		}
	}()
}

// StartAllServers correctly starts all the servers in
// app.Servers. A call to this function is non-blocking.
//
// This function is NOT safe for concurrent use.
func StartAllServers() {
	for _, s := range app.Servers {
		StartServer(s)
	}
}

// StopAllServers stops all servers gracefully.
// This function is blocking, and it is
// NOT safe for concurrent use.
func StopAllServers() {
	for _, serv := range app.Servers {
		serv.Lock()
		serv.Stop(app.ShutdownCutoff)
		serv.Unlock()
	}
	serverWg.Wait() // wait for servers to shut down
}

// ReplaceAllServers gracefully shuts down all the servers and starts
// new ones using the configuration from input. This function is safe
// to use concurrently. It blocks long enough for the servers to shut
// down. If an error occurs before attempting to start the server, it
// will be returned.
var ReplaceAllServers = replaceAllServers

func replaceAllServers(inputName string, input io.Reader) error {
	// Keep current configuration in case we need to roll back
	app.ServersMutex.Lock()
	backup := app.Servers            // Back up all existing servers
	StopAllServers()                 // Stop them
	app.Servers = []*server.Server{} // Delete them
	app.ServersMutex.Unlock()

	// Create and start new servers
	servers, err := InitializeReadConfig(inputName, input, false)
	if err != nil {
		// No health check needed, since at least one server did not try to start.
		// Just roll back.
		rollback(backup)
		return err
	} else {
		// Health check required to verify successful socket bind
		healthCheckRollbackMulti(servers, backup)
	}

	return nil
}

// healthcheck performs a health check and returns
// an error if the health check failed. It is safe
// for concurrent use and is blocking. Usually this
// function is not called directly, it is usually
// used by one of the healthCheckRollback functions.
// It handles all the synchronization needed to
// make sure the health check does not occur before
// the server is ready for one, etc.
var healthcheck = performHealthCheck

func performHealthCheck(srv *server.Server) error {
	// Give socket a moment to bind
	time.Sleep(healthCheckDelay)

	// Health check
	addr := srv.Address
	if srv.TLS {
		addr = "https://" + addr
	} else {
		addr = "http://" + addr
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // who cares, for a health check
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get(addr)
	if err != nil {
		return err
	}
	if resp != nil {
		resp.Body.Close()
	}

	return nil
}

// healthCheckRollback performs a health check on srv synchronously. If the
// health check fails, it rolls back using backup. This function is safe to
// call concurrently, as long as the calling function has already released
// the app.ServersMutex lock. This function is blocking but may be called
// in a separate goroutine since no value is returned and it handles synchronization.
func healthCheckRollback(srv *server.Server, backup *server.Server) {
	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	// Health check
	err := healthcheck(srv)
	if err != nil {
		for _, vh := range srv.Vhosts {
			// These were started without knowing the socket wouldn't bind
			vh.Stop()
		}
		srv.Stop(0)

		// Roll back to last working configuration
		srv = backup
		app.Servers = append(app.Servers, srv)
		StartServer(srv)
	}

}

// healthcheckRollbackMulti performs a batch health check on servers, and if the checks
// fail, it rolls back the configuration to backup. This function is safe
// to use concurrently, but make sure app.ServersMutex is unlocked before calling!
// It expects the servers to be in the app.Servers slice and it may replace
// that slice if rollback is required. This function is non-blocking.
func healthCheckRollbackMulti(servers []*server.Server, backup []*server.Server) {
	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	var once sync.Once // so we don't roll back more than once

	for _, srv := range servers {
		go func(srv *server.Server) {
			// Health check
			err := healthcheck(srv)
			if err != nil {
				for _, vh := range srv.Vhosts {
					// These were started without knowing the socket wouldn't bind
					vh.Stop()
				}

				// Roll back to last working configuration
				once.Do(func() { rollback(backup) })
			}
		}(srv)
	}
}

// rollback stops all servers, replaces them with
// the ones in backup, and starts them. There is no
// error reporting. This function is safe to use
// concurrently and is non-blocking. It replaces
// all the servers in app.Servers with those in backup.
func rollback(backup []*server.Server) {
	log.Println("Rolling back to last good configuration")
	go func() {
		app.ServersMutex.Lock()
		defer app.ServersMutex.Unlock()
		StopAllServers()
		app.Servers = backup
		StartAllServers() // hopefully this works
	}()
}

// healthCheckDelay is how long to wait between the time a server starts
// listening and performing a health check.
const healthCheckDelay = 750 * time.Millisecond
