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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	killTime    = 500 * time.Millisecond
	timeoutTime = 1000 * time.Millisecond
	waitTime    = 100 * time.Millisecond
)

func runQuery(t *testing.T, expected int, shouldErr bool, wg *sync.WaitGroup, once *sync.Once) {
	wg.Add(1)
	defer wg.Done()
	client := http.Client{}
	r, err := client.Get("http://127.0.0.1:9590")
	if shouldErr && err == nil {
		once.Do(func() {
			t.Fatal("Expected an error but none was encountered.")
		})
	} else if shouldErr && err != nil {
		if checkErr(t, err, once) {
			return
		}
	}
	if r != nil && r.StatusCode != expected {
		once.Do(func() {
			t.Fatalf("Incorrect status code on response. Expected %d. Got %d", expected, r.StatusCode)
		})
	} else if r == nil {
		once.Do(func() {
			t.Fatal("No response when a response was expected.")
		})
	}
}

func checkErr(t *testing.T, err error, once *sync.Once) bool {
	if err.(*url.Error).Err == io.EOF {
		return true
	}
	errno := err.(*url.Error).Err.(*net.OpError).Err.(syscall.Errno)
	if errno == syscall.ECONNREFUSED {
		return true
	} else if err != nil {
		once.Do(func() {
			t.Fatal("Error on Get:", err)
		})
	}
	return false
}

func createListener(sleep time.Duration) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		time.Sleep(sleep)
		rw.WriteHeader(http.StatusOK)
	})

	server := &http.Server{Addr: ":9590", Handler: mux}
	l, err := net.Listen("tcp", ":9590")
	if err != nil {
		fmt.Println(err)
	}
	return server, l, err
}

func runServer(timeout, sleep time.Duration, c chan os.Signal) error {
	server, l, err := createListener(sleep)
	if err != nil {
		return err
	}

	g := &Graceful{Timeout: timeout, Server: server, interrupt: c}
	return g.Serve(l)
}

func launchTestQueries(t *testing.T, wg *sync.WaitGroup, c chan os.Signal) {
	var once sync.Once
	for i := 0; i < 8; i++ {
		go runQuery(t, http.StatusOK, false, wg, &once)
	}

	time.Sleep(waitTime)
	c <- os.Interrupt
	time.Sleep(waitTime)

	for i := 0; i < 8; i++ {
		go runQuery(t, 0, true, wg, &once)
	}

	wg.Done()
}

func TestGracefulRun(t *testing.T) {
	c := make(chan os.Signal, 1)

	var wg sync.WaitGroup
	wg.Add(1) // Counts runServer

	go func() {
		runServer(killTime, killTime/2, c)
		wg.Done()
	}()

	wg.Add(1) // counts launchTestQueries
	go launchTestQueries(t, &wg, c)
	wg.Wait()
}

func TestGracefulRunTimesOut(t *testing.T) {
	c := make(chan os.Signal, 1)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		runServer(killTime, killTime*10, c)
		wg.Done()
	}()

	var once sync.Once
	wg.Add(1)
	go func() {
		for i := 0; i < 8; i++ {
			go runQuery(t, 0, true, &wg, &once)
		}
		time.Sleep(waitTime)
		c <- os.Interrupt
		time.Sleep(waitTime)
		for i := 0; i < 8; i++ {
			go runQuery(t, 0, true, &wg, &once)
		}
		wg.Done()
	}()

	wg.Wait()

}

func TestGracefulRunDoesntTimeOut(t *testing.T) {
	c := make(chan os.Signal, 1)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		runServer(0, killTime*2, c)
		wg.Done()
	}()

	wg.Add(1)
	go launchTestQueries(t, &wg, c)
	wg.Wait()
}

func TestGracefulRunNoRequests(t *testing.T) {
	c := make(chan os.Signal, 1)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		runServer(0, killTime*2, c)
		wg.Done()
	}()

	c <- os.Interrupt

	wg.Wait()

}

