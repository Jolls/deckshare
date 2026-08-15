package review

import (
	"context"
	"hash/fnv"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
)

// lockKey maps a (user, card) pair onto the single-bigint advisory-lock space Postgres requires.
// FNV-1a/64 over user_id's 16 bytes followed by card_id's 16 bytes, reinterpreted as a signed
// int64. Do NOT use UUID byte prefixes: these are UUIDv7, so the leading 48 bits are a timestamp
// and every user created in the same millisecond-range would share a key. A hash collision costs
// two unrelated pairs a shared lock -- needless serialisation, never a correctness bug. Nothing
// else in this codebase uses the bigint advisory space, so there is no cross-feature collision to
// reason about.
func lockKey(userID, cardID pgtype.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write(userID.Bytes[:])
	_, _ = h.Write(cardID.Bytes[:])
	return int64(h.Sum64())
}

// lockKeys returns the deduped, ascending lock keys for evs. Ascending *key* order, not
// (user,card) order, is what makes two overlapping batches deadlock-free (architecture.md §6):
// under a hash collision, tuple order and key order could otherwise disagree and a cycle becomes
// constructible. Sorting the keys makes it unconstructible by construction.
func lockKeys(userID pgtype.UUID, evs []Event) []int64 {
	seen := make(map[int64]bool, len(evs))
	keys := make([]int64, 0, len(evs))
	for _, ev := range evs {
		k := lockKey(userID, ev.CardID)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// acquireLocks takes every lock the batch needs, in ascending key order, before touching any row.
func acquireLocks(ctx context.Context, q *db.Queries, keys []int64) error {
	for _, k := range keys {
		if err := q.LockCardForGrade(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
