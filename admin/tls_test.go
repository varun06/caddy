package admin

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mholt/caddy/app"
)

func TestTLSEnable(t *testing.T) {
	defer cleanUp()

	tlsTestAddr := "localhost:1199"
	caddyfile := tlsTestAddr
	w, r, p := setUp(t, caddyfile, "POST", "/"+tlsTestAddr+"/tls", strings.NewReader(`tls testdata/server.crt testdata/server.key`))

	StartServer(app.Servers[0])
	time.Sleep(healthCheckDelay)
	tlsEnable(w, r, p)
	if expected, actual := http.StatusAccepted, w.Code; expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}

	// Give a moment for goroutine to shutdown the server, enable TLS, and begin health check
	time.Sleep(150 * time.Millisecond)

	app.ServersMutex.Lock()
	app.Servers[0].Lock()
	if !app.Servers[0].TLS {
		t.Error("Expected server to have TLS enabled, but it wasn't")
	}
	app.Servers[0].Unlock()
	app.ServersMutex.Unlock()

	// Check the health check
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // who cares, for a health check
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get("https://" + tlsTestAddr)
	if err != nil {
		t.Errorf("Expected HTTPS request to %s to succeed, but error was: %v", "https://"+tlsTestAddr, err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}
