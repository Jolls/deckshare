package http

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name         string
		remoteAddr   string
		trustedProxy string
		xff          []string
		want         string
	}{
		{"default path, no trusted proxy", "203.0.113.5:5555", "", nil, "203.0.113.5"},
		{"header never read when unset", "203.0.113.5:5555", "", []string{"1.2.3.4"}, "203.0.113.5"},
		{"spoofed header from untrusted peer ignored", "203.0.113.5:5555", "10.0.0.0/8", []string{"1.2.3.4"}, "203.0.113.5"},
		{"trusted peer, header absent", "10.0.0.5:5555", "10.0.0.0/8", nil, "10.0.0.5"},
		{"trusted single hop", "10.0.0.5:5555", "10.0.0.0/8", []string{"203.0.113.9"}, "203.0.113.9"},
		{"inner trusted hop skipped", "10.0.0.5:5555", "10.0.0.0/8", []string{"203.0.113.9, 10.0.0.6"}, "203.0.113.9"},
		{"chain across two trusted CIDRs", "10.0.0.5:5555", "10.0.0.0/8,172.16.0.0/12", []string{"203.0.113.9, 172.16.4.4, 10.0.0.6"}, "203.0.113.9"},
		{"client-forged left hop not reached", "10.0.0.5:5555", "10.0.0.0/8", []string{"1.2.3.4, 203.0.113.9"}, "203.0.113.9"},
		{"all hops trusted, fall back to peer", "10.0.0.5:5555", "10.0.0.0/8", []string{"10.0.0.6, 10.0.0.7"}, "10.0.0.5"},
		{"malformed rightmost, fall back to peer", "10.0.0.5:5555", "10.0.0.0/8", []string{"203.0.113.9, not-an-ip"}, "10.0.0.5"},
		{"malformed left hop never reached", "10.0.0.5:5555", "10.0.0.0/8", []string{"junk, 203.0.113.9"}, "203.0.113.9"},
		{"Header.Values joined, not just the first line", "10.0.0.5:5555", "10.0.0.0/8", []string{"203.0.113.9", "10.0.0.6"}, "203.0.113.9"},
		{"whitespace trimmed", "10.0.0.5:5555", "10.0.0.0/8", []string{"  203.0.113.9  "}, "203.0.113.9"},
		{"IPv4-mapped hop canonicalised", "10.0.0.5:5555", "10.0.0.0/8", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"IPv4-mapped inner hop matches IPv4 CIDR after Unmap", "10.0.0.5:5555", "10.0.0.0/8", []string{"203.0.113.9, ::ffff:10.0.0.6"}, "203.0.113.9"},
		{"IPv6 peer + IPv6 CIDR", "[2001:db8::1]:443", "2001:db8::/32", []string{"2606:4700::1234"}, "2606:4700::1234"},
		{"zone stripped from returned key", "10.0.0.5:5555", "10.0.0.0/8", []string{"fe80::1%eth0"}, "fe80::1"},
		{"unparseable peer, verbatim, header ignored", "unixsocket", "10.0.0.0/8", []string{"1.2.3.4"}, "unixsocket"},
		{"empty header value, fall back to peer", "10.0.0.5:5555", "10.0.0.0/8", []string{""}, "10.0.0.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/login", nil)
			r.RemoteAddr = tt.remoteAddr
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			trusted, err := parseTrustedProxies(tt.trustedProxy)
			if err != nil {
				t.Fatalf("parseTrustedProxies(%q): %v", tt.trustedProxy, err)
			}
			if got := trusted.clientIP(r); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		got, err := parseTrustedProxies("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("whitespace only", func(t *testing.T) {
		got, err := parseTrustedProxies("   ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("single prefix", func(t *testing.T) {
		got, err := parseTrustedProxies("10.0.0.0/8")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("len = %d, want 1", len(got))
		}
	})

	t.Run("whitespace tolerated between elements", func(t *testing.T) {
		got, err := parseTrustedProxies("10.0.0.0/8, 172.16.0.0/12 ,fd00::/8")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("len = %d, want 3", len(got))
		}
	})

	t.Run("empty element skipped", func(t *testing.T) {
		got, err := parseTrustedProxies("10.0.0.0/8,")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("len = %d, want 1", len(got))
		}
	})

	t.Run("bare IP rejected", func(t *testing.T) {
		_, err := parseTrustedProxies("10.0.0.1")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "10.0.0.1") {
			t.Errorf("error %q does not name offending element", err)
		}
	})

	t.Run("garbage rejected", func(t *testing.T) {
		_, err := parseTrustedProxies("garbage")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "garbage") {
			t.Errorf("error %q does not name offending element", err)
		}
	})

	t.Run("out of range prefix rejected", func(t *testing.T) {
		_, err := parseTrustedProxies("10.0.0.0/33")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "10.0.0.0/33") {
			t.Errorf("error %q does not name offending element", err)
		}
	})
}
