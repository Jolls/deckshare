package auth

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
)

// seedDefaultNoteTypes creates the stock Basic and Cloze note types for a newly signed-up user,
// the same two Anki ships out of the box -- so a new user can create a note immediately instead
// of hand-building a note type (and its qfmt/afmt) from scratch. Both are created in one
// transaction; callers decide how to handle a failure (Signup logs and continues rather than
// failing signup itself).
func (s *Service) seedDefaultNoteTypes(ctx context.Context, userID pgtype.UUID) error {
	tx, err := s.beginner.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const baseCardCSS = ".card {\n" +
		"    font-family: arial;\n" +
		"    font-size: 20px;\n" +
		"    text-align: center;\n" +
		"    color: black;\n" +
		"    background-color: white;\n" +
		"}\n"

	if _, err := db.CreateNoteTypeWithFieldsAndTemplates(ctx, tx, userID, "Basic", baseCardCSS, false, 0,
		[]string{"Front", "Back"},
		[]db.TemplateEdit{{Name: "Card 1", Qfmt: "{{Front}}", Afmt: "{{FrontSide}}<hr>{{Back}}"}},
	); err != nil {
		return err
	}
	if _, err := db.CreateNoteTypeWithFieldsAndTemplates(ctx, tx, userID, "Cloze", baseCardCSS+".cloze {\n    font-weight: bold;\n}\n", true, 0,
		[]string{"Text", "Extra"},
		[]db.TemplateEdit{{Name: "Cloze", Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"}},
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
