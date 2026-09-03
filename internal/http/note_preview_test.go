package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Jolls/deckshare/internal/auth"
)

// newClozeNoteTypeBody creates a single-field, single-template cloze note type, following the
// same shape notetypes_test.go's TestNoteTypeEdit_RejectsSecondTemplateOnClozeNoteType uses.
func newClozeNoteTypeBody(name string) string {
	v := url.Values{}
	v.Set("name", name)
	v.Set("css", "")
	v.Add("is_cloze", "on")
	v.Add("field_name[]", "Text")
	v.Add("template_name[]", "Cloze")
	v.Add("qfmt[]", "{{cloze:Text}}")
	v.Add("afmt[]", "{{cloze:Text}}")
	return v.Encode()
}

// schedulingRowCounts snapshots the row counts of every table a scheduling write would touch,
// so a test can assert a preview request left every one of them unchanged (invariants §2.5/§2.7).
type schedulingRowCounts struct {
	notes, cards, userCardState, reviewLog int64
}

// Counts are scoped to userID -- the user the calling test created -- rather than table-wide
// (CLAUDE.md §16). A table-wide count(*) would let any concurrently committing row move the
// total between the before/after snapshots under READ COMMITTED, failing the §2.5/§2.7
// assertion for a write this preview never made.
func countSchedulingRows(t *testing.T, tx pgx.Tx, userID string) schedulingRowCounts {
	t.Helper()
	return schedulingRowCounts{
		notes: countRows(t, tx, `SELECT count(*) FROM notes WHERE owner_id = $1`, userID),
		cards: countRows(t, tx,
			`SELECT count(*) FROM cards c JOIN decks d ON d.id = c.deck_id WHERE d.owner_id = $1`, userID),
		userCardState: countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE user_id = $1`, userID),
		reviewLog:     countRows(t, tx, `SELECT count(*) FROM review_log WHERE user_id = $1`, userID),
	}
}

// TestNotePreview_NonCloze_WritesNoSchedulingState is the load-bearing test for invariants
// §2.5/§2.7: a preview of a 2-template note type renders one card per template and leaves
// notes/cards/user_card_state/review_log completely untouched.
func TestNotePreview_NonCloze_WritesNoSchedulingState(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	ctx := context.Background()
	userID := userID(t, ctx, tx, email)

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	w := doRequest(handler, "POST", "/note-types", newReversedNoteTypeBody("Reversed2"), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create reversed note type status = %d: %s", w.Code, w.Body.String())
	}
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Reversed2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	before := countSchedulingRows(t, tx, userID)

	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", "Question text")
	body.Add("field[]", "Answer text")
	w = doRequest(handler, "POST", deckPath+"/notes/preview", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := strings.Count(w.Body.String(), `class="deckshare-card"`); got != 2 {
		t.Errorf("card count = %d, want 2 (one per template): %s", got, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Question text") || !strings.Contains(w.Body.String(), "Answer text") {
		t.Errorf("preview body missing field text: %s", w.Body.String())
	}

	after := countSchedulingRows(t, tx, userID)
	if after != before {
		t.Errorf("preview wrote scheduling state: before=%+v after=%+v", before, after)
	}
}

// TestNotePreview_Cloze_OneCardPerOrdinal covers the cloze branch of desiredCards: distinct
// {{cN::...}} markers become distinct cards, and no scheduling state is written.
func TestNotePreview_Cloze_OneCardPerOrdinal(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	ctx := context.Background()
	userID := userID(t, ctx, tx, email)

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	w = doRequest(handler, "POST", "/note-types", newClozeNoteTypeBody("Cloze3"), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create cloze note type status = %d: %s", w.Code, w.Body.String())
	}
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Cloze3'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	before := countSchedulingRows(t, tx, userID)

	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", "{{c1::first}} and {{c2::second}}")
	w = doRequest(handler, "POST", deckPath+"/notes/preview", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := strings.Count(w.Body.String(), `class="deckshare-card"`); got != 2 {
		t.Errorf("card count = %d, want 2 (one per cloze ordinal): %s", got, w.Body.String())
	}

	after := countSchedulingRows(t, tx, userID)
	if after != before {
		t.Errorf("preview wrote scheduling state: before=%+v after=%+v", before, after)
	}
}

// TestNotePreview_ClozeNoMarker_Returns200WithNotice is the "still typing" state: a cloze field
// with no {{cN::...}} marker yet is a normal, expected live-preview state, not a client error.
func TestNotePreview_ClozeNoMarker_Returns200WithNotice(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")
	doRequest(handler, "POST", "/note-types", newClozeNoteTypeBody("Cloze3"), cookie, "http://example.com")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Cloze3'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", "no marker here yet")
	w = doRequest(handler, "POST", deckPath+"/notes/preview", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "preview-notice") {
		t.Errorf("body missing notice: %s", w.Body.String())
	}
}

// TestNotePreview_EditForm_UsesUnsavedValues asserts the edit-form preview renders whatever is
// currently posted, not the saved note -- and that the saved note is provably untouched.
func TestNotePreview_EditForm_UsesUnsavedValues(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	createBody := url.Values{}
	createBody.Set("note_type_id", noteTypeID)
	createBody.Add("field[]", "OldFront")
	createBody.Add("field[]", "OldBack")
	w := doRequest(handler, "POST", deckPath+"/notes", createBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create note status = %d: %s", w.Code, w.Body.String())
	}
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, noteTypeID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}
	var savedFields string
	if err := tx.QueryRow(ctx, `SELECT fields::text FROM notes WHERE id = $1`, noteID).Scan(&savedFields); err != nil {
		t.Fatalf("lookup saved fields: %v", err)
	}

	previewBody := url.Values{}
	previewBody.Set("note_type_id", noteTypeID)
	previewBody.Add("field[]", "NewFront")
	previewBody.Add("field[]", "NewBack")
	w = doRequest(handler, "POST", "/notes/"+noteID+"/preview", previewBody.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "NewFront") {
		t.Errorf("preview body should contain unsaved value NewFront: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "OldFront") {
		t.Errorf("preview body should not contain saved value OldFront: %s", w.Body.String())
	}

	var fieldsAfter string
	if err := tx.QueryRow(ctx, `SELECT fields::text FROM notes WHERE id = $1`, noteID).Scan(&fieldsAfter); err != nil {
		t.Fatalf("lookup fields after preview: %v", err)
	}
	if fieldsAfter != savedFields {
		t.Errorf("preview changed stored fields: before=%q after=%q", savedFields, fieldsAfter)
	}
}

// TestNotePreview_FieldCountMismatch_Returns400 is the one hard-error case: a field[] count that
// doesn't match the note type's field count can't happen from the real form, only from a
// malformed/direct POST, and gets 400 -- with no partial write.
func TestNotePreview_FieldCountMismatch_Returns400(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	email := testEmail()
	cookie := loginCookie(t, tx, a, email, "correct-horse-battery")
	ctx := context.Background()
	userID := userID(t, ctx, tx, email)

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	before := countSchedulingRows(t, tx, userID)

	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", "OnlyOneField")
	w := doRequest(handler, "POST", deckPath+"/notes/preview", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}

	after := countSchedulingRows(t, tx, userID)
	if after != before {
		t.Errorf("failed preview wrote scheduling state: before=%+v after=%+v", before, after)
	}
}

// TestNotePreview_Media_ResolvesKnownFilenameLeavesUnknown covers the handler's media-resolver
// wiring (RewriteMediaSrcs itself is tested in internal/render): a filename with an existing
// media_refs row for the deck resolves to /media/{sha256}; an unregistered filename is left
// as-is.
func TestNotePreview_Media_ResolvesKnownFilenameLeavesUnknown(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, cookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var noteTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	data := []byte("fake-image-bytes")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(ctx, `INSERT INTO media_blobs (sha256, size_bytes, mime) VALUES ($1, $2, $3)`, sha, len(data), "image/jpeg"); err != nil {
		t.Fatalf("insert media_blobs: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_refs (deck_id, filename, sha256) VALUES ($1, $2, $3)`, deckID, "known.jpg", sha); err != nil {
		t.Fatalf("insert media_refs: %v", err)
	}

	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", `<img src="known.jpg">`)
	body.Add("field[]", `<img src="unknown.jpg">`)
	w := doRequest(handler, "POST", deckPath+"/notes/preview", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", w.Code, w.Body.String())
	}
	respBody := w.Body.String()
	if !strings.Contains(respBody, "/media/"+sha) {
		t.Errorf("known filename should resolve to /media/%s: %s", sha, respBody)
	}
	if !strings.Contains(respBody, `src="unknown.jpg"`) {
		t.Errorf("unknown filename should be left unresolved: %s", respBody)
	}
}

