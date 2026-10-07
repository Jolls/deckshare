package http

import (
	"context"
	"net/http"
	"net/url"
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
			w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark", tt.cookie, "http://example.com")
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

	w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark", cookie, "")
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
		w := doRequest(handler, "POST", "/settings/appearance", "color_scheme="+tt.scheme, cookie, "http://example.com")
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

	w := doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark&user_id="+idB+"&id="+idB, cookieA, "http://example.com")
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
		w := doRequest(handler, "POST", "/settings/appearance", "color_scheme="+v, cookie, "http://example.com")
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

	doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark", cookie, "http://example.com")
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
	doRequest(handler, "POST", "/settings/appearance", "color_scheme=light", cookie, "http://example.com")
	body := doRequest(handler, "GET", "/settings", "", cookie, "").Body.String()
	if !strings.Contains(body, `value="light" checked`) || strings.Contains(body, `value="auto" checked`) {
		t.Error("after choosing Light, only Light should be selected")
	}
}

// Card content is authored for a light page, so the review stage is a light surface in every mode.
func TestReviewPage_CardStageIsLightSurface(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	deckID, _ := setupOneCard(t, tx, handler, cookie)
	doRequest(handler, "POST", "/settings/appearance", "color_scheme=dark", cookie, "http://example.com")

	body := doRequest(handler, "GET", "/decks/"+deckID+"/review", "", cookie, "").Body.String()
	if !strings.Contains(body, `<html lang="en" data-theme="dark">`) {
		t.Error("review page should render the user's dark scheme on <html>")
	}
	if !strings.Contains(body, `class="review-card deckshare-card" data-theme="light"`) {
		t.Error("#review-stage should carry data-theme=light so cards stay a light surface")
	}
}

func TestStudyAll_CardStageIsLightSurface(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	setupOneCard(t, tx, handler, cookie)

	body := doRequest(handler, "GET", "/study", "", cookie, "").Body.String()
	if !strings.Contains(body, `class="review-card deckshare-card" data-theme="light"`) {
		t.Error("#review-stage on /study should carry data-theme=light")
	}
}

func TestNotePreview_CardIsLightSurface(t *testing.T) {
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
	if got := strings.Count(w.Body.String(), `class="deckshare-card" data-theme="light"`); got != 2 {
		t.Errorf("light-surface card count = %d, want 2: %s", got, w.Body.String())
	}
}
