package admin

import (
	"io"
	"net/http"
	"path"
	"testing"

	"github.com/mholt/caddy/app"
)

func TestReload(t *testing.T) {
	defer func() { ReplaceAllServers = replaceAllServers }()

	w, r, p := setUp(t, "", "POST", "/cmd/reload", nil)
	app.ConfigPath = "testdata/reload_test_config.txt"

	var replaceAllServersCalled bool
	ReplaceAllServers = func(source string, input io.Reader) error {
		replaceAllServersCalled = true
		if expected, actual := path.Base(app.ConfigPath), source; expected != actual {
			t.Errorf("Expected source to be '%s', got '%s' instead", expected, actual)
		}
		if input == nil {
			t.Error("Expected input to be non-nil, but it was nil")
		}
		return nil
	}

	reload(w, r, p)

	if expected, actual := http.StatusAccepted, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	if !replaceAllServersCalled {
		t.Error("Expected call to ReplaceAllServers, but there wasn't one")
	}
}
