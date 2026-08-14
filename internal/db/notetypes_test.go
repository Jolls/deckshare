package db

import (
	"context"
	"errors"
	"testing"
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
