package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/middleware/extensions"
)

func TestExtensionsGet(t *testing.T) {
	defer cleanUp()
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
	defer cleanUp()
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
	defer cleanUp()
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
	defer cleanUp()
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
	defer cleanUp()
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
