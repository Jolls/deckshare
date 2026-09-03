package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrLastAccessHolder is returned when an access change would leave a deck with no
// can_manage_access holder or no can_delete holder -- stranding it with nobody able to manage or
// delete it. The caller MUST roll back the transaction: the mutation has already been applied and
// this error is the only thing preventing it from committing.
var ErrLastAccessHolder = errors.New("db: deck would be left with no can_manage_access or can_delete holder")

// DeleteDeck deletes deckID on behalf of userID, in the order docs/schema.md's deletion policy
// requires. It must be called inside a transaction it does not own; the caller commits.
//
// Deleting a deck deletes the cards filed in it, and a note goes only when it has no cards left
// anywhere -- not merely because this was its home deck (architecture.md §20). Notes whose cards
// survive in other decks are re-homed instead.
//
// The post-condition -- no note is ever left with zero cards -- holds because the final DELETE
// can only cascade away cards whose deck_id is this deck, and every note whose cards were all in
// this deck was already removed in step 2.
//
// Returns pgx.ErrNoRows when the deck does not exist, is not visible to userID, or userID lacks
// can_delete. Those three are deliberately indistinguishable; handlers answer 404 for all of them.
func DeleteDeck(ctx context.Context, tx pgx.Tx, deckID, userID pgtype.UUID) error {
	q := New(tx)

	if _, err := q.LockDeckForDelete(ctx, LockDeckForDeleteParams{
		DeckID: deckID,
		UserID: userID,
	}); err != nil {
		return err
	}
	if _, err := q.DeleteNotesOrphanedByDeckDelete(ctx, deckID); err != nil {
		return fmt.Errorf("delete card-less notes: %w", err)
	}
	if _, err := q.RehomeNotesOffDeck(ctx, deckID); err != nil {
		return fmt.Errorf("re-home surviving notes: %w", err)
	}
	if _, err := q.DeleteDeckRow(ctx, deckID); err != nil {
		return fmt.Errorf("delete deck: %w", err)
	}
	return nil
}

// RevokeDeckAccess deletes targetUserID's deck_access row on behalf of userID, refusing to strand
// the deck. Must be called inside a transaction it does not own; on ErrLastAccessHolder the caller
// rolls back.
//
// A deck's sole member cannot revoke their own access -- they delete the deck instead. Ownership
// (decks.owner_id) is not a permission source and exempts nobody: the guard counts deck_access
// flag holders only (docs/schema.md).
func RevokeDeckAccess(ctx context.Context, tx pgx.Tx, deckID, userID, targetUserID pgtype.UUID) error {
	q := New(tx)

	if _, err := q.LockDeckForAccessChange(ctx, LockDeckForAccessChangeParams{
		DeckID: deckID,
		UserID: userID,
	}); err != nil {
		return err
	}
	n, err := q.DeleteDeckAccessRow(ctx, DeleteDeckAccessRowParams{
		DeckID:       deckID,
		TargetUserID: targetUserID,
	})
	if err != nil {
		return fmt.Errorf("revoke deck access: %w", err)
	}
	if n == 0 {
		return pgx.ErrNoRows
	}
	return assertDeckStillManageable(ctx, q, deckID)
}

// SetDeckAccess overwrites targetUserID's six permission flags on behalf of userID, refusing to
// strand the deck. Same transaction contract as RevokeDeckAccess.
func SetDeckAccess(ctx context.Context, tx pgx.Tx, userID pgtype.UUID, arg UpdateDeckAccessRowParams) error {
	q := New(tx)

	if _, err := q.LockDeckForAccessChange(ctx, LockDeckForAccessChangeParams{
		DeckID: arg.DeckID,
		UserID: userID,
	}); err != nil {
		return err
	}
	n, err := q.UpdateDeckAccessRow(ctx, arg)
	if err != nil {
		return fmt.Errorf("update deck access: %w", err)
	}
	if n == 0 {
		return pgx.ErrNoRows
	}
	return assertDeckStillManageable(ctx, q, arg.DeckID)
}

// ResetDeckProgress deletes targetUserID's user_card_state rows for deckID's cards on behalf of
// userID. Same authorize-and-lock contract as RevokeDeckAccess/SetDeckAccess, but resetting
// progress can never strand a deck, so there is no assertDeckStillManageable check and no error
// for zero rows deleted -- a collaborator who never studied this deck resets to a no-op.
// review_log is untouched (#189, CLAUDE.md §2.5).
func ResetDeckProgress(ctx context.Context, tx pgx.Tx, deckID, userID, targetUserID pgtype.UUID) error {
	q := New(tx)

	if _, err := q.LockDeckForAccessChange(ctx, LockDeckForAccessChangeParams{
		DeckID: deckID,
		UserID: userID,
	}); err != nil {
		return err
	}
	if _, err := q.DeleteUserCardStateForDeck(ctx, DeleteUserCardStateForDeckParams{
		DeckID:       deckID,
		TargetUserID: targetUserID,
	}); err != nil {
		return fmt.Errorf("reset deck progress: %w", err)
	}
	return nil
}

// assertDeckStillManageable runs after the mutation rather than before it: one check then covers
// revocation, downgrade, and any path added later, and there is no check-then-act window. The deck
// row lock taken by the callers is what makes it race-free -- without it two concurrent revocations
// each see the other's holder still present and both commit.
func assertDeckStillManageable(ctx context.Context, q *Queries, deckID pgtype.UUID) error {
	holders, err := q.CountDeckAccessHolders(ctx, deckID)
	if err != nil {
		return fmt.Errorf("count deck access holders: %w", err)
	}
	if holders.ManageCount == 0 || holders.DeleteCount == 0 {
		return ErrLastAccessHolder
	}
	return nil
}
