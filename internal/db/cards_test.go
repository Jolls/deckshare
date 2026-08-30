package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// The trap's regression test (docs/schema.md, #51 §0.4): editing a note must not drop and
// recreate its cards -- a surviving ordinal's card id, and therefore its user_card_state, must
// be untouched.
func TestSyncNoteCards_KeepsSurvivingOrdinalsUntouched(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)

	c1 := mustCard(t, tx, note, template, deck, 0)
	c2 := mustCard(t, tx, note, template, deck, 1)
	c3 := mustCard(t, tx, note, template, deck, 2)
	mustUserCardState(t, tx, user, c1)

	// Edit: keep ordinal 0 and 1, remove ordinal 2, add ordinal 3.
	desired := []DesiredCard{
		{Ordinal: 0, TemplateID: template},
		{Ordinal: 1, TemplateID: template},
		{Ordinal: 3, TemplateID: template},
	}
	if err := SyncNoteCards(ctx, tx, note, deck, desired); err != nil {
		t.Fatalf("SyncNoteCards: %v", err)
	}

	var gotID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE note_id = $1 AND ordinal = 0`, note).Scan(&gotID); err != nil {
		t.Fatalf("select ordinal 0: %v", err)
	}
	if gotID != c1 {
		t.Errorf("ordinal 0 card id changed: got %v, want %v (original)", gotID, c1)
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, c1) != 1 {
		t.Error("user_card_state for the surviving card should still exist")
	}
	if !rowExists(t, tx, "cards", c2) {
		t.Error("ordinal 1's original card should survive untouched")
	}
	if rowExists(t, tx, "cards", c3) {
		t.Error("removed ordinal 2's card should be gone")
	}
	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1 AND ordinal = 3`, note) != 1 {
		t.Error("new ordinal 3 card should have been created")
	}
}

// The concrete regression test for #138's "surviving ordinal, new note type" case: a card whose
// ordinal survives a note-type change must keep its id (and therefore its user_card_state/
// review_log) but have template_id repointed to the new note type's template at that ordinal --
// the study batch query reads qfmt/afmt from cards.template_id, not notes.note_type_id.
func TestSyncNoteCards_RepointsTemplateIDOnSurvivingOrdinal(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteTypeA := mustNoteType(t, tx, user)
	noteTypeB := mustNoteType(t, tx, user)
	templateA := mustTemplate(t, tx, noteTypeA, 0)
	templateB := mustTemplate(t, tx, noteTypeB, 0)
	note := mustNote(t, tx, user, noteTypeA, deck)

	card := mustCard(t, tx, note, templateA, deck, 0)

	desired := []DesiredCard{
		{Ordinal: 0, TemplateID: templateB},
	}
	if err := SyncNoteCards(ctx, tx, note, deck, desired); err != nil {
		t.Fatalf("SyncNoteCards: %v", err)
	}

	var gotID, gotTemplateID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id, template_id FROM cards WHERE note_id = $1 AND ordinal = 0`, note).Scan(&gotID, &gotTemplateID); err != nil {
		t.Fatalf("select ordinal 0: %v", err)
	}
	if gotID != card {
		t.Errorf("ordinal 0 card id changed: got %v, want %v (original)", gotID, card)
	}
	if gotTemplateID != templateB {
		t.Errorf("ordinal 0 card template_id = %v, want %v (new note type's template)", gotTemplateID, templateB)
	}
}

// The concrete regression test for the "card deletion vs. review_log" question docs/plans/
// 138-edit-note-type.md resolves as already-decided: SyncNoteCards already hard-deletes a card
// whose ordinal is dropped (e.g. a note-type change that removes a template), and that delete
// cascades away user_card_state but leaves review_log orphaned, not deleted.
func TestSyncNoteCards_RemovedOrdinal_ReviewLogSurvivesOrphaned(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)

	// A second, surviving ordinal so dropping ordinal 1 doesn't leave the note card-less
	// (SyncNoteCards would reject an empty desired set with ErrNoCards before touching anything).
	mustCard(t, tx, note, template, deck, 0)
	card := mustCard(t, tx, note, template, deck, 1)
	mustUserCardState(t, tx, user, card)
	reviewLogID := mustReviewLog(t, tx, user, card)

	desired := []DesiredCard{
		{Ordinal: 0, TemplateID: template},
	}
	if err := SyncNoteCards(ctx, tx, note, deck, desired); err != nil {
		t.Fatalf("SyncNoteCards: %v", err)
	}

	if rowExists(t, tx, "cards", card) {
		t.Error("removed ordinal's card should be gone")
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, card) != 0 {
		t.Error("user_card_state for the removed card should have cascaded away")
	}
	if countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1 AND card_id = $2`, reviewLogID, card) != 1 {
		t.Error("review_log row should survive, orphaned, with card_id unchanged")
	}
}

