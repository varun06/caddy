// This file comes from the excellent package by tylerb:
//   https://github.com/tylerb/graceful
//
// It has been modified for use with this program. The original
// code is MIT-licensed, as follows:
//
// The MIT License (MIT)
//
// Copyright (c) 2014 Tyler Bunnell
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package server

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"
)

type Graceful struct {
	*http.Server

	// Timeout is the duration to allow outstanding requests to survive
	// before forcefully terminating them.
	Timeout time.Duration

	// ConnState specifies an optional callback function that is
	// called when a client connection changes state. This is a proxy
	// to the underlying http.Server's ConnState, and the original
	// must not be set directly.
	ConnState func(net.Conn, http.ConnState)

	// ShutdownCallback is an optional callback function that is called
	// when shutdown is initiated. It can be used to notify the client
	// side of long lived connections (e.g. websockets) to reconnect.
	ShutdownCallback func()

	// interrupt signals the listener to stop serving connections,
	// and the server to shut down.
	interrupt chan os.Signal

	// stopChan is the channel on which callers may block while waiting for
	// the server to stop.
	stopChan chan struct{}

	// connections holds all connections managed by graceful
	connections map[net.Conn]struct{}

	// shutdownInitiated will be set to true once the shutdown process has begun.
	shutdownInitiated bool

	// This struct must be locked when modifying stopChan and interrupt fields.
	sync.Mutex
}

// NewGraceful creates a server at addr with graceful shutdown capabilities.
func NewGraceful(addr string, h http.Handler) *Graceful {
	return &Graceful{
		Server: &http.Server{Addr: addr, Handler: h},
	}
}

// ListenAndServe is equivalent to http.Server.ListenAndServe with graceful shutdown enabled.
func (g *Graceful) ListenAndServe() error {
	addr := g.Addr
	if addr == "" {
		addr = ":http"
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return g.Serve(tcpKeepAliveListener{listener.(*net.TCPListener)})
}

// ListenAndServeTLS is equivalent to http.Server.ListenAndServeTLS with graceful shutdown enabled.
func (g *Graceful) ListenAndServeTLS(certFile, keyFile string) error {
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

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	tlsListener := tls.NewListener(tcpKeepAliveListener{listener.(*net.TCPListener)}, config)
	return g.Serve(tlsListener)
}

// Serve is equivalent to http.Server.Serve with graceful shutdown enabled.
func (g *Graceful) Serve(listener net.Listener) error {
	// Track connection state
	add := make(chan net.Conn)
	remove := make(chan net.Conn)

	g.Server.ConnState = func(conn net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			add <- conn
		case http.StateClosed, http.StateHijacked:
			remove <- conn
		}
		if g.ConnState != nil {
			g.ConnState(conn, state)
		}
	}

	// Manage open connections
	shutdown := make(chan chan struct{})
	kill := make(chan struct{})
	go g.manageConnections(add, remove, shutdown, kill)

	// Handles the interrupt that triggers a shutdown
	go g.handleInterrupt(g.interruptChan(), listener)

	// Execution blocks here until listener is closed
	err := g.Server.Serve(listener)

	// Shuts down gracefully
	g.shutdown(shutdown, kill)

	return err
}

// Stop instructs the type to halt operations and close
// the stop channel when it is finished.
//
// timeout is grace period for which to wait before shutting
// down the server. The timeout value passed here will override the
// timeout given when constructing the server, as this is an explicit
// command to stop the server.
func (g *Graceful) Stop(timeout time.Duration) {
	g.Lock()
	g.Timeout = timeout
	g.Unlock()
	interrupt := g.interruptChan()
	interrupt <- syscall.SIGINT
}

// StopChan gets the stop channel which will block until
// stopping has completed, at which point it is closed.
// Callers should never close the stop channel.
func (g *Graceful) StopChan() <-chan struct{} {
	g.Lock()
	if g.stopChan == nil {
		g.stopChan = make(chan struct{})
	}
	g.Unlock()
	return g.stopChan
}

func (g *Graceful) manageConnections(add, remove chan net.Conn, shutdown chan chan struct{}, kill chan struct{}) {
	var done chan struct{}
	g.connections = make(map[net.Conn]struct{})
	for {
		select {
		case conn := <-add:
			g.connections[conn] = struct{}{}
		case conn := <-remove:
			delete(g.connections, conn)
			if done != nil && len(g.connections) == 0 {
				done <- struct{}{}
				return
			}
		case done = <-shutdown:
			if len(g.connections) == 0 {
				done <- struct{}{}
				return
			}
		case <-kill:
			for k := range g.connections {
				_ = k.Close() // nothing to do here if it errors
			}
			return
		}
	}
}

func (g *Graceful) interruptChan() chan os.Signal {
	g.Lock()
	if g.interrupt == nil {
		g.interrupt = make(chan os.Signal, 1)
	}
	g.Unlock()
	return g.interrupt
}

func (g *Graceful) handleInterrupt(interrupt chan os.Signal, listener net.Listener) {
	<-interrupt
	g.SetKeepAlivesEnabled(false)
	if g.ShutdownCallback != nil {
		g.ShutdownCallback() // do this before closing the listener to avoid race condition
	}
	listener.Close() // we are shutting down anyway. ignore error.
}

func (g *Graceful) shutdown(shutdown chan chan struct{}, kill chan struct{}) {
	g.Lock()
	defer g.Unlock()

	// Request done notification
	done := make(chan struct{})
	shutdown <- done

	if g.Timeout > 0 {
		select {
		case <-done:
		case <-time.After(g.Timeout):
			close(kill)
		}
	} else {
		<-done
	}

	// Close the stopChan to wake up any blocked goroutines.
	if g.stopChan != nil {
		close(g.stopChan)
	}
}

// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted
// connections. It's used by ListenAndServe and ListenAndServeTLS so
// dead TCP connections (e.g. closing laptop mid-download) eventually
// go away.
//
// This type was adapted from the Go standard library (server.go),
// which is by the Go Authors.
//
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
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
