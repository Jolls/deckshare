package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/db"
	"github.com/Jolls/deckshare/internal/fsrs"
)

// DefaultDesiredRetention matches go-fsrs's own default request retention; it is what a user with
// no user_fsrs_params row gets (architecture.md §11 step 10 defers fitting entirely).
const DefaultDesiredRetention = 0.9

// EffectiveParams resolves the (user, deck) override, else the user's global row, else library
// defaults. An empty params array means "use the library defaults" (migration 00012).
func EffectiveParams(ctx context.Context, q *db.Queries, userID, deckID pgtype.UUID) (fsrs.Params, error) {
	row, err := q.GetEffectiveFsrsParams(ctx, db.GetEffectiveFsrsParamsParams{UserID: userID, DeckID: deckID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fsrs.NewDefaultParams(DefaultDesiredRetention)
		}
		return fsrs.Params{}, err
	}
	return paramsFromRow(int(row.FsrsVersion), row.Params, row.DesiredRetention)
}

// EffectiveParamsForUsers is EffectiveParams for many users on one deck in a single query --
// callers that need every user's params up front (the instructor dashboard's per-page fold, #87)
// rather than one row at a time. A userID with neither a per-deck override nor a global row is
// not an error, same as EffectiveParams's pgx.ErrNoRows case -- it resolves to library defaults,
// so every requested userID is guaranteed a key in the result.
func EffectiveParamsForUsers(ctx context.Context, q *db.Queries, userIDs []pgtype.UUID, deckID pgtype.UUID) (map[pgtype.UUID]fsrs.Params, error) {
	rows, err := q.GetEffectiveFsrsParamsForUsers(ctx, db.GetEffectiveFsrsParamsForUsersParams{UserIds: userIDs, DeckID: deckID})
	if err != nil {
		return nil, err
	}
	byUser := make(map[pgtype.UUID]fsrs.Params, len(userIDs))
	for _, row := range rows {
		p, err := paramsFromRow(int(row.FsrsVersion), row.Params, row.DesiredRetention)
		if err != nil {
			return nil, err
		}
		byUser[row.UserID] = p
	}
	if len(byUser) < len(userIDs) {
		def, err := fsrs.NewDefaultParams(DefaultDesiredRetention)
		if err != nil {
			return nil, err
		}
		for _, id := range userIDs {
			if _, ok := byUser[id]; !ok {
				byUser[id] = def
			}
		}
	}
	return byUser, nil
}

// paramsFromRow decodes one user_fsrs_params row, shared by EffectiveParams and
// EffectiveParamsForUsers so the "empty array means library defaults" rule (migration 00012)
// lives in exactly one place.
func paramsFromRow(fsrsVersion int, params []byte, desiredRetention float64) (fsrs.Params, error) {
	var weights []float64
	if err := json.Unmarshal(params, &weights); err != nil {
		return fsrs.Params{}, fmt.Errorf("review: unmarshal user_fsrs_params.params: %w", err)
	}
	if len(weights) == 0 {
		return fsrs.NewDefaultParams(desiredRetention)
	}
	return fsrs.NewParams(fsrsVersion, weights, desiredRetention)
}

// ParamsCache memoizes EffectiveParams per deck for the lifetime of one call (a grade batch, an
// apkg import): both touch many cards but few decks, and EffectiveParams is a per-(user, deck)
// lookup, so resolving it once per deck instead of once per card avoids a redundant DB round trip
// per row. Not safe for concurrent use; each caller owns its own instance.
type ParamsCache struct {
	byDeck map[pgtype.UUID]fsrs.Params
}

func NewParamsCache() *ParamsCache {
	return &ParamsCache{byDeck: make(map[pgtype.UUID]fsrs.Params)}
}

func (c *ParamsCache) Get(ctx context.Context, q *db.Queries, userID, deckID pgtype.UUID) (fsrs.Params, error) {
	if p, ok := c.byDeck[deckID]; ok {
		return p, nil
	}
	p, err := EffectiveParams(ctx, q, userID, deckID)
	if err != nil {
		return fsrs.Params{}, err
	}
	c.byDeck[deckID] = p
	return p, nil
}
