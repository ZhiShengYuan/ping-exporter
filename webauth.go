package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// webConfig is the subset of Prometheus exporter-toolkit web config this
// process enforces. The file format matches node_exporter's web.config.file:
//
//	basic_auth_users:
//	  prometheus: <bcrypt hash>
type webConfig struct {
	BasicAuthUsers map[string]string `yaml:"basic_auth_users"`
}

// loadWebConfig returns an empty config when path is empty, which leaves basic
// auth disabled. A non-empty path with no users is a misconfiguration.
func loadWebConfig(path string) (*webConfig, error) {
	if path == "" {
		return &webConfig{}, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg webConfig
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.BasicAuthUsers) == 0 {
		return nil, errNoBasicAuthUsers
	}
	for user, hash := range cfg.BasicAuthUsers {
		if user == "" || !validBcrypt(hash) {
			return nil, errBadBasicAuth
		}
	}
	return &cfg, nil
}

type configError string

func (e configError) Error() string { return string(e) }

const (
	errNoBasicAuthUsers configError = "web config has no basic_auth_users"
	errBadBasicAuth     configError = "web config basic_auth_users entry is empty or not a bcrypt hash"
)

func validBcrypt(hash string) bool {
	hash = normalizeBcrypt(hash)
	return strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$")
}

// htpasswd and node_exporter emit $2y$; Go's bcrypt accepts $2a$ and $2b$.
func normalizeBcrypt(hash string) string {
	if strings.HasPrefix(hash, "$2y$") {
		return "$2a$" + hash[len("$2y$"):]
	}
	return hash
}

func (c *webConfig) enabled() bool {
	return c != nil && len(c.BasicAuthUsers) > 0
}

func (c *webConfig) allow(user, password string) bool {
	hash, ok := c.BasicAuthUsers[user]
	if !ok {
		// Compare against a real hash so a missing user is not cheaper.
		hash = dummyBcrypt
		user = ""
	}
	err := bcrypt.CompareHashAndPassword([]byte(normalizeBcrypt(hash)), []byte(password))
	return ok && user != "" && err == nil
}

// dummyBcrypt is generated once so a missing user still pays for a bcrypt
// compare. It is not a credential.
var dummyBcrypt string

func init() {
	hash, err := bcrypt.GenerateFromPassword([]byte("ping-exporter-not-a-credential"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	dummyBcrypt = string(hash)
}

func (c *webConfig) wrap(next http.Handler) http.Handler {
	if !c.enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || !c.allow(user, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="ping-exporter"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			slog.Warn("basic auth rejected", "remote", r.RemoteAddr, "path", r.URL.Path)
			return
		}
		next.ServeHTTP(w, r)
	})
}
