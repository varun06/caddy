package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/config"
	"github.com/mholt/caddy/middleware/extensions"
	"github.com/mholt/caddy/server"
)

func TestExtensionsGet(t *testing.T) {
	caddyfile := testAddr + `
	         ext .html .txt`
	w, r, p := setUp(t, caddyfile, "GET", "/"+testAddr+"/ext", nil)

	extensionsGet(w, r, p)

	if expected, actual := http.StatusOK, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
	if expected, actual := "application/json", w.Header().Get("Content-Type"); expected != actual {
		t.Errorf("Expected Content-Type: %s, got %s", expected, actual)
	}
	if expected, actual := `{"Extensions":[".html",".txt"]}`, strings.TrimSpace(w.Body.String()); expected != actual {
		t.Errorf("Expected body to be:\n%s\nGot:\n%s", expected, actual)
	}
}

func TestExtensionsCreate(t *testing.T) {
	w, r, p := setUp(t, testAddr, "GET", "/"+testAddr+"/ext", strings.NewReader("ext .html"))

	extensionsCreate(w, r, p)

	if expected, actual := http.StatusCreated, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
	if _, ok := app.Servers[0].Vhosts["localhost"].Config.HandlerMap["ext"]; !ok {
		t.Fatal("Expected ext handler, but there was none")
	}
}

func TestExtensionsDelete(t *testing.T) {
	caddyfile := testAddr + "\next .html"
	w, r, p := setUp(t, caddyfile, "DELETE", "/"+testAddr+"/ext", nil)

	extensionsDelete(w, r, p)

	if expected, actual := http.StatusOK, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
	if _, ok := app.Servers[0].Vhosts["localhost"].Config.HandlerMap["ext"]; ok {
		t.Fatal("Expected ext handler to be gone, but it was still there")
	}
}

func TestExtensionsSet(t *testing.T) {
	caddyfile := testAddr + "\next .html"
	w, r, p := setUp(t, caddyfile, "PUT", "/"+testAddr+"/ext", strings.NewReader(`[".txt",".zip"]`))

	extensionsSet(w, r, p)

	if expected, actual := http.StatusOK, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	actual := app.Servers[0].Vhosts["localhost"].Config.HandlerMap["ext"].(*extensions.Ext).Extensions
	if len(actual) != 2 {
		t.Fatalf("Expected 2 extensions, had %d", len(actual))
	}
	if actual[0] != ".txt" {
		t.Errorf("Expected extension 0 to be .txt, but was %s", actual[0])
	}
	if actual[1] != ".zip" {
		t.Errorf("Expected extension 0 to be .zip, but was %s", actual[1])
	}
}

func TestExtensionsAdd(t *testing.T) {
	caddyfile := testAddr + "\next .html"
	w, r, p := setUp(t, caddyfile, "POST", "/"+testAddr+"/ext/extensions", strings.NewReader(`[".htm"]`))

	extensionsAdd(w, r, p)

	if expected, actual := http.StatusCreated, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	actual := app.Servers[0].Vhosts["localhost"].Config.HandlerMap["ext"].(*extensions.Ext).Extensions
	if len(actual) != 2 {
		t.Fatalf("Expected 2 extensions, had %d", len(actual))
	}
	if actual[0] != ".html" {
		t.Errorf("Expected extension 0 to be .html, but was %s", actual[0])
	}
	if actual[1] != ".htm" {
		t.Errorf("Expected extension 1 to be .htm, but was %s", actual[1])
	}
}

func TestExtensionsRemove(t *testing.T) {
	caddyfile := testAddr + "\next .html .htm .txt"
	w, r, p := setUp(t, caddyfile, "POST", "/"+testAddr+"/ext/extensions", strings.NewReader(`[".htm"]`))

	extensionsRemove(w, r, p)

	if expected, actual := http.StatusOK, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	actual := app.Servers[0].Vhosts["localhost"].Config.HandlerMap["ext"].(*extensions.Ext).Extensions
	if len(actual) != 2 {
		t.Fatalf("Expected 2 extensions, had %d (%v)", len(actual), actual)
	}
	if actual[0] != ".html" {
		t.Errorf("Expected extension 0 to be .html, but was %s", actual[0])
	}
	if actual[1] != ".txt" {
		t.Errorf("Expected extension 1 to be .txt, but was %s", actual[1])
	}
}

// setUp sets up a test by creating the test server(s) according to
// the contents of caddyfile, then prepares a request according to
// the method, path, and body that is passed in. It also returns a
// ResponseRecorder for use in checking the response.
func setUp(t *testing.T, caddyfile, method, path string, body io.Reader) (*httptest.ResponseRecorder, *http.Request, httprouter.Params) {
	makeTestServer(t, caddyfile)
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		t.Fatalf("Error creating request: %v", err)
	}
	w := httptest.NewRecorder()
	_, param, _ := router.Lookup(method, path)
	return w, req, param
}

// makeTestServer clears app.Servers and then .... <TODO>
func makeTestServer(t *testing.T, caddyfile string) {
	app.Servers = []*server.Server{} // start empty each time

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

				// Okay, now the virtual host address must not exist already
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

const testAddr = "localhost:2015"