func TestSyncNoteCards_EmptyDesiredReturnsErrNoCards(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	note := mustNote(t, tx, user, noteType, deck)

	err := SyncNoteCards(ctx, tx, note, deck, nil)
	if !errors.Is(err, ErrNoCards) {
		t.Fatalf("SyncNoteCards error = %v, want ErrNoCards", err)
	}
}

// The concrete regression test for #89's "content identity must not flip on reorder" finding: a
// template swap must keep each card's id and template_id fixed and only move cards.ordinal to
// track the template's new position, so user_card_state/review_log stay attached to the same card
// and no card silently starts rendering a different template mid-schedule.
func TestRemapNoteTypeCards_ReorderKeepsCardIdentityAndRepointsOrdinal(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	templateA := mustTemplate(t, tx, noteType, 0)
	templateB := mustTemplate(t, tx, noteType, 1)
	note := mustNote(t, tx, user, noteType, deck)

	cardA := mustCard(t, tx, note, templateA, deck, 0)
	cardB := mustCard(t, tx, note, templateB, deck, 1)
	mustUserCardState(t, tx, user, cardA)
	reviewLogA := mustReviewLog(t, tx, user, cardA)

	changed := []TemplateOrdinalChange{
		{TemplateID: templateA, NewOrdinal: 1},
		{TemplateID: templateB, NewOrdinal: 0},
	}
	if err := RemapNoteTypeCards(ctx, tx, noteType, changed, nil, nil); err != nil {
		t.Fatalf("RemapNoteTypeCards: %v", err)
	}

	var gotOrdinal int32
	var gotTemplateID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT ordinal, template_id FROM cards WHERE id = $1`, cardA).Scan(&gotOrdinal, &gotTemplateID); err != nil {
		t.Fatalf("select cardA: %v", err)
	}
	if gotOrdinal != 1 {
		t.Errorf("cardA ordinal = %d, want 1", gotOrdinal)
	}
	if gotTemplateID != templateA {
		t.Errorf("cardA template_id changed: got %v, want %v (fixed identity)", gotTemplateID, templateA)
	}
	if err := tx.QueryRow(ctx, `SELECT ordinal, template_id FROM cards WHERE id = $1`, cardB).Scan(&gotOrdinal, &gotTemplateID); err != nil {
		t.Fatalf("select cardB: %v", err)
	}
	if gotOrdinal != 0 {
		t.Errorf("cardB ordinal = %d, want 0", gotOrdinal)
	}
	if gotTemplateID != templateB {
		t.Errorf("cardB template_id changed: got %v, want %v (fixed identity)", gotTemplateID, templateB)
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, cardA) != 1 {
		t.Error("cardA's user_card_state should survive the reorder untouched")
	}
	if countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1 AND card_id = $2`, reviewLogA, cardA) != 1 {
		t.Error("cardA's review_log should survive the reorder untouched")
	}
}

// Exercises the general-permutation path (stage-to-negative then finalize), not just a pairwise
// swap: three templates cyclically rotate ordinal 0->1->2->0.
func TestRemapNoteTypeCards_ThreeWayCyclicReorder(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	t0 := mustTemplate(t, tx, noteType, 0)
	t1 := mustTemplate(t, tx, noteType, 1)
	t2 := mustTemplate(t, tx, noteType, 2)
	note := mustNote(t, tx, user, noteType, deck)

	c0 := mustCard(t, tx, note, t0, deck, 0)
	c1 := mustCard(t, tx, note, t1, deck, 1)
	c2 := mustCard(t, tx, note, t2, deck, 2)

	changed := []TemplateOrdinalChange{
		{TemplateID: t0, NewOrdinal: 1},
		{TemplateID: t1, NewOrdinal: 2},
		{TemplateID: t2, NewOrdinal: 0},
	}
	if err := RemapNoteTypeCards(ctx, tx, noteType, changed, nil, nil); err != nil {
		t.Fatalf("RemapNoteTypeCards: %v", err)
	}

	wantOrdinal := map[pgtype.UUID]int32{c0: 1, c1: 2, c2: 0}
	for cardID, want := range wantOrdinal {
		var got int32
		if err := tx.QueryRow(ctx, `SELECT ordinal FROM cards WHERE id = $1`, cardID).Scan(&got); err != nil {
			t.Fatalf("select card %v: %v", cardID, err)
		}
		if got != want {
			t.Errorf("card %v ordinal = %d, want %d", cardID, got, want)
		}
	}
}

