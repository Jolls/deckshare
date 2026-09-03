package http

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/auth"
)

// parseCSP splits a Content-Security-Policy header value into directive -> sources, failing loudly
// on a directive repeated within the same header (browsers honour only the first occurrence).
func parseCSP(t *testing.T, policy string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, d := range strings.Split(policy, ";") {
		fields := strings.Fields(d)
		if len(fields) == 0 {
			continue
		}
		if _, dup := out[fields[0]]; dup {
			t.Fatalf("directive %q appears twice; browsers honour only the first occurrence", fields[0])
		}
		out[fields[0]] = fields[1:]
	}
	return out
}

func TestContentSecurityPolicy_Directives(t *testing.T) {
	got := parseCSP(t, contentSecurityPolicy)

	tests := []struct {
		directive string
		want      []string
		why       string
	}{
		{"default-src", []string{"'none'"}, "deny by default: font/media/worker/frame/object all fall back here"},
		{"script-src", []string{"'self'", "'unsafe-eval'"}, "all JS is served from /static/; 'unsafe-eval' is htmx's hx-vals js: Function() -- plan Open question 1"},
		{"style-src", []string{"'self'", "'unsafe-inline'", "https://cdn.jsdelivr.net"}, `card HTML carries inline style="" attributes that cannot take a nonce; layout.html loads Pico from jsDelivr`},
		{"img-src", []string{"'self'", "data:"}, "card media is same-origin only; data: is Pico's own form-control icons"},
		{"connect-src", []string{"'self'"}, "htmx XHR to /api/reviews/next + review.js's fetch()/navigator.sendBeacon to /api/reviews/batch"},
		{"form-action", []string{"'self'"}, "every form in web/templates/ posts same-origin"},
		{"frame-ancestors", []string{"'none'"}, "clickjacking against the rating buttons -- internal/render/css.go"},
		{"base-uri", []string{"'none'"}, "a <base> would repoint every relative card-media URL at once"},
	}

	for _, tt := range tests {
		t.Run(tt.directive, func(t *testing.T) {
			if !slices.Equal(got[tt.directive], tt.want) {
				t.Errorf("%s = %v, want %v (%s)", tt.directive, got[tt.directive], tt.want, tt.why)
			}
		})
	}

	if len(got) != len(tests) {
		t.Errorf("policy has %d directives, table covers %d -- add a row for the new one", len(got), len(tests))
	}
}

func TestContentSecurityPolicy_NoLoosening(t *testing.T) {
	got := parseCSP(t, contentSecurityPolicy)

	t.Run("script-src admits no inline script and no remote origin", func(t *testing.T) {
		for _, src := range got["script-src"] {
			if src != "'self'" && src != "'unsafe-eval'" {
				t.Errorf("script-src has %q: sanitisation already strips every script vector; CSP must not be the thing that lets one back in", src)
			}
		}
	})

	t.Run("img-src admits no external origin", func(t *testing.T) {
		// internal/render still permits http/https on <img src> -- bluemonday's scheme
		// allowlist is policy-global, not per-element -- so this directive is what actually
		// refuses the load of a remote card image (a cross-user tracking beacon).
		for _, src := range got["img-src"] {
			if src != "'self'" && src != "data:" {
				t.Errorf("img-src has %q: card media must stay same-origin only", src)
			}
		}
	})

	t.Run("style-src names exactly one external origin", func(t *testing.T) {
		var external []string
		for _, src := range got["style-src"] {
			if !strings.HasPrefix(src, "'") {
				external = append(external, src)
			}
		}
		want := []string{"https://cdn.jsdelivr.net"}
		if !slices.Equal(external, want) {
			t.Errorf("style-src external sources = %v, want %v -- drop this when Pico is vendored under /static/", external, want)
		}
	})

	t.Run("no wildcard anywhere", func(t *testing.T) {
		for directive, sources := range got {
			for _, src := range sources {
				if strings.Contains(src, "*") {
					t.Errorf("%s has wildcard source %q", directive, src)
				}
			}
		}
	})
}

