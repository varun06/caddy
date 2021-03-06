// Package basicauth implements HTTP Basic Authentication.
package basicauth

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jimstudt/http-authentication/basic"
	"github.com/mholt/caddy/middleware"
)

// BasicAuth is middleware to protect resources with a username and password.
// Note that HTTP Basic Authentication is not secure by itself and should
// not be used to protect important assets without HTTPS. Even then, the
// security of HTTP Basic Auth is disputed. Use discretion when deciding
// what to protect with BasicAuth.
type BasicAuth struct {
	Next     middleware.Handler
	SiteRoot string
	Rules    []Rule
}

// ServeHTTP implements the middleware.Handler interface.
func (a BasicAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) (int, error) {

	var hasAuth bool
	var isAuthenticated bool

	for _, rule := range a.Rules {
		for _, res := range rule.Resources {
			if !middleware.Path(r.URL.Path).Matches(res) {
				continue
			}

			// Path matches; parse auth header
			username, password, ok := r.BasicAuth()
			hasAuth = true

			// Check credentials
			if !ok ||
				username != rule.Username ||
				!rule.Password(password) {
				//subtle.ConstantTimeCompare([]byte(password), []byte(rule.Password)) != 1 {
				continue
			}

			// Flag set only on successful authentication
			isAuthenticated = true
		}
	}

	if hasAuth {
		if !isAuthenticated {
			w.Header().Set("WWW-Authenticate", "Basic")
			return http.StatusUnauthorized, nil
		}
		// "It's an older code, sir, but it checks out. I was about to clear them."
		return a.Next.ServeHTTP(w, r)
	}

	// Pass-thru when no paths match
	return a.Next.ServeHTTP(w, r)
}

// Rule represents a BasicAuth rule. A username and password
// combination protect the associated resources, which are
// file or directory paths.
type Rule struct {
	Username  string
	Password  func(string) bool
	Resources []string
}

type PasswordMatcher func(pw string) bool

var (
	htpasswords   map[string]map[string]PasswordMatcher
	htpasswordsMu sync.Mutex
)

func GetHtpasswdMatcher(filename, username, siteRoot string) (PasswordMatcher, error) {
	filename = filepath.Join(siteRoot, filename)
	htpasswordsMu.Lock()
	if htpasswords == nil {
		htpasswords = make(map[string]map[string]PasswordMatcher)
	}
	pm := htpasswords[filename]
	if pm == nil {
		fh, err := os.Open(filename)
		if err != nil {
			return nil, fmt.Errorf("open %q: %v", filename, err)
		}
		defer fh.Close()
		pm = make(map[string]PasswordMatcher)
		if err = parseHtpasswd(pm, fh); err != nil {
			return nil, fmt.Errorf("parsing htpasswd %q: %v", fh.Name(), err)
		}
		htpasswords[filename] = pm
	}
	htpasswordsMu.Unlock()
	if pm[username] == nil {
		return nil, fmt.Errorf("username %q not found in %q", username, filename)
	}
	return pm[username], nil
}

func parseHtpasswd(pm map[string]PasswordMatcher, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.IndexByte(line, '#') == 0 {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			return fmt.Errorf("malformed line, no color: %q", line)
		}
		user, encoded := line[:i], line[i+1:]
		for _, p := range basic.DefaultSystems {
			matcher, err := p(encoded)
			if err != nil {
				return err
			}
			if matcher != nil {
				pm[user] = matcher.MatchesPassword
				break
			}
		}
	}
	return scanner.Err()
}

func PlainMatcher(passw string) PasswordMatcher {
	return func(pw string) bool {
		return subtle.ConstantTimeCompare([]byte(pw), []byte(passw)) == 1
	}
}
