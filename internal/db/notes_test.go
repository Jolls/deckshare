package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// mustCardWithPosition is mustCard plus an explicit import_due_position (nil for a manually
// created card, which leaves it NULL).
func mustCardWithPosition(t *testing.T, tx pgx.Tx, noteID, templateID, deckID pgtype.UUID, ordinal int, importDuePosition *int32) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := tx.QueryRow(context.Background(),
		`INSERT INTO cards (note_id, template_id, ordinal, deck_id, import_due_position) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		noteID, templateID, ordinal, deckID, importDuePosition,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert card: %v", err)
	}
	return id
}

// notesInTeachingOrder is a small helper collecting every note ListNotesInDeck returns for deckID,
// paging through with the given page size so callers can exercise both "whole list in one page"
// and "several small pages" without duplicating the walk.
func notesInTeachingOrder(t *testing.T, q *Queries, userID, deckID pgtype.UUID, pageSize int32) []pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	atStart := true
	var cursorSortKey int64
	var cursorID pgtype.UUID
	var ids []pgtype.UUID
	seen := map[pgtype.UUID]bool{}
	for {
		rows, err := q.ListNotesInDeck(ctx, ListNotesInDeckParams{
			UserID: userID, DeckID: deckID,
			AtStart: atStart, CursorSortKey: cursorSortKey, CursorID: cursorID,
			LimitCount: pageSize,
		})
		if err != nil {
			t.Fatalf("ListNotesInDeck: %v", err)
		}
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatalf("ListNotesInDeck: duplicate note %v across pages", r.ID)
			}
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
		if int32(len(rows)) < pageSize {
			return ids
		}
		last := rows[len(rows)-1]
		atStart, cursorSortKey, cursorID = false, last.SortKey, last.ID
	}
}

// #90: the notes list sorts by teaching order -- (import_due_position, id) -- not modified_at, and
// a NULL position (a manually created card, or a note with no cards yet) sorts last. Notes are
// created in an order that disagrees with both the desired sort and with modified_at, so a test
// that accidentally sorted by creation order or by modified_at would fail.
func TestListNotesInDeck_TeachingOrder(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)

	pos := func(v int32) *int32 { return &v }

	noteNoCards := mustNote(t, tx, user, noteType, deck) // no cards at all: sort_key's MIN is NULL -> sentinel
	noteSecond := mustNote(t, tx, user, noteType, deck)
	mustCardWithPosition(t, tx, noteSecond, template, deck, 0, pos(20))
	noteManual := mustNote(t, tx, user, noteType, deck) // import_due_position NULL -> same sentinel as noteNoCards
	mustCardWithPosition(t, tx, noteManual, template, deck, 0, nil)
	noteFirst := mustNote(t, tx, user, noteType, deck)
	mustCardWithPosition(t, tx, noteFirst, template, deck, 0, pos(10))

	// Bump modified_at on the note that should sort FIRST to be the most recently edited --
	// if the query still consulted modified_at at all, this would move it last.
	if _, err := tx.Exec(ctx, `UPDATE notes SET modified_at = now() + interval '1 hour' WHERE id = $1`, noteFirst); err != nil {
		t.Fatalf("bump modified_at: %v", err)
	}

	q := New(tx)
	got := notesInTeachingOrder(t, q, user, deck, 200)
	want := []pgtype.UUID{noteFirst, noteSecond, noteNoCards, noteManual}
	if len(got) != len(want) {
		t.Fatalf("got %d notes, want %d: %v", len(got), len(want), got)
	}
	// noteNoCards and noteManual tie at the NULL sentinel; both possible orders between them
	// are fine (id tiebreak), but they must both sort after noteFirst/noteSecond.
	for i, id := range got[:2] {
		if id != want[i] {
			t.Errorf("got[%d] = %v, want %v", i, id, want[i])
		}
	}
}

// #90/#238: paging must survive an edit -- the cursor is a stable (import_due_position, id) key,
// not an offset that reshuffles when modified_at changes underneath it. Walking the list one row
// at a time must visit every note exactly once, in the same order a single unpaged fetch would.
func TestListNotesInDeck_KeysetCursorSurvivesEdit(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)

	pos := func(v int32) *int32 { return &v }
	var notes []pgtype.UUID
	for i, p := range []int32{40, 10, 30, 20, 50} {
		n := mustNote(t, tx, user, noteType, deck)
		mustCardWithPosition(t, tx, n, template, deck, i, pos(p))
		notes = append(notes, n)
	}
	// notes[] is in creation order (40,10,30,20,50); teaching order is 10,20,30,40,50.
	want := []pgtype.UUID{notes[1], notes[3], notes[2], notes[0], notes[4]}

	q := New(tx)
	singlePage := notesInTeachingOrder(t, q, user, deck, 200)
	if len(singlePage) != len(want) {
		t.Fatalf("single page: got %d notes, want %d", len(singlePage), len(want))
	}
	for i, id := range singlePage {
		if id != want[i] {
			t.Fatalf("single page[%d] = %v, want %v", i, id, want[i])
		}
	}

	// A bulk edit between pages mutates modified_at on the already-fetched note -- an
	// offset-based page would have reshuffled around this; the keyset cursor must not.
	if _, err := tx.Exec(ctx, `UPDATE notes SET modified_at = now() + interval '1 hour' WHERE id = $1`, notes[1]); err != nil {
		t.Fatalf("bump modified_at: %v", err)
	}
	pagedTwoAtATime := notesInTeachingOrder(t, q, user, deck, 2)
	if len(pagedTwoAtATime) != len(want) {
		t.Fatalf("paged: got %d notes, want %d", len(pagedTwoAtATime), len(want))
	}
	for i, id := range pagedTwoAtATime {
		if id != want[i] {
			t.Fatalf("paged[%d] = %v, want %v", i, id, want[i])
		}
	}
}

// Migration 00015's composite FK regression: notes.(deck_id, owner_id) must reference
// decks(id, owner_id) -- a note whose owner_id doesn't match its deck's owner is rejected at
// the database level, not just by convention (docs/schema.md, Identifiers).
func TestNotesOwnerMatchesDeck_RejectsMismatch(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	deckOwner := mustUser(t, tx)
	otherUser := mustUser(t, tx)
	deck := mustDeck(t, tx, deckOwner)
	noteType := mustNoteType(t, tx, deckOwner)

	_, err := tx.Exec(ctx,
		`INSERT INTO notes (guid, owner_id, note_type_id, deck_id, checksum) VALUES ('g', $1, $2, $3, 0)`,
		otherUser, noteType, deck,
	)
	if !isForeignKeyViolation(err) {
		t.Fatalf("insert with mismatched owner_id error = %v, want foreign_key_violation", err)
	}
}

// #138: UpdateNoteWithCards must be able to change a note's note_type_id, not just its
// fields/tags -- the one call site (internal/http/notes.go's POST /notes/{id}/edit) passes the
// target note type id for a note-type change and note.NoteTypeID unchanged otherwise.
func TestUpdateNoteWithCards_ChangesNoteType(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	mustDeckAccess(t, tx, deck, user, fullAccess())
	noteTypeA := mustNoteType(t, tx, user)
	noteTypeB := mustNoteType(t, tx, user)
	templateB := mustTemplate(t, tx, noteTypeB, 0)
	note := mustNote(t, tx, user, noteTypeA, deck)

	desired := []DesiredCard{{Ordinal: 0, TemplateID: templateB}}
	if err := UpdateNoteWithCards(ctx, tx, user, note, noteTypeB, []byte(`["a"]`), []string{}, 0, desired); err != nil {
		t.Fatalf("UpdateNoteWithCards: %v", err)
	}

	var gotNoteTypeID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT note_type_id FROM notes WHERE id = $1`, note).Scan(&gotNoteTypeID); err != nil {
		t.Fatalf("select note: %v", err)
	}
	if gotNoteTypeID != noteTypeB {
		t.Errorf("notes.note_type_id = %v, want %v", gotNoteTypeID, noteTypeB)
	}
}

