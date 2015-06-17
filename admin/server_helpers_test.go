package admin

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/server"
)

func TestInitializeReadConfig(t *testing.T) {
	defer func() { InitializeWithBindings = initializeWithBindings }()
	app.ServersMutex.Lock()
	defer app.ServersMutex.Unlock()

	// Happy path
	var initializeCalled bool
	caddyfile := `localhost:8520, 127.0.0.1:9932`
	InitializeWithBindings = func(bindings map[*net.TCPAddr][]*server.Config, replace bool) ([]*server.Server, error) {
		initializeCalled = true

		if expected, actual := 2, len(bindings); expected != actual {
			t.Errorf("Expected to be passed %d bindings, but got %d", expected, actual)
		}
		if !replace {
			t.Error("Expected replace to be true, not false")
		}
		return nil, nil
	}

	InitializeReadConfig("Testfile", strings.NewReader(caddyfile), true)

	if !initializeCalled {
		t.Error("Expected call to InitializeWithBindings but it didn't")
	}

	// Test with parse error
	caddyfile = `localhost:8520 {`
	_, err := InitializeReadConfig("Testfile", strings.NewReader(caddyfile), false)
	if err == nil {
		t.Error("Expected a parse error, but didn't get one")
	}

	// Test with binding arrange error
	caddyfile = `<not-resolvable>`
	_, err = InitializeReadConfig("Testfile", strings.NewReader(caddyfile), false)
	if err == nil {
		t.Error("Expected an error resolving the address, but didn't get one")
	}
}

func TestInitializeWithBindings(t *testing.T) {
	defer cleanUp()
	defer func() { StartServer = startServer }()

	var startServerCalled int
	StartServer = func(s *server.Server) {
		startServerCalled++
	}

	// Happy path
	bindings, err := config.ArrangeBindings([]*server.Config{
		{Host: "localhost", Port: "5933"},
		{Host: "localhost", Port: "3203"},
	})
	if err != nil {
		t.Fatal("Could not arrange bindings in preparation for test:", err)
	}
	newServers, err := InitializeWithBindings(bindings, false)
	if err != nil {
		t.Errorf("Expected no error on happy path, but got '%v'", err)
	}
	if expected, actual := 2, startServerCalled; expected != actual {
		t.Errorf("Expected StartServer to be called %d times, but called %d times", expected, actual)
	}
	if expected, actual := 2, len(newServers); expected != actual {
		t.Errorf("Expected %d new servers to be returned, but returned %d", expected, actual)
	}
	startServerCalled = 0

	// Path where a vhost gets added to an existing server
	bindings, err = config.ArrangeBindings([]*server.Config{
		{Host: "127.0.0.1", Port: "3203"},
	})
	if err != nil {
		t.Fatal("Could not arrange bindings in preparation for test:", err)
	}
	_, err = InitializeWithBindings(bindings, false)
	if err != nil {
		t.Errorf("Expected no error on happy path, but got '%v'", err)
	}
	if expected, actual := 0, startServerCalled; expected != actual {
		t.Errorf("Expected StartServer to be called %d times, but called %d times", expected, actual)
	}
	startServerCalled = 0

	// Path where replace check refuses to start a server that already exists
	bindings, err = config.ArrangeBindings([]*server.Config{
		{Host: "127.0.0.1", Port: "3203"},
	})
	if err != nil {
		t.Fatal("Could not arrange bindings in preparation for test:", err)
	}
	_, err = InitializeWithBindings(bindings, false)
	if err == nil {
		t.Errorf("Expected an error when trying to initialize a duplicate server without replace enabled")
	}
	if expected, actual := 0, startServerCalled; expected != actual {
		t.Errorf("Expected StartServer to be called %d times, but called %d times", expected, actual)
	}
	startServerCalled = 0

	// Check some details of the servers to make sure they were created properly
	if expected, actual := 2, len(app.Servers); expected != actual {
		t.Fatalf("Expected %d servers, got %d", expected, actual)
	}
	srv, _ := getServerAndVirtualHost("localhost", "5933")
	if expected, actual := 1, len(srv.Vhosts); expected != actual {
		t.Errorf("Expected %d virtualhost on %s, got %d:\n%#v",
			expected, srv.Address, actual, srv.Vhosts)
	}
	srv, _ = getServerAndVirtualHost("localhost", "3203")
	if expected, actual := 2, len(srv.Vhosts); expected != actual {
		t.Errorf("Expected %d virtualhosts on %s, got %d:\n%#v",
			expected, srv.Address, actual, srv.Vhosts)
	}

	// Make sure a server can be parsed properly and set up correctly (like with middleware)
	// (InitializeReadConfig should call the real InitializeWithBindings)
	cleanUp()
	caddyfile := "localhost:8520, 127.0.0.1:9932\ngzip"
	_, err = InitializeReadConfig("Testfile", strings.NewReader(caddyfile), false)
	if err != nil {
		t.Errorf("Expected no errors, but got '%v'", err)
	}

	// TODO: Rarely, but sometimes, the tests bomb here with a nil pointer dereference.
	// Race detector doesn't detect a race, though
	if app.Servers[0].Vhosts["localhost"].Config.HandlerMap["gzip"] == nil ||
		app.Servers[1].Vhosts["127.0.0.1"].Config.HandlerMap["gzip"] == nil {
		t.Error("Expected all the servers be properly configured with middleware, but they weren't")
	}
}

