package http

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/deckshare/internal/auth"
)

// colorSchemeOf reads the stored colour scheme (#268) for a user id.
func colorSchemeOf(t *testing.T, tx pgx.Tx, id string) string {
	t.Helper()
	var s string
	if err := tx.QueryRow(context.Background(), `SELECT color_scheme FROM users WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read color_scheme: %v", err)
	}
	return s
}

func TestSettingsAppearanceRoutes_AllowDeny(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	tests := []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"POST /settings/appearance no session", nil, 303},
		{"POST /settings/appearance valid session", cookie, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent=azure", tt.cookie, "http://example.com")
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

func TestPostSettingsAppearanceWithoutOrigin_403(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")

	w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent=azure", cookie, "")
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if got := colorSchemeOf(t, tx, userID(t, context.Background(), tx, email)); got != "auto" {
		t.Errorf("color_scheme = %q after a rejected POST, want auto", got)
	}
}

func TestSettingsAppearanceGoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	id := userID(t, context.Background(), tx, email)

	for _, tt := range []struct{ scheme, wantTag string }{
		{"dark", `<html lang="en" data-theme="dark">`},
		{"light", `<html lang="en" data-theme="light">`},
		{"auto", `<html lang="en">`},
	} {
		w := doRequest(handler, "POST", "/settings/appearance", "color_scheme="+tt.scheme+"&accent=azure", cookie, "http://example.com")
		if w.Code != 200 {
			t.Fatalf("%s: status = %d, want 200: %s", tt.scheme, w.Code, w.Body.String())
		}
		if got := colorSchemeOf(t, tx, id); got != tt.scheme {
			t.Errorf("stored color_scheme = %q, want %q", got, tt.scheme)
		}
		if !strings.Contains(w.Body.String(), tt.wantTag) {
			t.Errorf("%s: response missing %s", tt.scheme, tt.wantTag)
		}
	}
}

// The caller's id comes from the session; a user_id/id in the form must be ignored.
func TestSettingsAppearance_OnlyCallerRow(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ctx := context.Background()
	emailA, emailB := testEmail(), testEmail()
	cookieA := loginCookie(t, tx, a, emailA, "correct-horse-battery")
	loginCookie(t, tx, a, emailB, "correct-horse-battery")
	idA, idB := userID(t, ctx, tx, emailA), userID(t, ctx, tx, emailB)

	w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent=azure&user_id="+idB+"&id="+idB, cookieA, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := colorSchemeOf(t, tx, idA); got != "dark" {
		t.Errorf("caller's color_scheme = %q, want dark", got)
	}
	if got := colorSchemeOf(t, tx, idB); got != "auto" {
		t.Errorf("other user's color_scheme = %q, want auto", got)
	}
}

func TestSettingsAppearance_RejectsUnknownValue(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	id := userID(t, context.Background(), tx, email)

	for _, v := range []string{"sepia", "", "DARK", "%20dark", "auto%3B"} {
		w := doRequest(handler, "POST", "/settings/appearance", "color_scheme="+v+"&accent=azure", cookie, "http://example.com")
		if w.Code != 400 {
			t.Errorf("value %q: status = %d, want 400", v, w.Code)
		}
		if !strings.Contains(w.Body.String(), "Choose Light, Dark or Auto") {
			t.Errorf("value %q: response missing validation message", v)
		}
		if got := colorSchemeOf(t, tx, id); got != "auto" {
			t.Errorf("value %q: stored color_scheme = %q, want unchanged auto", v, got)
		}
	}
}

func TestSettingsAppearance_AppliedOnNextPage(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent=azure", cookie, "http://example.com")
	w := doRequest(handler, "GET", "/decks", "", cookie, "")
	if w.Code != 200 {
		t.Fatalf("GET /decks status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `<html lang="en" data-theme="dark">`) {
		t.Error("the next page load should render the stored scheme")
	}
}

func TestSettingsPage_AppearanceRadiosReflectStoredValue(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "GET", "/settings", "", cookie, "")
	if !strings.Contains(w.Body.String(), `value="auto" checked`) {
		t.Error("a fresh user should see Auto selected")
	}
	doRequest(handler, "POST", "/settings/appearance", "color_scheme=light&accent=azure", cookie, "http://example.com")
	body := doRequest(handler, "GET", "/settings", "", cookie, "").Body.String()
	if !strings.Contains(body, `value="light" checked`) || strings.Contains(body, `value="auto" checked`) {
		t.Error("after choosing Light, only Light should be selected")
	}
}

// #276: the card stage follows the page scheme, so it must not force one.
func TestReviewPage_CardStageFollowsScheme(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, _ := setupOneCard(t, tx, handler, cookie)
	doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent=azure", cookie, "http://example.com")

	body := doRequest(handler, "GET", "/decks/"+deckID+"/review", "", cookie, "").Body.String()
	if !strings.Contains(body, `<html lang="en" data-theme="dark">`) {
		t.Error("review page should render the user's dark scheme on <html>")
	}
	if !strings.Contains(body, `class="review-card deckshare-card">`) || strings.Contains(body, `class="review-card deckshare-card" data-theme`) {
		t.Error("#review-stage must not carry its own data-theme, so cards follow the page scheme")
	}
}

func TestStudyAll_CardStageFollowsScheme(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	setupOneCard(t, tx, handler, cookie)

	body := doRequest(handler, "GET", "/study", "", cookie, "").Body.String()
	if !strings.Contains(body, `class="review-card deckshare-card">`) || strings.Contains(body, `class="review-card deckshare-card" data-theme`) {
		t.Error("#review-stage on /study must not carry its own data-theme")
	}
}

func TestNotePreview_CardFollowsScheme(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com").Header().Get("Location")
	if w := doRequest(handler, "POST", "/note-types", newClozeNoteTypeBody("ClozeLS"), cookie, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Fatalf("create cloze note type status = %d: %s", w.Code, w.Body.String())
	}
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'ClozeLS'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}
	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", "{{c1::first}} and {{c2::second}}")
	w := doRequest(handler, "POST", deckPath+"/notes/preview", body.Encode(), cookie, "http://example.com")
	if got := strings.Count(w.Body.String(), `class="deckshare-card">`); got != 2 || strings.Contains(w.Body.String(), `class="deckshare-card" data-theme`) {
		t.Errorf("unthemed card count = %d, want 2: %s", got, w.Body.String())
	}
}

// accentOf reads the stored accent colour (#267) for a user id.
func accentOf(t *testing.T, tx pgx.Tx, id string) string {
	t.Helper()
	var s string
	if err := tx.QueryRow(context.Background(), `SELECT accent FROM users WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read accent: %v", err)
	}
	return s
}