func TestMoveNote_UpdatesOwnerAndCards(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	owner1 := mustUser(t, tx)
	owner2 := mustUser(t, tx)
	d1 := mustDeck(t, tx, owner1)
	d2 := mustDeck(t, tx, owner2)
	mustDeckAccess(t, tx, d1, owner1, fullAccess())
	mustDeckAccess(t, tx, d2, owner1, fullAccess())
	noteType := mustNoteType(t, tx, owner1)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, owner1, noteType, d1)
	card := mustCard(t, tx, note, template, d1, 0)

	if err := MoveNote(ctx, tx, owner1, note, d2); err != nil {
		t.Fatalf("MoveNote: %v", err)
	}

	var deckID, ownerID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT deck_id, owner_id FROM notes WHERE id = $1`, note).Scan(&deckID, &ownerID); err != nil {
		t.Fatalf("select note: %v", err)
	}
	if deckID != d2 {
		t.Errorf("note.deck_id = %v, want %v", deckID, d2)
	}
	if ownerID != owner2 {
		t.Errorf("note.owner_id = %v, want %v", ownerID, owner2)
	}
	var cardDeckID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT deck_id FROM cards WHERE id = $1`, card).Scan(&cardDeckID); err != nil {
		t.Fatalf("select card: %v", err)
	}
	if cardDeckID != d2 {
		t.Errorf("card.deck_id = %v, want %v", cardDeckID, d2)
	}
}

func TestMoveNote_DeniedWithoutTargetAccess(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	owner1 := mustUser(t, tx)
	owner2 := mustUser(t, tx)
	d1 := mustDeck(t, tx, owner1)
	d2 := mustDeck(t, tx, owner2)
	mustDeckAccess(t, tx, d1, owner1, fullAccess())
	noteType := mustNoteType(t, tx, owner1)
	note := mustNote(t, tx, owner1, noteType, d1)

	err := MoveNote(ctx, tx, owner1, note, d2)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("MoveNote error = %v, want pgx.ErrNoRows", err)
	}
}
