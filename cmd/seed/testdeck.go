package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jolls/deckshare/internal/db"
)

// The "Test Deck" is owned by Teacher A, shared view+study with the students, and with Teacher B as a full co-owner. Its
// cards are not real flashcards: each one's front names what it exercises and its back says what
// to expect, so a manual pass is "flip each card and compare". Today it covers dark-mode colour
// handling (#276); add cards here as other behaviours need eyeballing.
const (
	testDeckName          = "Test Deck"
	colourTestNoteType    = "Colour Test"
	colourTestClozeType   = "Colour Test Cloze"
	testDeckNewPerDay     = 100 // the other seeded decks cap at 2/day to exercise #172; this one must show every card
	testDeckRevPerDay     = 100
	colourTestNoteTypeCSS = baseCardCSS +
		".red { color: #c00; }\n" +
		".green { color: #080; }\n" +
		".grey { color: #888; }\n" +
		".faint { color: #ccc; }\n" +
		".rgbblack { color: rgb(0, 0, 0); }\n" +
		".hslgrey { color: hsl(0, 0%, 25%); }\n" +
		".outlined { border-width: 2px; border-style: solid; border-color: #000; padding: 8px; }\n" +
		".shorthandborder { border: 2px solid #000; padding: 8px; }\n" +
		".inverse { background-color: #000; color: #fff; padding: 8px; }\n" +
		".pale { background-color: #ffffe0; color: #000; padding: 8px; }\n" +
		".navybox { background-color: #003366; color: #fff; padding: 8px; }\n" +
		".tablehead { background-color: #ddd; color: #000; }\n"
	colourTestClozeCSS = baseCardCSS + ".cloze {\n    font-weight: bold;\n    color: blue;\n}\n"
)

// colourTestSamples are {front, back}. Fields go through the card-HTML sanitiser at render time,
// so only allowlisted markup (class, inline style colours, table) is used.
var colourTestSamples = [][2]string{
	{
		"Default .card rule: black text on a white surface",
		"Dark: light text on a dark surface. Light: unchanged.",
	},
	{
		`Chromatic text via class: <span class="red">red</span> and <span class="green">green</span>`,
		"Should stay red and green in both modes, and be readable on the dark surface.",
	},
	{
		`Greys via class: <span class="grey">#888 grey</span> and <span class="faint">#ccc faint grey</span>`,
		"Dark: both flip but keep their order (faint stays fainter than the main text) and stay visible.",
	},
	{
		`Colour functions: <span class="rgbblack">rgb(0, 0, 0)</span> and <span class="hslgrey">hsl grey 25%</span>`,
		"Both are greys, so both should flip to light text in dark mode.",
	},
	{
		`<span class="outlined">border-color longhand (#000)</span>`,
		"Dark: the border flips to a light colour. Light: black border.",
	},
	{
		`<span class="inverse">White text on a black block</span>`,
		"Both are greys, so the block flips too: dark text on a light block in dark mode. Legible either way.",
	},
	{
		`<table><tr><th class="tablehead">Grey header cell</th><td>body cell</td></tr></table>`,
		"Grey header background and black text are both greys, so both flip. Header should stay legible.",
	},
	{
		`KNOWN GAP: <span class="shorthandborder">border shorthand (2px solid #000)</span>`,
		"Shorthands are not rewritten: the black border stays black, nearly invisible on the dark surface.",
	},
	{
		`KNOWN GAP: <span class="pale">black text on a pale yellow background</span>`,
		"The pale yellow background is chromatic so it stays bright, but the black text flips to light: expect near-illegible text in dark mode.",
	},
	{
		`KNOWN GAP: <span class="navybox">white text on a navy background</span>`,
		"The reverse of the previous card: the navy stays, the white text flips dark. Expect dark-on-dark in dark mode.",
	},
	{
		`KNOWN GAP: <span style="color:#000">inline style color:#000</span>`,
		"Inline style attributes are not rewritten: the text stays black on the dark surface.",
	},
}

// colourTestClozeSamples: a cloze's back is its front (Afmt is {{cloze:Text}}), so the text
// itself says what to expect.
var colourTestClozeSamples = [][2]string{
	{"Cloze CSS sets bold blue: {{c1::this should stay blue}}. Check it is readable on the dark surface.", ""},
	{"Ordinary text keeps the default colour; only {{c1::the deletion}} is blue and bold.", ""},
}

