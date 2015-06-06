package admin

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/mholt/caddy/app"
	"github.com/mholt/caddy/middleware/extensions"
)

func TestRootSet(t *testing.T) {
	caddyfile := testAddr + `
	         root /fizz
	         ext .testing`
	newRoot := "/buzz"
	w, r, p := setUp(t, caddyfile, "GET", "/"+testAddr+"/root?root="+url.QueryEscape(newRoot), nil)

	rootSet(w, r, p)

	if expected, actual := http.StatusOK, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
	if expected, actual := newRoot, app.Servers[0].Vhosts["localhost"].Config.Root; expected != actual {
		t.Errorf("Expected new root to be '%s' but was '%s'", expected, actual)
	}

	ext, ok := app.Servers[0].Vhosts["localhost"].Config.HandlerMap["ext"].(*extensions.Ext)
	if ext == nil || !ok {
		t.Fatal("Rebuilding must have failed because the extensions middleware is invalid or missing")
	}
	if ext.Root != newRoot {
		t.Errorf("Expected entire middleare stack to be rebuilt; new root should be '%s' but was '%s'", newRoot, ext.Root)
	}
}
