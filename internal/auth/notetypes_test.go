package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/db"
)

func TestSignup_SeedsDefaultNoteTypes(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada Lovelace")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	q := db.New(tx)
	noteTypes, err := q.ListNoteTypesForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListNoteTypesForUser: %v", err)
	}
	if len(noteTypes) != 2 {
		t.Fatalf("got %d note types, want 2 (Basic, Cloze)", len(noteTypes))
	}

	basic, cloze := noteTypes[0], noteTypes[1]
	if basic.Name != "Basic" {
		t.Errorf("noteTypes[0].Name = %q, want %q", basic.Name, "Basic")
	}
	if basic.IsCloze {
		t.Error("Basic note type should not be marked is_cloze")
	}
	if cloze.Name != "Cloze" {
		t.Errorf("noteTypes[1].Name = %q, want %q", cloze.Name, "Cloze")
	}
	if !cloze.IsCloze {
		t.Error("Cloze note type should be marked is_cloze")
	}
	if !strings.Contains(basic.Css, ".card {") {
		t.Errorf("Basic css = %q, want a base .card rule", basic.Css)
	}
	if !strings.Contains(cloze.Css, ".card {") {
		t.Errorf("Cloze css = %q, want a base .card rule", cloze.Css)
	}
	if !strings.Contains(cloze.Css, ".cloze {") || !strings.Contains(cloze.Css, "font-weight: bold") {
		t.Errorf("Cloze css = %q, want a bold .cloze rule", cloze.Css)
	}

	basicFields, err := q.ListFieldsForNoteType(ctx, basic.ID)
	if err != nil {
		t.Fatalf("ListFieldsForNoteType(Basic): %v", err)
	}
	assertFieldNames(t, "Basic", basicFields, "Front", "Back")

	clozeFields, err := q.ListFieldsForNoteType(ctx, cloze.ID)
	if err != nil {
		t.Fatalf("ListFieldsForNoteType(Cloze): %v", err)
	}
	assertFieldNames(t, "Cloze", clozeFields, "Text", "Extra")

	basicTemplates, err := q.ListTemplatesForNoteType(ctx, basic.ID)
	if err != nil {
		t.Fatalf("ListTemplatesForNoteType(Basic): %v", err)
	}
	if len(basicTemplates) != 1 {
		t.Fatalf("Basic has %d templates, want 1", len(basicTemplates))
	}
	if got, want := basicTemplates[0].Qfmt, "{{Front}}"; got != want {
		t.Errorf("Basic qfmt = %q, want %q", got, want)
	}
	if got, want := basicTemplates[0].Afmt, "{{FrontSide}}<hr>{{Back}}"; got != want {
		t.Errorf("Basic afmt = %q, want %q", got, want)
	}

	clozeTemplates, err := q.ListTemplatesForNoteType(ctx, cloze.ID)
	if err != nil {
		t.Fatalf("ListTemplatesForNoteType(Cloze): %v", err)
	}
	if len(clozeTemplates) != 1 {
		t.Fatalf("Cloze has %d templates, want 1", len(clozeTemplates))
	}
	if got, want := clozeTemplates[0].Qfmt, "{{cloze:Text}}"; got != want {
		t.Errorf("Cloze qfmt = %q, want %q", got, want)
	}
	if got, want := clozeTemplates[0].Afmt, "{{cloze:Text}}"; got != want {
		t.Errorf("Cloze afmt = %q, want %q", got, want)
	}
}

func assertFieldNames(t *testing.T, noteType string, fields []db.Field, want ...string) {
	t.Helper()
	if len(fields) != len(want) {
		t.Fatalf("%s has %d fields, want %d", noteType, len(fields), len(want))
	}
	for i, f := range fields {
		if f.Name != want[i] {
			t.Errorf("%s field[%d].Name = %q, want %q", noteType, i, f.Name, want[i])
		}
	}
}

func TestSeedDefaultNoteTypes_ErrorIsReturned(t *testing.T) {
	tx := beginTx(t)
	s := newTestService(t, tx)
	ctx := context.Background()

	// Signup already seeds Basic/Cloze once; calling seedDefaultNoteTypes again for the same
	// user hits the (owner_id, name) unique constraint, proving the failure path Signup
	// swallows is real and well-formed.
	user, _, err := s.Signup(ctx, "1.2.3.4", testEmail(), "correct-horse-battery", "Ada Lovelace")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if err := s.seedDefaultNoteTypes(ctx, user.ID); err == nil {
		t.Error("seedDefaultNoteTypes should fail once Basic/Cloze already exist for this user")
	}
}
