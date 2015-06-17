package admin

import (
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/server"
)

func TestServerList(t *testing.T) {
	defer cleanUp()

	caddyfile := testAddr
	w, r, p := setUp(t, caddyfile, "GET", "/", nil)

	serverList(w, r, p)

	if expected, actual := http.StatusOK, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
	if expected, actual := "application/json", w.Header().Get("Content-Type"); expected != actual {
		t.Errorf("Expected Content-Type: %s, got %s", expected, actual)
	}
	if w.Body.Len() == 0 {
		t.Errorf("Expected response body to be non-empty")
	}
}

func TestServersCreate(t *testing.T) {
	defer cleanUp()
	defer func() { InitializeReadConfig = initializeReadConfig }()

	caddyfile := testAddr
	newServerAddr := "127.0.0.1:3932"
	reqCaddyfile := newServerAddr + "\ngzip"
	w, r, p := setUp(t, caddyfile, "POST", "/?replace=true", strings.NewReader(reqCaddyfile))

	// Test good response

	// We need only confirm that this function was called correctly
	var initializeCalled bool
	InitializeReadConfig = func(filename string, body io.Reader, replace bool) ([]*server.Server, error) {
		initializeCalled = true

		if expected, actual := "HTTP_POST", filename; expected != actual {
			t.Errorf("Expected filename to be %s but was %s", expected, actual)
		}
		buf, err := ioutil.ReadAll(body)
		if err != nil {
			t.Fatalf("Expected no errors reading body, but error was '%v'", err)
		}
		if expected, actual := reqCaddyfile, string(buf); expected != actual {
			t.Errorf("Expected request body to be:\n%s\n but was:\n%s", expected, actual)
		}
		if !replace {
			t.Error("Expected replace to be true, but was false")
		}
		return nil, nil
	}

	serversCreate(w, r, p)

	if expected, actual := http.StatusAccepted, w.Code; expected != actual {
		t.Errorf("Expected good status %d, got %d", expected, actual)
	}

	// Test error response

	InitializeReadConfig = func(filename string, body io.Reader, replace bool) ([]*server.Server, error) {
		return nil, errors.New("testing bad request (normal)")
	}
	w, r, p = setUp(t, caddyfile, "POST", "/", strings.NewReader("asdf"))

	serversCreate(w, r, p)

	if expected, actual := http.StatusBadRequest, w.Code; expected != actual {
		t.Errorf("Expected error status %d, got %d", expected, actual)
	}
}

func TestServersReplace(t *testing.T) {
	defer func() { ReplaceAllServers = replaceAllServers }()

	// Mock this function; we need only to assert it was called properly
	var replaceAllServersCalled bool
	ReplaceAllServers = func(source string, input io.Reader) error {
		replaceAllServersCalled = true
		if expected, actual := "HTTP_POST", source; expected != actual {
			t.Errorf("Expected source file to be '%s' but was '%s'", expected, actual)
		}

		buf, err := ioutil.ReadAll(input)
		if err != nil {
			t.Errorf("Didn't expect error reading body, but had '%v'", err)
			return err
		}
		if len(buf) == 0 {
			t.Errorf("Expected input body to have length > 0, but it didn't")
			return errors.New("empty body")
		}

		return nil
	}

	caddyfile := testAddr
	newServerAddr := "127.0.0.1:3933"
	newCaddyfile := newServerAddr
	w, r, p := setUp(t, caddyfile, "PUT", "/", strings.NewReader(newCaddyfile))

	serversReplace(w, r, p)

	if expected, actual := http.StatusAccepted, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
	if !replaceAllServersCalled {
		t.Error("Expected call to ReplaceAllServers, but it wasn't called")
	}
	replaceAllServersCalled = false

	// Test error conditions

	w, r, p = setUp(t, caddyfile, "PUT", "/", strings.NewReader(""))
	serversReplace(w, r, p)
	if expected, actual := http.StatusBadRequest, w.Code; expected != actual {
		t.Errorf("With empty body, expected status %d, got %d", expected, actual)
	}

	ReplaceAllServers = func(source string, input io.Reader) error {
		return errors.New("bad syntax handler")
	}
	w, r, p = setUp(t, caddyfile, "PUT", "/", strings.NewReader("<invalid syntax>"))
	serversReplace(w, r, p)
	if expected, actual := http.StatusBadRequest, w.Code; expected != actual {
		t.Errorf("With invalid syntax, expected status %d, got %d", expected, actual)
	}
}

func TestServerInfo(t *testing.T) {
	caddyfile := testAddr
	w, r, p := setUp(t, caddyfile, "GET", "/"+testAddr, nil)

	serverInfo(w, r, p)

	if expected, actual := http.StatusOK, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
	if expected, actual := "application/json", w.Header().Get("Content-Type"); expected != actual {
		t.Errorf("Expected Content-Type: %s, got %s", expected, actual)
	}
	if w.Body.Len() == 0 {
		t.Errorf("Expected response body to be non-empty")
	}
}

func TestServerStop(t *testing.T) {
	caddyfile := testAddr + ` {
	}
	localhost:6842, 127.0.0.1:6842 {
	}`
	w, r, p := setUp(t, caddyfile, "DELETE", "/"+testAddr, nil)

	shutdownCallbackExecuted := make(chan bool, 1)
	for _, srv := range app.Servers {
		for _, vhost := range srv.Vhosts {
			vhost.Config.Shutdown = append(vhost.Config.Shutdown, func() error {
				shutdownCallbackExecuted <- true
				return nil
			})
		}
	}

	// Start and stop the first server.
	StartAllServers()
	time.Sleep(healthCheckDelay)
	serverStop(w, r, p)
	if expected, actual := http.StatusAccepted, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	resp, err := http.Get("http://" + testAddr)
	if err == nil {
		t.Errorf("Expected server to be shut down, but GET request succeeded: %s", resp.Status)
		resp.Body.Close()
	}

	select {
	case <-shutdownCallbackExecuted:
	case <-time.After(app.ShutdownCutoff):
		t.Errorf("Shutdown callback was not executed")
	}

	// Now stop just one vhost of the second server instead of the whole listener
	secondServer, _, _ := serverAndVirtualHost("localhost:6842")
	method, path := "DELETE", "/localhost:6842"
	r, err = http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatalf("Error creating request: %v", err)
	}
	w = httptest.NewRecorder()
	_, p, _ = router.Lookup(method, path)

	serverStop(w, r, p)
	if expected, actual := http.StatusAccepted, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	select {
	case <-shutdownCallbackExecuted:
	case <-time.After(app.ShutdownCutoff):
		t.Errorf("Shutdown callback was not executed")
	}

	if _, ok := secondServer.Vhosts["localhost"]; ok {
		t.Error("Expected second server to have stopped virtualhost deleted, but it still exists")
	}
}

// The address to use for creating test servers. It is important
// for several tests that other servers do not have the same hostname
// as this one, so be careful if changing this value.
const testAddr = "localhost:2099"
