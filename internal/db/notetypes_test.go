package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Defense in depth alongside UpdateNoteType's equivalent check: any future caller of
// CreateNoteTypeWithFieldsAndTemplates that bypasses the HTTP handler's own guard must still be
// stopped, since a cloze note type with more than one template lets a later SyncNoteCards delete
// the second template's cards and discard their user_card_state/review_log.
func TestCreateNoteTypeWithFieldsAndTemplates_RejectsClozeWithMultipleTemplates(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	user := mustUser(t, tx)

	_, err := CreateNoteTypeWithFieldsAndTemplates(ctx, tx, user, "Cloze", "", true, 0,
		[]string{"Text"},
		[]TemplateEdit{
			{Name: "Cloze", Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"},
			{Name: "Cloze 2", Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"},
		},
	)
	if !errors.Is(err, ErrClozeNoteTypeSingleTemplate) {
		t.Fatalf("error = %v, want ErrClozeNoteTypeSingleTemplate", err)
	}
}

// #89: a submission that repeats the same existing field/template id at two positions must be
// rejected, not silently applied -- otherwise the two submitted rows resolve to the same
// underlying row and RemapNoteFields/FinalizeCardOrdinalsForTemplates would be asked to assign it
// two different final ordinals.
func TestPlanFieldsAndTemplates_RejectDuplicateSubmittedID(t *testing.T) {
	tx := beginTx(t)

	user := mustUser(t, tx)
	noteType := mustNoteType(t, tx, user)

	t.Run("fields", func(t *testing.T) {
		frontID := mustField(t, tx, noteType, 0, "Front")
		_, err := planFields([]Field{{ID: frontID, Ordinal: 0, Name: "Front"}}, []FieldEdit{
			{ID: frontID, Name: "Front"},
			{ID: frontID, Name: "Front Again"},
		})
		if !errors.Is(err, ErrFieldNotFound) {
			t.Fatalf("planFields error = %v, want ErrFieldNotFound", err)
		}
	})

	t.Run("templates", func(t *testing.T) {
		templateID := mustTemplate(t, tx, noteType, 0)
		_, err := planTemplates([]Template{{ID: templateID, Ordinal: 0, Name: "Card"}}, []TemplateEdit{
			{ID: templateID, Name: "Card"},
			{ID: templateID, Name: "Card Again"},
		})
		if !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("planTemplates error = %v, want ErrTemplateNotFound", err)
		}
	})
}

// #89: a combined reorder-and-removal must rewrite every affected note's fields array
// positionally, in bulk -- keeping Back/Front (swapped) and dropping Extra.
func TestApplyFieldPlan_ReorderAndRemoval_RemapsNotesFieldsPositionally(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	q := New(tx)

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	frontID := mustField(t, tx, noteType, 0, "Front")
	backID := mustField(t, tx, noteType, 1, "Back")
	_ = mustField(t, tx, noteType, 2, "Extra")

	note1 := mustNoteWithFields(t, tx, user, noteType, deck, []string{"F1", "B1", "E1"})
	note2 := mustNoteWithFields(t, tx, user, noteType, deck, []string{"F2", "B2", "E2"})

	existing, err := q.ListFieldsForNoteType(ctx, noteType)
	if err != nil {
		t.Fatalf("ListFieldsForNoteType: %v", err)
	}
	submitted := []FieldEdit{
		{ID: backID, Name: "Back"},
		{ID: frontID, Name: "Front"},
	}
	plan, err := planFields(existing, submitted)
	if err != nil {
		t.Fatalf("planFields: %v", err)
	}
	if err := applyFieldPlan(ctx, q, noteType, submitted, plan); err != nil {
		t.Fatalf("applyFieldPlan: %v", err)
	}

	got1 := noteFields(t, tx, note1)
	if len(got1) != 2 || got1[0] != "B1" || got1[1] != "F1" {
		t.Errorf("note1 fields = %v, want [B1 F1]", got1)
	}
	got2 := noteFields(t, tx, note2)
	if len(got2) != 2 || got2[0] != "B2" || got2[1] != "F2" {
		t.Errorf("note2 fields = %v, want [B2 F2]", got2)
	}
}

// #89: a pure rename (every field kept at its existing ordinal, nothing removed or added) must
// not touch any note row at all -- RemapNoteFields would otherwise rewrite every note's fields
// column to its own unchanged value and bump modified_at for no reason.
func TestApplyFieldPlan_PureRename_DoesNotTouchNoteRows(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	q := New(tx)

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	frontID := mustField(t, tx, noteType, 0, "Front")
	backID := mustField(t, tx, noteType, 1, "Back")
	note := mustNoteWithFields(t, tx, user, noteType, deck, []string{"F", "B"})

	var before time.Time
	if err := tx.QueryRow(ctx, `SELECT modified_at FROM notes WHERE id = $1`, note).Scan(&before); err != nil {
		t.Fatalf("select modified_at: %v", err)
	}

	existing, err := q.ListFieldsForNoteType(ctx, noteType)
	if err != nil {
		t.Fatalf("ListFieldsForNoteType: %v", err)
	}
	submitted := []FieldEdit{
		{ID: frontID, Name: "Front Renamed"},
		{ID: backID, Name: "Back"},
	}
	plan, err := planFields(existing, submitted)
	if err != nil {
		t.Fatalf("planFields: %v", err)
	}
	if !fieldPlanIsIdentity(plan) {
		t.Fatal("a pure rename should plan as identity")
	}
	if err := applyFieldPlan(ctx, q, noteType, submitted, plan); err != nil {
		t.Fatalf("applyFieldPlan: %v", err)
	}

	var after time.Time
	if err := tx.QueryRow(ctx, `SELECT modified_at FROM notes WHERE id = $1`, note).Scan(&after); err != nil {
		t.Fatalf("select modified_at: %v", err)
	}
	if !after.Equal(before) {
		t.Errorf("modified_at changed from %v to %v, want unchanged (no note row touched)", before, after)
	}
	if got := noteFields(t, tx, note); len(got) != 2 || got[0] != "F" || got[1] != "B" {
		t.Errorf("note fields = %v, want [F B] unchanged", got)
	}
}

// #89: appending a new field must leave every existing note's prior values untouched and add a
// trailing empty string for the new slot -- no note has ever held a value for it.
func TestApplyFieldPlan_AddedField_NewNotesGetEmptyString(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	q := New(tx)

	user := mustUser(t, tx)
	deck := mustDeck(t, tx, user)
	noteType := mustNoteType(t, tx, user)
	frontID := mustField(t, tx, noteType, 0, "Front")
	backID := mustField(t, tx, noteType, 1, "Back")

	note := mustNoteWithFields(t, tx, user, noteType, deck, []string{"F", "B"})

	existing, err := q.ListFieldsForNoteType(ctx, noteType)
	if err != nil {
		t.Fatalf("ListFieldsForNoteType: %v", err)
	}
	submitted := []FieldEdit{
		{ID: frontID, Name: "Front"},
		{ID: backID, Name: "Back"},
		{Name: "Extra"}, // new field, ID.Valid == false
	}
	plan, err := planFields(existing, submitted)
	if err != nil {
		t.Fatalf("planFields: %v", err)
	}
	if err := applyFieldPlan(ctx, q, noteType, submitted, plan); err != nil {
		t.Fatalf("applyFieldPlan: %v", err)
	}

	got := noteFields(t, tx, note)
	want := []string{"F", "B", ""}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("note fields = %v, want %v", got, want)
	}
}

