package review

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func testUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("scan uuid %q: %v", s, err)
	}
	return u
}

func TestLockKeys_AscendingAndDeduped(t *testing.T) {
	userID := testUUID(t, "00000000-0000-7000-8000-000000000001")
	cardA := testUUID(t, "00000000-0000-7000-8000-0000000000aa")
	cardB := testUUID(t, "00000000-0000-7000-8000-0000000000bb")
	cardC := testUUID(t, "00000000-0000-7000-8000-0000000000cc")

	// Unsorted, with card A repeated (two events grading the same card in one batch).
	evs := []Event{{CardID: cardB}, {CardID: cardA}, {CardID: cardC}, {CardID: cardA}}
	keys := lockKeys(userID, evs)

	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d, want 3 (deduped)", len(keys))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Errorf("keys not strictly ascending at index %d: %v", i, keys)
		}
	}
}

// Two overlapping batches that list their shared cards in opposite order must still acquire
// locks in the same order -- the deadlock-freedom property (architecture.md §6), verified here
// without a database.
func TestLockKeys_OverlappingBatchesConverge(t *testing.T) {
	userID := testUUID(t, "00000000-0000-7000-8000-000000000001")
	cardA := testUUID(t, "00000000-0000-7000-8000-0000000000aa")
	cardB := testUUID(t, "00000000-0000-7000-8000-0000000000bb")

	batch1 := lockKeys(userID, []Event{{CardID: cardA}, {CardID: cardB}})
	batch2 := lockKeys(userID, []Event{{CardID: cardB}, {CardID: cardA}})

	if len(batch1) != 2 || len(batch2) != 2 {
		t.Fatalf("expected 2 keys each, got %d and %d", len(batch1), len(batch2))
	}
	if batch1[0] != batch2[0] || batch1[1] != batch2[1] {
		t.Errorf("lock acquisition order diverged: batch1=%v batch2=%v", batch1, batch2)
	}
}

func TestLockKey_DifferentUsersDifferentKeys(t *testing.T) {
	card := testUUID(t, "00000000-0000-7000-8000-0000000000aa")
	userA := testUUID(t, "00000000-0000-7000-8000-000000000001")
	userB := testUUID(t, "00000000-0000-7000-8000-000000000002")

	if lockKey(userA, card) == lockKey(userB, card) {
		t.Error("lockKey should differ across users for the same card")
	}
}
