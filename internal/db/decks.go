package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// CreateDeckWithAccess creates a deck and grants its creator all six deck_access flags in one
// transaction (docs/schema.md: a deck's creator gets full access; a personal deck is the trivial
// case of that, not a separate code path). Must be called inside a transaction it does not own;
// the caller commits.
func CreateDeckWithAccess(ctx context.Context, tx pgx.Tx, ownerID pgtype.UUID, name, description string) (Deck, error) {
	q := New(tx)
	deck, err := q.CreateDeck(ctx, CreateDeckParams{
		OwnerID:     ownerID,
		Name:        name,
		Description: description,
	})
	if err != nil {
		return Deck{}, err
	}
	if err := q.GrantFullDeckAccess(ctx, GrantFullDeckAccessParams{
		DeckID: deck.ID,
		UserID: ownerID,
	}); err != nil {
		return Deck{}, err
	}
	return deck, nil
}
