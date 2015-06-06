// Package server implements a configurable, general-purpose web server.
// It relies on configurations obtained from the adjacent config package
// and can execute middleware as defined by the adjacent middleware package.
package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"

	"github.com/bradfitz/http2"
)

// Server represents an instance of a server, which serves
// static content at a particular address (host and port).
type Server struct {
	*Graceful `json:"-"`
	HTTP2     bool                    // temporary while http2 is not in std lib (TODO: remove flag when part of std lib)
	Address   string                  // the actual address for net.Listen to listen on
	TLS       bool                    // whether this server is serving all HTTPS hosts or not
	Vhosts    map[string]*VirtualHost // virtual hosts keyed by their address
}

// New creates a new Server which will bind to addr and serve
// the sites/hosts configured in configs. This function does
// not start serving.
func New(addr string, configs []Config, tls bool) (*Server, error) {
	s := &Server{
		Address: addr,
		TLS:     tls,
		Vhosts:  make(map[string]*VirtualHost),
	}

	// Our server is its own handler
	s.Graceful = NewGraceful(addr, s)

	// When server shuts down, make sure each virtualhost cleans up.
	s.Graceful.ShutdownCallback = func() {
		for _, vh := range s.Vhosts {
			vh.Stop()
		}
	}

	for _, conf := range configs {
		if _, exists := s.Vhosts[conf.Host]; exists {
			return nil, fmt.Errorf("Cannot serve %s - host already defined for address %s", conf.Address(), s.Address)
		}

		vh := &VirtualHost{Config: conf}

		// Build middleware stack
		err := vh.BuildStack()
		if err != nil {
			return nil, err
		}

		s.Vhosts[conf.Host] = vh
	}

	return s, nil
}

// Start starts the server. It blocks until the server quits.
func (s *Server) Start() error {
	if s.HTTP2 {
		// TODO: This call may not be necessary after HTTP/2 is merged into std lib
		http2.ConfigureServer(s.Server, nil)
	}

	// Run startup functions or make other preparations
	for _, vh := range s.Vhosts {
		if err := vh.Start(); err != nil {
			return err
		}
	}

	if s.TLS {
		var tlsConfigs []TLSConfig
		for _, vh := range s.Vhosts {
			tlsConfigs = append(tlsConfigs, vh.Config.TLS)
		}
		return ListenAndServeTLSWithSNI(s, tlsConfigs)
	}

	return s.ListenAndServe()
}

// ListenAndServeTLSWithSNI serves TLS with Server Name Indication (SNI) support, which allows
// multiple sites (different hostnames) to be served from the same address. This method is
// adapted directly from the std lib's net/http ListenAndServeTLS function, which was
// written by the Go Authors. It has been modified to support multiple certificate/key pairs.
func ListenAndServeTLSWithSNI(srv *Server, tlsConfigs []TLSConfig) error {
	addr := srv.Addr
	if addr == "" {
		addr = ":https"
	}

	config := new(tls.Config)
	if srv.TLSConfig != nil {
		*config = *srv.TLSConfig
	}
	if config.NextProtos == nil {
		config.NextProtos = []string{"http/1.1"}
	}

	// Here we diverge from the stdlib a bit by loading multiple certs/key pairs
	// then we map the server names to their certs
	var err error
	config.Certificates = make([]tls.Certificate, len(tlsConfigs))
	for i, tlsConfig := range tlsConfigs {
		config.Certificates[i], err = tls.LoadX509KeyPair(tlsConfig.Certificate, tlsConfig.Key)
		if err != nil {
			return err
		}
	}
	config.BuildNameToCertificate()

	// Customize our TLS configuration
	config.MinVersion = tlsConfigs[0].ProtocolMinVersion
	config.MaxVersion = tlsConfigs[0].ProtocolMaxVersion
	config.CipherSuites = tlsConfigs[0].Ciphers
	config.PreferServerCipherSuites = tlsConfigs[0].PreferServerCipherSuites

	// TLS client authentication, if user enabled it
	err = setupClientAuth(tlsConfigs, config)
	if err != nil {
		return err
	}

	// Create listener and we're on our way
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	tlsListener := tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, config)

	return srv.Serve(tlsListener)
}

// IsIgnorableError returns true if err is an error that is expected
// when a server quits - for example, trying to accept on a closed listener.
// In that case, no action need be taken, since of course the listener is
// closed - that is what makes the server quit!
func IsIgnorableError(err error) bool {
	opErr, ok := err.(*net.OpError)
	return !ok || (ok && opErr.Op != "accept")
}

// setupClientAuth sets up TLS client authentication only if
// any of the TLS configs specified at least one cert file.
func setupClientAuth(tlsConfigs []TLSConfig, config *tls.Config) error {
	var clientAuth bool
	for _, cfg := range tlsConfigs {
		if len(cfg.ClientCerts) > 0 {
			clientAuth = true
			break
		}
	}

	if clientAuth {
		pool := x509.NewCertPool()
		for _, cfg := range tlsConfigs {
			for _, caFile := range cfg.ClientCerts {
				caCrt, err := ioutil.ReadFile(caFile) // Anyone that gets a cert from Matt Holt can connect
				if err != nil {
					return err
				}
				if !pool.AppendCertsFromPEM(caCrt) {
					return fmt.Errorf("Error loading client certificate '%s': no certificates were successfully parsed", caFile)
				}
			}
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return nil
}

// ServeHTTP is the entry point for every request to the address that s
// is bound to. It acts as a multiplexer for the requests hostname as
// defined in the Host header so that the correct VirtualHost
// (configuration and middleware stack) will handle the request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		// In case the user doesn't enable error middleware, we still
		// need to make sure that we stay alive up here
		if rec := recover(); rec != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError),
				http.StatusInternalServerError)
		}
	}()

	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host // oh well
	}

	// Try the host as given, or try falling back to 0.0.0.0 (wildcard)
	if _, ok := s.Vhosts[host]; !ok {
		if _, ok2 := s.Vhosts["0.0.0.0"]; ok2 {
			host = "0.0.0.0"
		}
	}

	if vh, ok := s.Vhosts[host]; ok {
		w.Header().Set("Server", "Caddy")

		status, _ := vh.Stack.ServeHTTP(w, r)

		// Fallback error response in case error handling wasn't chained in
		if status >= 400 {
			w.WriteHeader(status)
			fmt.Fprintf(w, "%d %s", status, http.StatusText(status))
		}
	} else {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "No such host at %s", s.Address)
	}
}
