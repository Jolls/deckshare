package review

import "testing"

func TestInterleave(t *testing.T) {
	cases := []struct {
		name                  string
		reviewCount, newCount int
		limit                 int
		wantLen               int
		wantReviewTaken       int
		wantNewTaken          int
	}{
		{"both zero", 0, 0, 20, 0, 0, 0},
		{"review only", 5, 0, 20, 5, 5, 0},
		{"new only", 0, 5, 20, 5, 0, 5},
		{"under limit takes everything", 3, 2, 20, 5, 3, 2},
		{"exactly at limit", 10, 10, 20, 20, 10, 10},
		{"oversized both, capped at limit", 15, 15, 20, 20, 10, 10},
		{"lopsided 3:1", 9, 3, 12, 12, 9, 3},
		// 3:27 (1:9) of the total pool, scaled down to a 12-card limit: 12*3/30=1.2 -> 1 review,
		// 11 new (the algorithm rounds by picking whichever source is furthest behind its share
		// at each step, not by pre-computing a rounded target count).
		{"lopsided 1:9 pool, scaled to limit", 3, 27, 12, 12, 1, 11},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			picks := interleave(c.reviewCount, c.newCount, c.limit)
			if len(picks) != c.wantLen {
				t.Fatalf("len(picks) = %d, want %d", len(picks), c.wantLen)
			}
			var revTaken, newTaken int
			for _, p := range picks {
				if p {
					revTaken++
				} else {
					newTaken++
				}
			}
			if revTaken != c.wantReviewTaken || newTaken != c.wantNewTaken {
				t.Errorf("revTaken=%d newTaken=%d, want revTaken=%d newTaken=%d",
					revTaken, newTaken, c.wantReviewTaken, c.wantNewTaken)
			}
		})
	}
}

// TestInterleave_NeverExceedsSourceCounts: at every prefix of the returned picks, the running
// review/new tallies never exceed reviewCount/newCount -- interleave must never "invent" a pick
// from a source that ran out (it should fall through to the other source instead).
func TestInterleave_NeverExceedsSourceCounts(t *testing.T) {
	for reviewCount := 0; reviewCount <= 6; reviewCount++ {
		for newCount := 0; newCount <= 6; newCount++ {
			for limit := 1; limit <= 10; limit++ {
				picks := interleave(reviewCount, newCount, limit)
				var revTaken, newTaken int
				for _, p := range picks {
					if p {
						revTaken++
					} else {
						newTaken++
					}
					if revTaken > reviewCount || newTaken > newCount {
						t.Fatalf("reviewCount=%d newCount=%d limit=%d: revTaken=%d newTaken=%d exceeded a source count",
							reviewCount, newCount, limit, revTaken, newTaken)
					}
				}
			}
		}
	}
}

// TestInterleave_Interleaved: with a large lopsided ratio, the minority source's picks aren't all
// clumped at one end -- a regression guard on the deficit scheduler actually spreading things out
// rather than degenerating to "all of one source, then all of the other".
func TestInterleave_Interleaved(t *testing.T) {
	picks := interleave(9, 3, 12)
	if len(picks) != 12 {
		t.Fatalf("len(picks) = %d, want 12", len(picks))
	}
	// Every run of 4 consecutive picks should contain at least one "new" pick (false), since new
	// cards are 1-in-4 overall; a truly clumped ordering (e.g. all 9 review picks first) would
	// fail this for the first two windows.
	for start := 0; start+4 <= len(picks); start++ {
		hasNew := false
		for _, p := range picks[start : start+4] {
			if !p {
				hasNew = true
			}
		}
		if !hasNew {
			t.Errorf("window [%d:%d) = %v has no new-card pick, want interleaving not clumping", start, start+4, picks[start:start+4])
		}
	}
}