// seedTestDeck creates the Test Deck and its two note types for owner, shares it with every
// other seeded user, and fills it on first run. Safe to re-run: existing note types, grants and
// a non-empty deck are left alone.
func seedTestDeck(ctx context.Context, pool *pgxpool.Pool, owner, admin db.User, viewers []db.User) error {
	if err := ensureDeck(ctx, pool, owner.ID, testDeckName); err != nil {
		return fmt.Errorf("ensure deck %q: %w", testDeckName, err)
	}
	q := db.New(pool)
	decks, err := q.ListDecksForUser(ctx, owner.ID)
	if err != nil {
		return fmt.Errorf("list owner decks: %w", err)
	}
	deck, ok := findDeck(decks, testDeckName)
	if !ok {
		return fmt.Errorf("deck %q not found after ensureDeck", testDeckName)
	}

	basicType, err := ensureNoteType(ctx, pool, owner.ID, colourTestNoteType, colourTestNoteTypeCSS, false,
		[]string{"Front", "Back"},
		[]db.TemplateEdit{{Name: "Card 1", Qfmt: "{{Front}}", Afmt: "{{FrontSide}}<hr>{{Back}}"}})
	if err != nil {
		return err
	}
	clozeType, err := ensureNoteType(ctx, pool, owner.ID, colourTestClozeType, colourTestClozeCSS, true,
		[]string{"Text", "Extra"},
		[]db.TemplateEdit{{Name: "Cloze", Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"}})
	if err != nil {
		return err
	}

	if deck.CardCount == 0 {
		if _, err := pool.Exec(ctx, `
			UPDATE decks SET preset = jsonb_build_object(
				'new', jsonb_build_object('perDay', $1::int),
				'rev', jsonb_build_object('perDay', $2::int)
			) WHERE id = $3`, testDeckNewPerDay, testDeckRevPerDay, deck.ID); err != nil {
			return fmt.Errorf("set %s preset: %w", testDeckName, err)
		}
		if err := seedSampleNotes(ctx, pool, owner.ID, deck.ID, basicType, "Basic", colourTestSamples); err != nil {
			return fmt.Errorf("seed %s basic notes: %w", testDeckName, err)
		}
		if err := seedSampleNotes(ctx, pool, owner.ID, deck.ID, clozeType, "Cloze", colourTestClozeSamples); err != nil {
			return fmt.Errorf("seed %s cloze notes: %w", testDeckName, err)
		}
		log.Printf("seeded notes in %s", testDeckName)
	} else {
		log.Printf("%s already has cards, skipping note seeding", testDeckName)
	}

	// admin gets every flag, as Teacher B does on Shared Classroom. An upsert, not a grant, so it
	// also lifts a row an earlier seed run created as view+study only.
	if _, err := pool.Exec(ctx, `
		INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content,
			can_edit_settings, can_manage_access, can_delete, can_view_progress, can_view_flags)
		VALUES ($1, $2, true, true, true, true, true, true, true, true)
		ON CONFLICT (deck_id, user_id) DO UPDATE SET can_view = true, can_study = true,
			can_edit_content = true, can_edit_settings = true, can_manage_access = true,
			can_delete = true, can_view_progress = true, can_view_flags = true`,
		deck.ID, admin.ID); err != nil {
		return fmt.Errorf("ensure admin access to %q: %w", testDeckName, err)
	}
	for _, u := range viewers {
		if err := ensureDeckAccess(ctx, pool, db.GrantDeckAccessParams{
			DeckID: deck.ID, CallerUserID: owner.ID, TargetUserID: u.ID,
			CanView: true, CanStudy: true,
		}); err != nil {
			return fmt.Errorf("ensure access to %q: %w", testDeckName, err)
		}
	}
	return nil
}

// ensureNoteType returns ownerID's note type called name, creating it with the given shape if
// it doesn't exist yet. An existing one is returned untouched, so hand edits survive a re-seed.
func ensureNoteType(ctx context.Context, pool *pgxpool.Pool, ownerID pgtype.UUID, name, css string, isCloze bool, fields []string, templates []db.TemplateEdit) (pgtype.UUID, error) {
	existing, err := db.New(pool).ListNoteTypesForUser(ctx, ownerID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("list note types: %w", err)
	}
	if nt, ok := findNoteType(existing, name); ok {
		return nt.ID, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	nt, err := db.CreateNoteTypeWithFieldsAndTemplates(ctx, tx, ownerID, name, css, isCloze, 0, fields, templates)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("create note type %q: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return pgtype.UUID{}, fmt.Errorf("commit: %w", err)
	}
	log.Printf("created note type: %s", name)
	return nt.ID, nil
}