func TestSecurityHeaders_OnEveryResponse(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, _ := setupOneCard(t, tx, handler, cookie)

	tests := []struct {
		name, method, path, body string
		cookie                   *http.Cookie
		origin                   string
		wantStatus               int
	}{
		{"public page", "GET", "/login", "", nil, "", 200},
		{"redirect", "GET", "/", "", nil, "", 303},
		{"static asset", "GET", "/static/htmx.min.js", "", nil, "", 200},
		{"deck list", "GET", "/decks", "", cookie, "", 200},
		{"reviewer page", "GET", "/decks/" + deckID + "/review", "", cookie, "", 200},
		{"refill fragment", "GET", "/api/reviews/next?deck=" + deckID + "&cursor=", "", cookie, "", 200},
		{"not found", "GET", "/decks/00000000-0000-0000-0000-000000000000/review", "", cookie, "", 404},
		{"CSRF rejection", "POST", "/decks", "name=X", cookie, "http://evil.example", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, tt.method, tt.path, tt.body, tt.cookie, tt.origin)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body.String())
			}
			// The CSRF-rejection case only passes if securityHeaders sits outside
			// auth.Service.Middleware -- a future reorder would silently drop the header from
			// every rejected request.
			if got := w.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
				t.Errorf("Content-Security-Policy = %q, want %q", got, contentSecurityPolicy)
			}
			// Set alongside the CSP by the same middleware, so they share its outside-the-CSRF
			// -middleware guarantee above.
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
			}
			if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
				t.Errorf("Referrer-Policy = %q, want %q", got, "same-origin")
			}
		})
	}
}

func TestReviewPage_StyleSrcUnsafeInlineIsLoadBearing(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	ntBody := url.Values{}
	ntBody.Set("name", "Basic2")
	ntBody.Set("css", ".card { color: red; }")
	ntBody.Add("field_name[]", "Front")
	ntBody.Add("field_name[]", "Back")
	ntBody.Add("template_name[]", "Card 1")
	ntBody.Add("qfmt[]", "{{Front}}")
	ntBody.Add("afmt[]", "{{FrontSide}}<hr>{{Back}}")
	w = doRequest(handler, "POST", "/note-types", ntBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note type status = %d: %s", w.Code, w.Body.String())
	}

	var noteTypeID string
	if err := tx.QueryRow(context.Background(), `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	noteBody := url.Values{}
	noteBody.Set("note_type_id", noteTypeID)
	noteBody.Add("field[]", `<span style="color: blue">Q</span>`)
	noteBody.Add("field[]", "A")
	w = doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d: %s", w.Code, w.Body.String())
	}

	deckID := strings.TrimPrefix(deckPath, "/decks/")
	w = doRequest(handler, "GET", "/decks/"+deckID+"/review", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET review status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// The note-type CSS block, from review.html -- this is why a style-src nonce cannot be
	// added without breaking the next assertion (a nonce anywhere in style-src makes CSP
	// ignore 'unsafe-inline' entirely, per security.go's style-src comment).
	if !strings.Contains(body, "<style>") || !strings.Contains(body, "color: red") {
		t.Error("response missing note-type <style> block with sanitised CSS -- see security.go's style-src comment")
	}

	// An inline style attribute surviving sanitiseCardHTML onto card markup -- this is why
	// 'unsafe-inline' cannot be dropped from style-src. A regex, not an exact string: bluemonday's
	// whitespace normalisation of the re-emitted attribute is not contractual.
	inlineStyleRe := regexp.MustCompile(`style="[^"]*color[^"]*blue`)
	if !inlineStyleRe.MatchString(body) {
		t.Error("response missing sanitised inline style=\"...color...blue\" on card content -- see security.go's style-src comment")
	}
}
