package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type Graceful struct {
	*http.Server
	stop  chan bool           // used to signal the server to shut down
	wg    sync.WaitGroup      // for waiting on client connections
	conns map[string]net.Conn // idle connections (keyed by local address)
	mu    sync.Mutex          // protects the map of idle connections
}

func NewGraceful(addr string) *Graceful {
	return &Graceful{
		Server: &http.Server{Addr: addr},
		stop:   make(chan bool),
		conns:  make(map[string]net.Conn),
	}
}

func (g *Graceful) Serve(listener net.Listener) error {
	// This goroutine waits for a stop signal
	go func() {
		g.stop <- true // blocks until s.Stop() is called
		close(g.stop)
		g.SetKeepAlivesEnabled(false)
		listener.Close()
	}()

	g.ConnState = func(conn net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			g.wg.Add(1)
		case http.StateActive:
			g.mu.Lock()
			delete(g.conns, conn.LocalAddr().String())
			g.mu.Unlock()
		case http.StateIdle:
			select {
			case <-g.stop:
				conn.Close()
			default:
				g.mu.Lock()
				g.conns[conn.LocalAddr().String()] = conn
				g.mu.Unlock()
			}
		case http.StateHijacked, http.StateClosed:
			g.wg.Done()
		}
	}

	g.Server.Serve(tcpKeepAliveListener{listener.(*net.TCPListener)})
	return nil
}

// Stop gracefully shuts down the server.
// This method is idempotent.
func (g *Graceful) Stop() {
	<-g.stop
}

// ListenAndServe creates a listener and starts serving.
// It blocks until g is stopped.
func (g *Graceful) ListenAndServe(handler http.Handler) error {
	g.Server.Handler = handler

	addr := g.Addr
	if addr == "" {
		addr = ":http"
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return g.Serve(listener)
}

// ListenAndServeTLS creates a TLS listener and starts serving.
// It blocks until g is stopped.
func (g *Graceful) ListenAndServeTLS(handler http.Handler) error {
	g.Server.Handler = handler

	addr := g.Addr
	if addr == "" {
		addr = ":http"
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return g.Serve(listener)
}

// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted
// connections. It's used by ListenAndServe and ListenAndServeTLS so
// dead TCP connections (e.g. closing laptop mid-download) eventually
// go away.
//
// This type was shamelessly borrowed from the Go standard library's
// net/http package, which is by the Go Authors.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}
