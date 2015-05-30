package server

import (
	"crypto/tls"
	"fmt"
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

// NewGraceful makes a new Graceful server.
func NewGraceful(addr string) *Graceful {
	return &Graceful{
		Server: &http.Server{Addr: addr},
		stop:   make(chan bool),
		conns:  make(map[string]net.Conn),
	}
}

// Serve starts a server that can shutdown gracefully.
func (g *Graceful) Serve(listener net.Listener) error {
	// This goroutine waits for a stop signal
	go func() {
		g.stop <- true // blocks until s.Stop() is called
		close(g.stop)
		g.SetKeepAlivesEnabled(false)
		listener.Close()
	}()

	g.ConnState = func(conn net.Conn, state http.ConnState) {
		fmt.Println(conn, state)
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
				fmt.Println("Closing")
				conn.Close()
			default:
				fmt.Println("Saving conn")
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
// It blocks until g is stopped. It is directly adapted from net/http.
func (g *Graceful) ListenAndServeTLS(handler http.Handler, certFile, keyFile string) error {
	g.Server.Handler = handler

	addr := g.Addr
	if addr == "" {
		addr = ":https"
	}
	config := &tls.Config{}
	if g.TLSConfig != nil {
		*config = *g.TLSConfig
	}
	if config.NextProtos == nil {
		config.NextProtos = []string{"http/1.1"}
	}

	var err error
	config.Certificates = make([]tls.Certificate, 1)
	config.Certificates[0], err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	// TODO... ??
	//tlsListener := tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, config)
	tlsListener := tls.NewListener(ln, config)
	return g.Serve(tlsListener)
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