func TestGracefulForwardsConnState(t *testing.T) {
	c := make(chan os.Signal, 1)
	states := make(map[http.ConnState]int)
	var stateLock sync.Mutex

	connState := func(conn net.Conn, state http.ConnState) {
		stateLock.Lock()
		states[state]++
		stateLock.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(1)

	expected := map[http.ConnState]int{
		http.StateNew:    8,
		http.StateActive: 8,
		http.StateClosed: 8,
	}

	go func() {
		server, l, _ := createListener(killTime / 2)
		g := &Graceful{
			ConnState: connState,
			Timeout:   killTime,
			Server:    server,
			interrupt: c,
		}
		g.Serve(l)

		wg.Done()
	}()

	wg.Add(1)
	go launchTestQueries(t, &wg, c)
	wg.Wait()

	stateLock.Lock()
	if !reflect.DeepEqual(states, expected) {
		t.Errorf("Incorrect connection state tracking.\n  actual: %v\nexpected: %v\n", states, expected)
	}
	stateLock.Unlock()
}

func TestGracefulExplicitStop(t *testing.T) {
	server, l, err := createListener(1 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	g := &Graceful{Timeout: killTime, Server: server}

	go func() {
		go g.Serve(l)
		time.Sleep(waitTime)
		g.Stop(killTime)
	}()

	// block on the stopChan until the server has shut down
	select {
	case <-g.StopChan():
	case <-time.After(timeoutTime):
		t.Fatal("Timed out while waiting for explicit stop to complete")
	}
}

func TestGracefulExplicitStopOverride(t *testing.T) {
	server, l, err := createListener(1 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	g := &Graceful{Timeout: killTime, Server: server}

	go func() {
		go g.Serve(l)
		time.Sleep(waitTime)
		g.Stop(killTime / 2)
	}()

	// block on the stopChan until the server has shut down
	select {
	case <-g.StopChan():
	case <-time.After(killTime):
		t.Fatal("Timed out while waiting for explicit stop to complete")
	}
}

func TestShutdownCallback(t *testing.T) {
	server, l, err := createListener(1 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	called := make(chan struct{})
	cb := func() { close(called) }

	g := &Graceful{Server: server, ShutdownCallback: cb}

	go func() {
		go g.Serve(l)
		time.Sleep(waitTime)
		g.Stop(killTime)
	}()

	select {
	case <-called:
	case <-time.After(killTime):
		t.Fatal("Timed out while waiting for ShutdownCallback callback to be called")
	}
}

func hijackingListener(g *Graceful) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		conn, bufrw, err := rw.(http.Hijacker).Hijack()
		if err != nil {
			http.Error(rw, "webserver doesn't support hijacking", http.StatusInternalServerError)
			return
		}

		defer conn.Close()

		bufrw.WriteString("HTTP/1.1 200 OK\r\n\r\n")
		bufrw.Flush()
	})

	server := &http.Server{Addr: ":9590", Handler: mux}
	l, err := net.Listen("tcp", ":9590")
	return server, l, err
}

func TestNotifyClosed(t *testing.T) {
	c := make(chan os.Signal, 1)

	var wg sync.WaitGroup
	wg.Add(1)

	g := &Graceful{Timeout: killTime, interrupt: c}
	server, l, err := hijackingListener(g)
	if err != nil {
		t.Fatal(err)
	}

	g.Server = server

	go func() {
		g.Serve(l)
		wg.Done()
	}()

	var once sync.Once
	for i := 0; i < 8; i++ {
		runQuery(t, http.StatusOK, false, &wg, &once)
	}

	g.Stop(0)

	// block on the stopChan until the server has shut down
	select {
	case <-g.StopChan():
	case <-time.After(timeoutTime):
		t.Fatal("Timed out while waiting for explicit stop to complete")
	}

	if len(g.connections) > 0 {
		t.Fatal("hijacked connections should not be managed")
	}

}

func TestStopDeadlock(t *testing.T) {
	c := make(chan struct{})

	server, l, err := createListener(1 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	g := &Graceful{Server: server}

	go func() {
		time.Sleep(waitTime)
		g.Serve(l)
	}()

	go func() {
		g.Stop(0)
		close(c)
	}()

	select {
	case <-c:
	case <-time.After(timeoutTime):
		t.Fatal("Timed out while waiting for explicit stop to complete")
	}
}
