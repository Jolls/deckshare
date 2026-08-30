package db

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// ComputeNoteChecksum is Anki's own csum algorithm: truncated SHA-1 of a note's first field with
// HTML tags stripped. Shared by internal/http/notes.go's validateNoteFields (ordinary note
// create/edit) and UpdateNoteType's bulk recompute (#89, when a field remap changes which field is
// now first).
func ComputeNoteChecksum(firstField string) int64 {
	stripped := htmlTagRe.ReplaceAllString(firstField, "")
	sum := sha1.Sum([]byte(stripped)) //nolint:gosec // Anki csum compatibility, not a security use of SHA-1
	return int64(binary.BigEndian.Uint32(sum[:4]))
}

// CreateNoteWithCards creates a note and generates its cards in one transaction. Must be called
// inside a transaction it does not own; the caller commits.
func CreateNoteWithCards(ctx context.Context, tx pgx.Tx, arg CreateNoteParams, desired []DesiredCard) (Note, error) {
	q := New(tx)
	note, err := q.CreateNote(ctx, arg)
	if err != nil {
		return Note{}, err
	}
	if err := SyncNoteCards(ctx, tx, note.ID, note.DeckID, desired); err != nil {
		return Note{}, err
	}
	return note, nil
}

// UpdateNoteWithCards locks the note (authorising the caller), updates its content -- including
// its note type, e.g. for a #138 note-type change -- and syncs its cards to desired. Must be
// called inside a transaction it does not own; the caller commits. Returns pgx.ErrNoRows if the
// note is absent, invisible, or the caller lacks can_edit_content.
func UpdateNoteWithCards(ctx context.Context, tx pgx.Tx, userID, noteID, noteTypeID pgtype.UUID, fields []byte, tags []string, checksum int64, desired []DesiredCard) error {
	q := New(tx)
	note, err := q.LockNoteForContentEdit(ctx, LockNoteForContentEditParams{
		UserID: userID,
		NoteID: noteID,
	})
	if err != nil {
		return err
	}
	if _, err := q.UpdateNoteContent(ctx, UpdateNoteContentParams{
		Fields:     fields,
		Tags:       tags,
		Checksum:   checksum,
		NoteTypeID: noteTypeID,
		NoteID:     noteID,
	}); err != nil {
		return err
	}
	return SyncNoteCards(ctx, tx, noteID, note.DeckID, desired)
}

// MoveNote changes a note's deck, re-syncing owner_id to the target deck's owner and moving any
// of the note's cards that were filed in its old home deck along with it. Must be called inside
// a transaction it does not own; the caller commits. Returns pgx.ErrNoRows when the caller lacks
// can_view + can_edit_content on either the source or the target deck -- the two cases are
// deliberately indistinguishable.
func MoveNote(ctx context.Context, tx pgx.Tx, userID, noteID, targetDeckID pgtype.UUID) error {
	q := New(tx)
	note, err := q.LockNoteForContentEdit(ctx, LockNoteForContentEditParams{
		UserID: userID,
		NoteID: noteID,
	})
	if err != nil {
		return err
	}
	sourceDeckID := note.DeckID

	n, err := q.MoveNoteToDeck(ctx, MoveNoteToDeckParams{
		NoteID:       noteID,
		TargetDeckID: targetDeckID,
		UserID:       userID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return pgx.ErrNoRows
	}

	if _, err := q.MoveNoteCardsFromDeck(ctx, MoveNoteCardsFromDeckParams{
		TargetDeckID: targetDeckID,
		NoteID:       noteID,
		SourceDeckID: sourceDeckID,
	}); err != nil {
		return err
	}
	return nil
}
