package auth

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestIsStateChanging(t *testing.T) {
	tests := []struct {
		method string
		want   bool
	}{
		{http.MethodGet, false},
		{http.MethodHead, false},
		{http.MethodOptions, false},
		{http.MethodPost, true},
		{http.MethodPut, true},
		{http.MethodPatch, true},
		{http.MethodDelete, true},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			if got := isStateChanging(tt.method); got != tt.want {
				t.Errorf("isStateChanging(%q) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		name         string
		origin       string
		configOrigin string
		host         string
		want         bool
	}{
		{"absent origin denied", "", "", "example.com", false},
		{"matching host, no config origin", "https://example.com", "", "example.com", true},
		{"wrong host, no config origin", "https://evil.com", "", "example.com", false},
		{"case-insensitive host match", "https://Example.COM", "", "example.com", true},
		{"unparseable origin", "://bad", "", "example.com", false},

		{"matching config origin", "https://enshu.example", "https://enshu.example", "ignored.internal", true},
		{"wrong host vs config origin", "https://evil.com", "https://enshu.example", "ignored.internal", false},
		{"wrong scheme vs config origin", "http://enshu.example", "https://enshu.example", "ignored.internal", false},
		{"wrong port vs config origin", "https://enshu.example:8080", "https://enshu.example", "ignored.internal", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{cfg: Config{Origin: tt.configOrigin}}
			if tt.configOrigin != "" {
				var err error
				s.origin, err = url.Parse(tt.configOrigin)
				if err != nil {
					t.Fatalf("parse configOrigin: %v", err)
				}
			}
			r := httptest.NewRequest(http.MethodPost, "http://"+tt.host+"/x", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			got := s.checkOrigin(r)
			if got != tt.want {
				t.Errorf("checkOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMiddleware_CSRFBlocksBeforeHandler(t *testing.T) {
	s := &Service{cfg: Config{}}
	called := false
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	r := httptest.NewRequest(http.MethodPost, "http://example.com/x", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if called {
		t.Error("handler should not have been called")
	}
}

func TestMiddleware_CSRFRejectionIsLogged(t *testing.T) {
	s := &Service{cfg: Config{}}
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not have been called")
	}))

	r := httptest.NewRequest(http.MethodPost, "http://example.com/x", nil)
	r.Host = "example.com"
	r.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	handler.ServeHTTP(w, r)

	got := buf.String()
	for _, want := range []string{"POST", "/x", `Origin="https://evil.com"`, `Host="example.com"`} {
		if !strings.Contains(got, want) {
			t.Errorf("log output %q missing %q", got, want)
		}
	}
}

func TestMiddleware_CSRFAllowsMatchingOrigin(t *testing.T) {
	s := &Service{cfg: Config{}}
	called := false
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodPost, "http://example.com/x", nil)
	r.Host = "example.com"
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !called {
		t.Error("handler should have been called")
	}
}

func TestRequireUser_NoSessionGET(t *testing.T) {
	handler := RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestRequireUser_NoSessionPOST(t *testing.T) {
	handler := RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}
