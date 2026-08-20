package review

// interleave decides, for up to limit total picks, whether each pick comes from the review
// source or the new source (#116, new.mix = "mixed"), proportional to reviewCount:newCount in
// *this* fetch -- not a running total carried across the whole study day, which is what makes it
// robust to cards being introduced or graded between fetches (nothing depends on cumulative state
// from an earlier one). Returns a slice of length min(limit, reviewCount+newCount) where true
// means "take the next review row", false "take the next new row".
//
// A deficit scheduler: at each step, pick whichever source's taken-so-far is furthest behind its
// target share of what's taken so far, comparing (revTaken+1)*newCount against
// (newTaken+1)*reviewCount to stay in integer arithmetic. Once one source is exhausted the other
// fills the rest.
func interleave(reviewCount, newCount, limit int) []bool {
	total := reviewCount + newCount
	if total > limit {
		total = limit
	}
	if total <= 0 {
		return nil
	}

	picks := make([]bool, 0, total)
	var revTaken, newTaken int
	for i := 0; i < total; i++ {
		var wantReview bool
		switch {
		case revTaken >= reviewCount:
			wantReview = false
		case newTaken >= newCount:
			wantReview = true
		default:
			wantReview = int64(revTaken+1)*int64(newCount) <= int64(newTaken+1)*int64(reviewCount)
		}
		picks = append(picks, wantReview)
		if wantReview {
			revTaken++
		} else {
			newTaken++
		}
	}
	return picks
}