// TestNotePreview_CSSIsSanitised covers the handler calling SanitiseCSS (tested itself in
// internal/render): a disallowed property is stripped from the preview's <style> block.
func TestNotePreview_CSSIsSanitised(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	cookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	ctx := context.Background()

	w := doRequest(handler, "POST", "/decks", "name=D", cookie, "http://example.com")
	deckPath := w.Header().Get("Location")

	ntBody := url.Values{}
	ntBody.Set("name", "Basic2")
	ntBody.Set("css", ".card { position: fixed; color: red; }")
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
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&noteTypeID); err != nil {
		t.Fatalf("lookup note type: %v", err)
	}

	body := url.Values{}
	body.Set("note_type_id", noteTypeID)
	body.Add("field[]", "Q")
	body.Add("field[]", "A")
	w = doRequest(handler, "POST", deckPath+"/notes/preview", body.Encode(), cookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", w.Code, w.Body.String())
	}
	respBody := w.Body.String()
	if strings.Contains(respBody, "position") {
		t.Errorf("disallowed CSS property should be stripped: %s", respBody)
	}
	if !strings.Contains(respBody, "color") {
		t.Errorf("allowed CSS property should survive: %s", respBody)
	}
}

// TestNotePreview_SharedDeck_CollaboratorCanPreview is the regression test for the shared-deck
// preview bug: buildNotePreview used to resolve the note's *current* note type with
// GetNoteTypeForOwner, so a collaborator editing a note on someone else's deck always got
// pgx.ErrNoRows -> 404, even though Save on the same form worked. The note-type lookup now
// mirrors POST /notes/{id}/edit -- unscoped for the current note type, owner-scoped for a change.
func TestNotePreview_SharedDeck_CollaboratorCanPreview(t *testing.T) {
	tx := beginTx(t)
	handler, a := newTestHandler(t, tx, auth.Config{})
	ownerCookie := loginCookie(t, tx, a, testEmail(), "correct-horse-battery")
	collabEmail := testEmail()
	collabCookie := loginCookie(t, tx, a, collabEmail, "correct-horse-battery")
	ctx := context.Background()

	deckPath := setupDeckAndNoteType(t, handler, ownerCookie)
	deckID := strings.TrimPrefix(deckPath, "/decks/")
	var basicID string
	if err := tx.QueryRow(ctx, `SELECT id FROM note_types WHERE name = 'Basic2'`).Scan(&basicID); err != nil {
		t.Fatalf("lookup Basic2: %v", err)
	}
	noteBody := url.Values{}
	noteBody.Set("note_type_id", basicID)
	noteBody.Add("field[]", "Q")
	noteBody.Add("field[]", "A")
	doRequest(handler, "POST", deckPath+"/notes", noteBody.Encode(), ownerCookie, "http://example.com")
	var noteID string
	if err := tx.QueryRow(ctx, `SELECT id FROM notes WHERE note_type_id = $1`, basicID).Scan(&noteID); err != nil {
		t.Fatalf("lookup note: %v", err)
	}

	collabID := userID(t, ctx, tx, collabEmail)
	if _, err := tx.Exec(ctx,
		`INSERT INTO deck_access (deck_id, user_id, can_view, can_edit_content) VALUES ($1, $2, true, true)`,
		deckID, collabID,
	); err != nil {
		t.Fatalf("grant collaborator access: %v", err)
	}

	before := countSchedulingRows(t, tx, collabID)

	// The note type belongs to the deck owner, not the collaborator -- the exact case that 404'd.
	body := url.Values{}
	body.Set("note_type_id", basicID)
	body.Add("field[]", "collaborator's unsaved question")
	body.Add("field[]", "collaborator's unsaved answer")
	w := doRequest(handler, "POST", "/notes/"+noteID+"/preview", body.Encode(), collabCookie, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("collaborator preview status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "collaborator&#39;s unsaved question") {
		t.Errorf("preview body missing the posted unsaved text: %s", w.Body.String())
	}

	if after := countSchedulingRows(t, tx, collabID); after != before {
		t.Errorf("preview wrote scheduling state: before=%+v after=%+v", before, after)
	}
}
