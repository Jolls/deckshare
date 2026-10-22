package http

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/apkg"
	"github.com/Jolls/deckshare/internal/auth"
)

func serveLimited(slots chan struct{}, next http.HandlerFunc) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	concurrencyLimit(slots, next).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	return w
}

func TestConcurrencyLimit_FullReturns503WithRetryAfter(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	called := false
	w := serveLimited(slots, func(http.ResponseWriter, *http.Request) { called = true })

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if called {
		t.Error("next ran with every slot taken")
	}
	if len(slots) != 1 {
		t.Errorf("len(slots) = %d, want 1 (a rejected request must not touch the slot it never held)", len(slots))
	}
}

func TestConcurrencyLimit_ReleasesOnCompletion(t *testing.T) {
	slots := make(chan struct{}, 1)
	heldWhileRunning := -1
	w := serveLimited(slots, func(w http.ResponseWriter, _ *http.Request) {
		heldWhileRunning = len(slots)
		w.WriteHeader(http.StatusOK)
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if heldWhileRunning != 1 {
		t.Errorf("slots held while next ran = %d, want 1", heldWhileRunning)
	}
	if len(slots) != 0 {
		t.Errorf("len(slots) = %d after completion, want 0", len(slots))
	}
	if w := serveLimited(slots, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }); w.Code != http.StatusOK {
		t.Errorf("second request status = %d, want 200", w.Code)
	}
}

func TestConcurrencyLimit_ReleasesOnPanic(t *testing.T) {
	slots := make(chan struct{}, 1)
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		serveLimited(slots, func(http.ResponseWriter, *http.Request) { panic("boom") })
	}()
	if !panicked {
		t.Fatal("the handler's panic did not propagate")
	}
	if len(slots) != 0 {
		t.Errorf("len(slots) = %d after a panic, want 0", len(slots))
	}
}

func TestCappedBuffer_RefusesPastMax(t *testing.T) {
	c := cappedBuffer{max: 4}
	if n, err := c.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("first Write = (%d, %v), want (3, nil)", n, err)
	}
	n, err := c.Write([]byte("de"))
	if !errors.Is(err, errExportTooLarge) || n != 0 {
		t.Errorf("second Write = (%d, %v), want (0, errExportTooLarge)", n, err)
	}
	if c.Len() != 3 {
		t.Errorf("Len = %d, want 3 (the refused write must not land)", c.Len())
	}
}

// The sentinel has to survive apkg.Write's error wrapping, or the export route cannot tell an
// oversize package from any other failure.
func TestExportWrite_CappedBufferSurfacesSentinel(t *testing.T) {
	col, err := apkg.ReadFile(mathFixture, apkg.DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	buf := cappedBuffer{max: 1024}
	if err := apkg.Write(col, &buf); !errors.Is(err, errExportTooLarge) {
		t.Errorf("apkg.Write into a 1 KiB cap = %v, want errExportTooLarge", err)
	}
}

func fullSlots() chan struct{} {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	return slots
}

func readMathFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(mathFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// assertBusy checks the response a request gets when every slot is taken, and that no package
// went out with it.
func assertBusy(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if !strings.Contains(w.Body.String(), "busy") {
		t.Errorf("response body (%d bytes) does not contain a 'busy' message", w.Body.Len())
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want none -- nothing should have run", loc)
	}
}

func TestImportRoute_SlotsFull_503(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandlerWithSlots(t, tx, auth.Config{}, fullSlots())
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	assertBusy(t, doUploadRequest(t, handler, "/import", "file", "m.apkg", readMathFixture(t), cookie, "http://example.com"))
}

func TestImportRoute_ReleasesSlotOnEarlyReturn(t *testing.T) {
	tx := beginTx(t)
	slots := make(chan struct{}, 1)
	handler, a := newTestHandlerWithSlots(t, tx, auth.Config{}, slots)
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doUploadRequest(t, handler, "/import", "file", "x.apkg", []byte("not a zip file"), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(slots) != 0 {
		t.Fatalf("len(slots) = %d after a 400, want 0", len(slots))
	}
	if w := doUploadRequest(t, handler, "/import", "file", "m.apkg", readMathFixture(t), cookie, "http://example.com"); w.Code != http.StatusSeeOther {
		t.Errorf("import after the 400 = %d, want 303", w.Code)
	}
}

func TestExportRoute_ReleasesSlotOnNotFound(t *testing.T) {
	tx := beginTx(t)
	slots := make(chan struct{}, 1)
	handler, a := newTestHandlerWithSlots(t, tx, auth.Config{}, slots)
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	strangerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	w := doRequest(handler, "POST", "/decks", "name=Owner Deck", ownerCookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	for name, tc := range map[string]struct {
		path   string
		cookie *http.Cookie
	}{
		"malformed id":        {"/decks/not-a-uuid/export", ownerCookie},
		"deck without access": {deckPath + "/export", strangerCookie},
	} {
		w := doRequest(handler, "GET", tc.path, "", tc.cookie, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, w.Code)
		}
		if len(slots) != 0 {
			t.Errorf("%s: len(slots) = %d after a 404, want 0", name, len(slots))
		}
	}
}

func TestSlotsFull_UnrelatedRouteUnaffected(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandlerWithSlots(t, tx, auth.Config{}, fullSlots())
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	for _, path := range []string{"/decks", "/import"} {
		if w := doRequest(handler, "GET", path, "", cookie, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s with every slot taken = %d, want 200", path, w.Code)
		}
	}
}

// Import and export draw on one pool of slots: memory is the resource being bounded, so the total
// number of whole packages in flight is what matters, not how many of each.
func TestImportExport_ShareSlots(t *testing.T) {
	tx := beginTx(t)
	slots := make(chan struct{}, 1)
	handler, a := newTestHandlerWithSlots(t, tx, auth.Config{}, slots)
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	w := doUploadRequest(t, handler, "/import", "file", "m.apkg", readMathFixture(t), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("setup import status = %d, want 303", w.Code)
	}
	deckPath := w.Header().Get("Location")

	slots <- struct{}{} // an import in flight
	w = doRequest(handler, "GET", deckPath+"/export", "", cookie, "")
	assertBusy(t, w)
	if bytes.HasPrefix(w.Body.Bytes(), []byte("PK\x03\x04")) {
		t.Error("a package body was sent with every slot taken")
	}
}

// The slot is taken only once the upload has been read: a client trickling its body (the server
// deliberately sets no ReadTimeout, so large uploads can finish) must not hold one of the few
// slots while it does. A body that is not multipart fails at the parse, before any slot is needed,
// so it answers 400 even with every slot taken.
func TestImportRoute_SlotNotTakenBeforeBodyRead(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandlerWithSlots(t, tx, auth.Config{}, fullSlots())
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")

	w := doRequest(handler, "POST", "/import", "not multipart", cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (the parse fails before a slot is needed)", w.Code)
	}
}
