package admin

import (
	"net/http"
	"testing"
	"time"

	"github.com/mholt/caddy/app"
)

func TestReload(t *testing.T) {
	defer killServers()

	caddyfile := testAddr
	w, r, p := setUp(t, caddyfile, "POST", "/cmd/reload", nil)

	app.ConfigPath = "reload_test_config.txt"
	reload(w, r, p)
	time.Sleep(healthCheckDelay + time.Second)

	if expected, actual := http.StatusAccepted, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	app.ServersMutex.Lock()
	if expected, actual := 1, len(app.Servers); expected != actual {
		t.Fatalf("Expected %d server, got %d", expected, actual)
	}
	if expected, actual := 1, len(app.Servers[0].Vhosts); expected != actual {
		t.Fatalf("Expected %d virtualhost on %s, got %d", expected, app.Servers[0].Address, actual)
	}
	if _, ok := app.Servers[0].Vhosts["127.0.0.1"]; !ok {
		t.Fatal("Expected server 0 to have vhost 127.0.0.1 but it didn't")
	}
	if app.Servers[0].Vhosts["127.0.0.1"].Config.HandlerMap["ext"] == nil {
		t.Error("Expected the servers be properly configured, but they weren't")
	}
	app.ServersMutex.Unlock()

	// Check the health check
	resp, err := http.Get("http://127.0.0.1:9392")
	if err != nil {
		t.Errorf("Expected GET request to %s to succeed, but error was: %v", "http://127.0.0.1:9392", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}