func TestStartServer(t *testing.T) {
	defer cleanUp()
	defer killServers()

	var testWg sync.WaitGroup
	var addresses = []string{
		"localhost:3723",
		"127.0.0.1:2388",
		":7732",
	}
	var caddyfile string
	for _, addr := range addresses {
		caddyfile += addr + " "
	}
	makeTestServers(t, caddyfile)

	for _, srv := range app.Servers {
		testWg.Add(1)

		go func(srv *server.Server) {
			defer testWg.Done()

			StartServer(srv)
			time.Sleep(healthCheckDelay)

			resp, err := http.Get("http://" + srv.Address)
			if err != nil {
				t.Errorf("Expected request to %s to succeed, but error was: %v", srv.Address, err)
			}
			if resp != nil {
				resp.Body.Close() // really important, even though we don't use it
			}
		}(srv)
	}

	testWg.Wait()
}

func TestStartAndStopAllServers(t *testing.T) {
	defer cleanUp()
	defer killServers()

	var addresses = []string{
		"localhost:3723",
		"127.0.0.1:2388",
		":7732",
	}
	caddyfile := strings.Join(addresses, " ")
	makeTestServers(t, caddyfile)

	// Start
	StartAllServers()
	time.Sleep(healthCheckDelay)
	for _, srv := range app.Servers {
		resp, err := http.Get("http://" + srv.Address)
		if err != nil {
			t.Errorf("Expected request to %s to succeed, but error was: %v", srv.Address, err)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}

	// Stop
	app.ServersMutex.Lock()
	StopAllServers()
	for _, srv := range app.Servers {
		resp, err := http.Get("http://" + srv.Address)
		if err == nil {
			t.Errorf("Expected server %s to be stopped, but request succeeded", srv.Address)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	app.ServersMutex.Unlock()
}

func TestReplaceAllServers(t *testing.T) {
	defer cleanUp()
	defer func() { StartServer = startServer }()

	addresses1, addresses2 := []string{
		"localhost:2893",
		"127.0.0.1:4439",
		":8867",
	}, []string{
		"localhost:9779",
		"127.0.0.1:1994",
		":4208",
	}
	caddyfile1, caddyfile2 := strings.Join(addresses1, ", "), strings.Join(addresses2, ", ")

	// Set up initial servers
	makeTestServers(t, caddyfile1)

	// Mock StartServers so we don't have to actually start real listeners,
	// but we can still keep track of how many got started. This helps avoid a
	// mess of race conditions and deadlocking in tests.
	var replacementServersStarted int
	StartServer = func(s *server.Server) {
		var found bool
		for _, srv := range app.Servers {
			if srv.Address == s.Address {
				found = true
				break
			}
		}
		if found {
			replacementServersStarted++ // doesn't count unless we recognize it
		} else {
			t.Errorf("Unexpected server started (address '%s')", s.Address)
		}
	}

	// Replace with other set of servers
	err := ReplaceAllServers("Testfile", strings.NewReader(caddyfile2))
	if err != nil {
		t.Fatalf("Expected no errors during replacement, but got '%v'", err)
	}

	// Make sure all the old ones are gone
	app.ServersMutex.Lock()
	for _, srv := range app.Servers {
		for _, vhost := range srv.Vhosts {
			var found bool
			for _, addr := range addresses2 {
				if vhost.Config.Address() == addr {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Found server with address %s after replacement, but shouldn't have", vhost.Config.Address())
			}
		}
	}
	app.ServersMutex.Unlock()

	// Make sure all the replacement servers were 'started'
	if expected, actual := len(addresses2), replacementServersStarted; expected != actual {
		t.Errorf("Expected %d replacement servers to be started, but %d were", expected, actual)
	}
}

// TODO: TestRollback?
// TODO: Does rollback() trigger the program to quit because it stops all
// servers before starting new ones? (Might have to keep API off to test this.)

func TestHealthCheck(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(4) // Test in parallel so calls to time.Sleep overlap

	var testHealthCheck = func(addr string, shouldSucceed, https bool) {
		begin := time.Now()
		defer wg.Done()

		// Set up HTTP or HTTPS server
		var configs []*server.Config
		if https {
			configs = []*server.Config{
				{TLS: server.TLSConfig{Enabled: true, Certificate: "testdata/server.crt", Key: "testdata/server.key"}},
			}
		} else {
			configs = []*server.Config{{}}
		}

		// Create server, maybe start it
		srv, err := server.New(addr, configs, https)
		if err != nil {
			t.Fatal("Could not create test server:", err)
		}
		if shouldSucceed {
			go srv.Start()
			defer func() {
				srv.Lock()
				srv.Stop(0)
				srv.Unlock()
			}()
		}

		// Perform health check and assert
		err = healthcheck(srv)

		if actual := time.Since(begin); actual < healthCheckDelay {
			t.Errorf("Expected healtcheck to sleep for %v so server could start, but test took only %v",
				healthCheckDelay, actual)
		}
		if shouldSucceed && err != nil {
			t.Errorf("Expected healthcheck for %s to succeed, but error was '%v'", addr, err)
		} else if !shouldSucceed && err == nil {
			t.Errorf("Expected healthcheck for %s to fail, but no error", addr)
		}
	}

	go testHealthCheck(":4011", true, false)
	go testHealthCheck(":4012", true, true)
	go testHealthCheck(":4013", false, false)
	go testHealthCheck(":4014", false, true)

	wg.Wait()
}

func TestHealthcheckRollback(t *testing.T) {
	defer cleanUp()
	defer func() { StartServer = startServer }()
	defer func() { healthcheck = performHealthCheck }()

	backupAddr := "localhost:5931"

	// Mock functions that are under test elsewhere,
	// since they can be slow and complicated.

	var healthCheckPerformed bool
	healthcheck = func(s *server.Server) error {
		healthCheckPerformed = true
		return errors.New("forced health check")
	}

	var startServerCalled bool
	StartServer = func(s *server.Server) {
		startServerCalled = true
		if s.Address != backupAddr {
			t.Errorf("Expected backup addr to be %s, but was %s", backupAddr, s.Address)
		}
	}

	srv := &server.Server{Graceful: server.NewGraceful(testAddr, nil), Address: testAddr}
	backup := &server.Server{Address: backupAddr}

	healthCheckRollback(srv, backup)

	if !healthCheckPerformed {
		t.Error("Expected health check to be performed, but it wasn't")
	}
	if !startServerCalled {
		t.Error("Expected backup server to be restarted, but it wasn't")
	}
}

//
// HELPFUL TEST UTILITY FUNCTIONS BELOW
//

// setUp sets up a test by creating the test server(s) according to
// the contents of caddyfile, then prepares a request according to
// the method, path, and body that is passed in. It also returns a
// ResponseRecorder for use in checking the response. It does NOT
// start any listeners. For that, you should use StartServer().
// A call to cleanUp should happen at the end of a function calling setUp.
// If caddyfile is empty string, no servers are created; just the
// return values are.
func setUp(t *testing.T, caddyfile, method, path string, body io.Reader) (*httptest.ResponseRecorder, *http.Request, httprouter.Params) {
	if caddyfile != "" {
		makeTestServers(t, caddyfile)
	}
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		t.Fatalf("Error creating request: %v", err)
	}
	w := httptest.NewRecorder()
	_, param, _ := router.Lookup(method, path)
	return w, req, param
}

// cleanUp empties the app.Servers slice. It should be
// called at the end of a test function that calls
// the real InitializeWithBindings function. It is safe
// for concurrent use.
func cleanUp() {
	app.ServersMutex.Lock()
	app.Servers = []*server.Server{}
	app.ServersMutex.Unlock()
}

// makeTestServers clears app.Servers and then populates it
// according to the contents of the caddyfile. It does NOT
// start the listeners. A test function that calls this should
// clean up afterwards with a call to cleanUp().
func makeTestServers(t *testing.T, caddyfile string) {
	cleanUp() // start fresh each time

	configs, err := config.Load("Testfile", strings.NewReader(caddyfile))
	if err != nil {
		t.Fatalf("Could not create server configs: %v", err)
	}

	// Arrange it by bind address (resolve hostname)
	bindings, err := config.ArrangeBindings(configs)
	if err != nil {
		t.Fatalf("Could not arrange test server bindings: %v", err)
	}

	for address, cfgs := range bindings {
		// Create a server that will build the virtual host
		s, err := server.New(address.String(), cfgs, cfgs[0].TLS.Enabled)
		if err != nil {
			t.Fatalf("Could not create test server %s: %v", address, err)
		}

		// See if there's a server that is already listening at the address
		var hasListener bool
		for _, existingServer := range app.Servers {
			if address.String() == existingServer.Address {
				hasListener = true

				// The virtual host address must not exist already
				if _, vhostExists := existingServer.Vhosts[cfgs[0].Host]; vhostExists {
					t.Fatalf("Virtualhost already exists: %s", cfgs[0].Address())
				}

				vh := s.Vhosts[cfgs[0].Host]
				existingServer.Vhosts[cfgs[0].Host] = vh
				break
			}
		}

		if !hasListener {
			// Initiate the new server that will operate the listener for this virtualhost
			app.Servers = append(app.Servers, s)
		}
	}
}

// killServers immediately and forcefully stops all
// servers but does not delete them. Call this function
// after tests that start listeners. It is safe for
// concurrent use. It blocks until all servers have
// completed shutting down.
func killServers() {
	app.ServersMutex.Lock()
	for _, serv := range app.Servers {
		serv.Stop(0)
	}
	app.ServersMutex.Unlock()
	serverWg.Wait()
}
