package db

import (
	"context"
	"strings"
	"testing"
)

// TestSortKeyFunctions_MatchInlineExpressions (#219): deckshare_new_card_key and deckshare_rev_key
// (migration 00026) are the one definition of the study queue's sort keys, and must return exactly
// what the expressions they replaced in reviews.sql returned. The CASE below is that old
// expression, kept here verbatim as the reference.
func TestSortKeyFunctions_MatchInlineExpressions(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	var total, mismatches int
	if err := tx.QueryRow(ctx, `
		WITH inputs(rev_order, due, scheduled_days, card_id, seed) AS (
			SELECT o, d, s, i::uuid, 'seed-a'
			FROM unnest(ARRAY['due','random','intervalAsc','intervalDesc','bogus']) AS o,
			     unnest(ARRAY['2026-06-01 04:30+00','2026-06-02 11:15+00']::timestamptz[]) AS d,
			     unnest(ARRAY[0,7]) AS s,
			     unnest(ARRAY['018f5b3e-0000-7000-8000-000000000001','018f5b3e-0000-7000-8000-000000000002']) AS i
		)
		SELECT count(*),
		       count(*) FILTER (WHERE deckshare_rev_key(rev_order, due, scheduled_days, card_id, seed)
		                        IS DISTINCT FROM (CASE
		           WHEN rev_order = 'random' THEN
		               ('x' || md5(card_id::text || seed))::bit(52)::bigint::double precision
		           WHEN rev_order = 'intervalAsc' THEN scheduled_days::double precision
		           WHEN rev_order = 'intervalDesc' THEN (-scheduled_days)::double precision
		           ELSE extract(epoch from due)
		       END))
		FROM inputs`,
	).Scan(&total, &mismatches); err != nil {
		t.Fatalf("deckshare_rev_key: %v", err)
	}
	if total != 40 {
		t.Fatalf("compared %d input rows, want 40", total)
	}
	if mismatches != 0 {
		t.Errorf("deckshare_rev_key differs from the inline CASE on %d of %d inputs", mismatches, total)
	}

	if err := tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE deckshare_new_card_key(x) IS DISTINCT FROM COALESCE(x, 2147483647)::double precision)
		FROM unnest(ARRAY[NULL, -5, 0, 7, 2147483647]::integer[]) AS x`,
	).Scan(&total, &mismatches); err != nil {
		t.Fatalf("deckshare_new_card_key: %v", err)
	}
	if total != 5 || mismatches != 0 {
		t.Errorf("deckshare_new_card_key: %d of %d inputs differ from COALESCE(x, 2147483647), want 0 of 5", mismatches, total)
	}

	// Volatility is part of the contract: declaring either function stricter than its body is not
	// checked by Postgres and buys wrong plans (extract(epoch from timestamptz) is STABLE).
	for fn, want := range map[string]string{
		"deckshare_new_card_key(integer)":                       "i",
		"deckshare_rev_key(text,timestamptz,integer,uuid,text)": "s",
	} {
		var got string
		if err := tx.QueryRow(ctx, `SELECT provolatile::text FROM pg_proc WHERE oid = $1::regprocedure`, fn).Scan(&got); err != nil {
			t.Fatalf("provolatile %s: %v", fn, err)
		}
		if got != want {
			t.Errorf("provolatile of %s = %q, want %q", fn, got, want)
		}
	}
}

// TestStudyQueries_SortKeyFunctionsInlined (#219): the point of defining the keys as functions is
// that they cost nothing at runtime. Postgres only inlines a SQL function that is a single
// subquery-free SELECT, not STRICT, not SECURITY DEFINER and with no SET clause -- break any of
// those and every study query silently falls back to a per-row function call. So each generated
// query must (a) call the functions and (b) plan with them expanded away.
func TestStudyQueries_SortKeyFunctionsInlined(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		sql     string
		wantNew bool // calls deckshare_new_card_key
		wantRev bool // calls deckshare_rev_key
	}{
		{"ListDueCardsForStudy", listDueCardsForStudy, true, true},
		{"ListReviewCardsForStudy", listReviewCardsForStudy, false, true}, // review-state cards only
		{"ListNewCardsForStudy", listNewCardsForStudy, true, false},
		{"ListNotesInDeck", listNotesInDeck, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantNew && !strings.Contains(tc.sql, "deckshare_new_card_key(") {
				t.Errorf("%s does not call deckshare_new_card_key", tc.name)
			}
			if tc.wantRev && !strings.Contains(tc.sql, "deckshare_rev_key(") {
				t.Errorf("%s does not call deckshare_rev_key", tc.name)
			}

			// The raw simple-query path: the generated SQL still carries $N placeholders, which
			// GENERIC_PLAN accepts but pgx's client-side argument substitution would reject.
			results, err := tx.Conn().PgConn().Exec(ctx, "EXPLAIN (GENERIC_PLAN, VERBOSE, COSTS OFF)\n"+tc.sql).ReadAll()
			if err != nil {
				t.Fatalf("EXPLAIN %s: %v", tc.name, err)
			}
			var plan strings.Builder
			for _, res := range results {
				for _, row := range res.Rows {
					plan.Write(row[0])
					plan.WriteByte('\n')
				}
			}
			if !strings.Contains(plan.String(), "Scan") {
				t.Fatalf("EXPLAIN %s returned no plan:\n%s", tc.name, plan.String())
			}
			for _, fn := range []string{"deckshare_new_card_key(", "deckshare_rev_key("} {
				if strings.Contains(plan.String(), fn) {
					t.Errorf("%s plan still calls %s -- the planner did not inline it:\n%s", tc.name, fn, plan.String())
				}
			}
		})
	}
}
