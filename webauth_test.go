package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestLoadWebConfigDisabled(t *testing.T) {
	cfg, err := loadWebConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.enabled() {
		t.Fatal("empty path should disable auth")
	}
}

func TestLoadWebConfigRejectsEmptyUsers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.yml")
	if err := os.WriteFile(path, []byte("basic_auth_users: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWebConfig(path); err == nil {
		t.Fatal("expected error for empty users")
	}
}

func TestBasicAuth(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	yHash := "$2y$" + string(hash)[len("$2a$"):]
	dir := t.TempDir()
	path := filepath.Join(dir, "web.yml")
	body := "basic_auth_users:\n  prometheus: " + yHash + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadWebConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := cfg.wrap(next)

	unauth := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth code %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate")
	}

	bad := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	bad.SetBasicAuth("prometheus", "nope")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password code %d", rec.Code)
	}

	unknown := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	unknown.SetBasicAuth("other", "s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, unknown)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user code %d", rec.Code)
	}

	ok := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	ok.SetBasicAuth("prometheus", "s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, ok)
	if rec.Code != http.StatusOK {
		t.Fatalf("good password code %d", rec.Code)
	}
}
