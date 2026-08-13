package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew_InvalidOrigin(t *testing.T) {
	if _, err := New(nil, Config{Origin: "://bad"}); err == nil {
		t.Error("New with an unparseable Config.Origin should error")
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  bool
	}{
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"valid", "user@example.com", true},
		{"254 chars", strings.Repeat("a", 242) + "@example.com", true}, // 242+12 = 254
		{"255 chars", strings.Repeat("a", 243) + "@example.com", false},
		{"no at sign", "not-an-email", false},
		{"trailing garbage", "user@example.com, evil@example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := validateEmail(tt.email)
			if ok != tt.want {
				t.Errorf("validateEmail(%q) ok = %v, want %v", tt.email, ok, tt.want)
			}
		})
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name string
		pw   string
		want bool
	}{
		{"empty", "", false},
		{"7 chars", strings.Repeat("a", 7), false},
		{"8 chars", strings.Repeat("a", 8), true},
		{"256 chars", strings.Repeat("a", 256), true},
		{"257 chars", strings.Repeat("a", 257), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := validatePassword(tt.pw)
			if ok != tt.want {
				t.Errorf("validatePassword(len=%d) ok = %v, want %v", len(tt.pw), ok, tt.want)
			}
		})
	}
}

func TestValidateDisplayName(t *testing.T) {
	tests := []struct {
		name string
		dn   string
		want bool
	}{
		{"empty", "", false},
		{"valid", "Ada Lovelace", true},
		{"100 chars", strings.Repeat("a", 100), true},
		{"101 chars", strings.Repeat("a", 101), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := validateDisplayName(tt.dn)
			if ok != tt.want {
				t.Errorf("validateDisplayName(%q) ok = %v, want %v", tt.dn, ok, tt.want)
			}
		})
	}
}

func TestValidateTimezone(t *testing.T) {
	tests := []struct {
		name string
		tz   string
		want bool
	}{
		{"UTC", "UTC", true},
		{"America/New_York", "America/New_York", true},
		{"empty", "", false},
		{"unknown", "Not/AZone", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := validateTimezone(tt.tz)
			if ok != tt.want {
				t.Errorf("validateTimezone(%q) ok = %v, want %v", tt.tz, ok, tt.want)
			}
		})
	}
}

func TestHashToken(t *testing.T) {
	a := hashToken("token-a")
	b := hashToken("token-a")
	c := hashToken("token-b")
	if a != b {
		t.Error("same token should hash identically")
	}
	if a == c {
		t.Error("different tokens should hash differently")
	}
	if len(a) != 64 {
		t.Errorf("len(hash) = %d, want 64", len(a))
	}
}

func TestNewToken(t *testing.T) {
	a, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	b, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if a == b {
		t.Error("two calls should not produce the same token")
	}
}

func TestSetSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	SetSessionCookie(w, "the-token")

	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("len(cookies) = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != CookieName {
		t.Errorf("Name = %q, want %q", c.Name, CookieName)
	}
	if c.Value != "the-token" {
		t.Errorf("Value = %q, want %q", c.Value, "the-token")
	}
	if !c.Secure {
		t.Error("Secure should be true")
	}
	if !c.HttpOnly {
		t.Error("HttpOnly should be true")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want %v", c.SameSite, http.SameSiteLaxMode)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if c.Domain != "" {
		t.Errorf("Domain = %q, want empty", c.Domain)
	}
	if c.MaxAge != int(SessionLifetime.Seconds()) {
		t.Errorf("MaxAge = %d, want %d", c.MaxAge, int(SessionLifetime.Seconds()))
	}
}

func TestClearSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	ClearSessionCookie(w)

	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("len(cookies) = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != CookieName {
		t.Errorf("Name = %q, want %q", c.Name, CookieName)
	}
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("Value = %q, want empty", c.Value)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
}