func TestSettingsAccent_GoldenPath(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	id := userID(t, context.Background(), tx, email)

	if got := accentOf(t, tx, id); got != "azure" {
		t.Fatalf("default accent = %q, want azure", got)
	}
	body := doRequest(handler, "GET", "/decks", "", cookie, "").Body.String()
	if strings.Contains(body, "data-accent") {
		t.Error("the default accent must render no data-accent, so existing users are unchanged")
	}

	w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent=purple", cookie, "http://example.com")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	const wantTag = `<html lang="en" data-theme="dark" data-accent="purple">`
	if !strings.Contains(w.Body.String(), wantTag) {
		t.Errorf("response missing %s", wantTag)
	}
	if got := accentOf(t, tx, id); got != "purple" {
		t.Errorf("stored accent = %q, want purple", got)
	}
	if !strings.Contains(doRequest(handler, "GET", "/decks", "", cookie, "").Body.String(), wantTag) {
		t.Error("the next page load should render the stored accent")
	}

	// Choosing the default again clears the attribute.
	w = doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent=azure", cookie, "http://example.com")
	if !strings.Contains(w.Body.String(), `<html lang="en" data-theme="dark">`) {
		t.Error("azure should render no data-accent on <html>")
	}
}

func TestSettingsAccent_RejectsUnknownValue(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	id := userID(t, context.Background(), tx, email)

	for _, v := range []string{"magenta", "", "Blue", "%20blue", `blue%22%20onload%3D`} {
		w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&accent="+v, cookie, "http://example.com")
		if w.Code != 400 {
			t.Errorf("accent %q: status = %d, want 400", v, w.Code)
		}
		if got := accentOf(t, tx, id); got != "azure" {
			t.Errorf("accent %q: stored accent = %q, want unchanged azure", v, got)
		}
		if got := colorSchemeOf(t, tx, id); got != "auto" {
			t.Errorf("accent %q: color_scheme = %q, want unchanged auto (no partial write)", v, got)
		}
	}
}

func TestSettingsAccent_OnlyCallerRow(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ctx := context.Background()
	emailA, emailB := testEmail(), testEmail()
	cookieA := loginCookie(t, tx, a, emailA, "correct-horse-battery")
	loginCookie(t, tx, a, emailB, "correct-horse-battery")
	idA, idB := userID(t, ctx, tx, emailA), userID(t, ctx, tx, emailB)

	doRequest(handler, "POST", "/settings/appearance", "color_scheme=auto&accent=red&user_id="+idB+"&id="+idB, cookieA, "http://example.com")
	if got := accentOf(t, tx, idA); got != "red" {
		t.Errorf("caller's accent = %q, want red", got)
	}
	if got := accentOf(t, tx, idB); got != "azure" {
		t.Errorf("other user's accent = %q, want azure", got)
	}
}

func TestSettingsPage_AccentRadiosReflectStoredValue(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	body := doRequest(handler, "GET", "/settings", "", cookie, "").Body.String()
	if !strings.Contains(body, `value="azure" checked`) {
		t.Error("a fresh user should see Azure selected")
	}
	doRequest(handler, "POST", "/settings/appearance", "color_scheme=auto&accent=green", cookie, "http://example.com")
	body = doRequest(handler, "GET", "/settings", "", cookie, "").Body.String()
	if !strings.Contains(body, `value="green" checked`) || strings.Contains(body, `value="azure" checked`) {
		t.Error("after choosing Green, only Green should be selected")
	}
}

// Every offered accent must have a generated override block, or choosing it silently renders
// Pico's default colour.
func TestAccentOptions_HaveGeneratedCSS(t *testing.T) {
	css, err := os.ReadFile("../../web/static/accents.css")
	if err != nil {
		t.Fatalf("read accents.css: %v", err)
	}
	for _, o := range accentOptions {
		if o.Value == "azure" {
			continue // Pico's default: no override block
		}
		if want := `:root[data-accent="` + o.Value + `"]:not([data-theme=dark])`; !strings.Contains(string(css), want) {
			t.Errorf("accents.css has no light block for %q", o.Value)
		}
	}
}

// The live preview (#267) needs the form id it binds to and its same-origin script (CSP script-src 'self').
func TestSettingsPage_LoadsAppearancePreview(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	body := doRequest(handler, "GET", "/settings", "", cookie, "").Body.String()
	for _, want := range []string{`id="appearance-form"`, `src="/static/settings_appearance.js"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %s", want)
		}
	}
	if w := doRequest(handler, "GET", "/static/settings_appearance.js", "", nil, ""); w.Code != 200 {
		t.Errorf("GET settings_appearance.js status = %d, want 200", w.Code)
	}
}