// #89: notes.checksum (sha1-of-stripped-html of field 0) must be recomputed only when field 0's
// identity actually changes -- not on every field edit.
func TestApplyFieldPlan_RecomputesChecksumOnlyWhenField0Changes(t *testing.T) {
	t.Run("field 0 changes", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		q := New(tx)

		user := mustUser(t, tx)
		deck := mustDeck(t, tx, user)
		noteType := mustNoteType(t, tx, user)
		frontID := mustField(t, tx, noteType, 0, "Front")
		backID := mustField(t, tx, noteType, 1, "Back")
		note := mustNoteWithFields(t, tx, user, noteType, deck, []string{"F", "B"})

		existing, err := q.ListFieldsForNoteType(ctx, noteType)
		if err != nil {
			t.Fatalf("ListFieldsForNoteType: %v", err)
		}
		// Swap: field 0 becomes Back's content.
		submitted := []FieldEdit{
			{ID: backID, Name: "Back"},
			{ID: frontID, Name: "Front"},
		}
		plan, err := planFields(existing, submitted)
		if err != nil {
			t.Fatalf("planFields: %v", err)
		}
		if err := applyFieldPlan(ctx, q, noteType, submitted, plan); err != nil {
			t.Fatalf("applyFieldPlan: %v", err)
		}

		var got int64
		if err := tx.QueryRow(ctx, `SELECT checksum FROM notes WHERE id = $1`, note).Scan(&got); err != nil {
			t.Fatalf("select checksum: %v", err)
		}
		want := ComputeNoteChecksum("B")
		if got != want {
			t.Errorf("checksum = %d, want %d (ComputeNoteChecksum of new field 0 'B')", got, want)
		}
	})

	t.Run("field 0 unchanged", func(t *testing.T) {
		tx := beginTx(t)
		ctx := context.Background()
		q := New(tx)

		user := mustUser(t, tx)
		deck := mustDeck(t, tx, user)
		noteType := mustNoteType(t, tx, user)
		frontID := mustField(t, tx, noteType, 0, "Front")
		backID := mustField(t, tx, noteType, 1, "Back")
		note := mustNoteWithFields(t, tx, user, noteType, deck, []string{"F", "B"})

		var before int64
		if err := tx.QueryRow(ctx, `SELECT checksum FROM notes WHERE id = $1`, note).Scan(&before); err != nil {
			t.Fatalf("select checksum: %v", err)
		}

		existing, err := q.ListFieldsForNoteType(ctx, noteType)
		if err != nil {
			t.Fatalf("ListFieldsForNoteType: %v", err)
		}
		// Rename only; field 0 stays at ordinal 0.
		submitted := []FieldEdit{
			{ID: frontID, Name: "Front Renamed"},
			{ID: backID, Name: "Back"},
		}
		plan, err := planFields(existing, submitted)
		if err != nil {
			t.Fatalf("planFields: %v", err)
		}
		if err := applyFieldPlan(ctx, q, noteType, submitted, plan); err != nil {
			t.Fatalf("applyFieldPlan: %v", err)
		}

		var after int64
		if err := tx.QueryRow(ctx, `SELECT checksum FROM notes WHERE id = $1`, note).Scan(&after); err != nil {
			t.Fatalf("select checksum: %v", err)
		}
		if after != before {
			t.Errorf("checksum changed from %d to %d, want unchanged (field 0 identity didn't move)", before, after)
		}
	})
}
