package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
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

	var weights []float64
	if err := json.Unmarshal(row.Params, &weights); err != nil {
		return fsrs.Params{}, fmt.Errorf("review: unmarshal user_fsrs_params.params: %w", err)
	}
	if len(weights) == 0 {
		return fsrs.NewDefaultParams(row.DesiredRetention)
	}
	return fsrs.NewParams(int(row.FsrsVersion), weights, row.DesiredRetention)
}