// The note-type-bulk version of #138's TestSyncNoteCards_RemovedOrdinal_ReviewLogSurvivesOrphaned,
// plus the RESTRICT-respecting ordering: a removed template's cards must be gone before the
// template row itself can be deleted.
func TestRemapNoteTypeCards_RemovedTemplate_CardsDeletedReviewLogOrphanedTemplateRowGone(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	templateC := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)

	card := mustCard(t, tx, note, templateC, deck, 0)
	mustUserCardState(t, tx, user, card)
	reviewLogID := mustReviewLog(t, tx, user, card)

	if err := RemapNoteTypeCards(ctx, tx, noteType, nil, []pgtype.UUID{templateC}, nil); err != nil {
		t.Fatalf("RemapNoteTypeCards: %v", err)
	}

	if rowExists(t, tx, "cards", card) {
		t.Error("removed template's card should be gone")
	}
	if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, card) != 0 {
		t.Error("user_card_state should have cascaded away")
	}
	if countRows(t, tx, `SELECT count(*) FROM review_log WHERE id = $1 AND card_id = $2`, reviewLogID, card) != 1 {
		t.Error("review_log should survive, orphaned, with card_id unchanged")
	}
	if rowExists(t, tx, "templates", templateC) {
		t.Error("the now-cardless template row should also be gone")
	}
}

// An added template must get a card for every existing note of the note type, fresh (no
// user_card_state), via the existing CreateCardsForNewTemplate.
func TestRemapNoteTypeCards_AddedTemplate_CreatesCardsForEveryExistingNote(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	note1 := mustNote(t, tx, user, noteType, deck)
	note2 := mustNote(t, tx, user, noteType, deck)

	newTemplate := mustTemplate(t, tx, noteType, 0)
	added := []AddedTemplate{{TemplateID: newTemplate, Ordinal: 0}}
	if err := RemapNoteTypeCards(ctx, tx, noteType, nil, nil, added); err != nil {
		t.Fatalf("RemapNoteTypeCards: %v", err)
	}

	for _, noteID := range []pgtype.UUID{note1, note2} {
		var cardID pgtype.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM cards WHERE note_id = $1 AND template_id = $2 AND ordinal = 0`, noteID, newTemplate).Scan(&cardID); err != nil {
			t.Fatalf("expected a new card for note %v: %v", noteID, err)
		}
		if countRows(t, tx, `SELECT count(*) FROM user_card_state WHERE card_id = $1`, cardID) != 0 {
			t.Error("new card should have no user_card_state -- New for everyone")
		}
	}
}

// A single RemapNoteTypeCards call must correctly cover every note of the note type at once, not
// just a single note -- the "not one round trip per note" property this function exists for.
func TestRemapNoteTypeCards_MultipleNotesRemappedTogether(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	templateA := mustTemplate(t, tx, noteType, 0)
	templateB := mustTemplate(t, tx, noteType, 1)

	type pair struct{ a, b pgtype.UUID }
	var cards []pair
	for i := 0; i < 3; i++ {
		note := mustNote(t, tx, user, noteType, deck)
		cards = append(cards, pair{
			a: mustCard(t, tx, note, templateA, deck, 0),
			b: mustCard(t, tx, note, templateB, deck, 1),
		})
	}

	changed := []TemplateOrdinalChange{
		{TemplateID: templateA, NewOrdinal: 1},
		{TemplateID: templateB, NewOrdinal: 0},
	}
	if err := RemapNoteTypeCards(ctx, tx, noteType, changed, nil, nil); err != nil {
		t.Fatalf("RemapNoteTypeCards: %v", err)
	}

	for _, p := range cards {
		var ordA, ordB int32
		if err := tx.QueryRow(ctx, `SELECT ordinal FROM cards WHERE id = $1`, p.a).Scan(&ordA); err != nil {
			t.Fatalf("select card a: %v", err)
		}
		if err := tx.QueryRow(ctx, `SELECT ordinal FROM cards WHERE id = $1`, p.b).Scan(&ordB); err != nil {
			t.Fatalf("select card b: %v", err)
		}
		if ordA != 1 || ordB != 0 {
			t.Errorf("got ordinals a=%d b=%d, want a=1 b=0", ordA, ordB)
		}
	}
}

func TestSyncNoteCards_SequentialSyncsAccumulate(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	template := mustTemplate(t, tx, noteType, 0)
	note := mustNote(t, tx, user, noteType, deck)
	mustCard(t, tx, note, template, deck, 0)

	nested := beginSavepoint(t, tx)
	if err := SyncNoteCards(ctx, nested, note, deck, []DesiredCard{
		{Ordinal: 0, TemplateID: template},
		{Ordinal: 1, TemplateID: template},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := nested.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if countRows(t, tx, `SELECT count(*) FROM cards WHERE note_id = $1 AND ordinal = 1`, note) != 1 {
		t.Fatal("newly created card should exist")
	}
}
