# Plan: #217 — unscoped review_log assertions in review_test.go

## Finding: already fixed on this branch

The two assertions the issue names are already scoped. No code change is needed.

- `internal/http/review_test.go:711` (in `TestReviewBatch_MalformedIs400`):
  ```go
  if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE card_id = $1`, cardID); n != 0 {
  ```
- `internal/http/review_test.go:802` (in `TestReviewRoutes_AccessControl`):
  ```go
  if n := countRows(t, tx, `SELECT count(*) FROM review_log WHERE card_id = $1`, cardID); n != 0 {
  ```

Both are preceded by a comment explaining the scoping:
```go
// Scoped to this test's own card, not table-wide: a dev database seeded by cmd/seed already
// holds review_log rows, and DB-backed tests must tolerate that (CLAUDE.md §16, #141).
```
(second site: `// Scoped to this test's own card, same reason as TestReviewBatch_MalformedIs400's count.`)

`git blame` attributes both lines to commit `d11a3adc` (Jolls, 2026-09-05, "Seed open and resolved
card flags on both deck kinds"). That commit's body states directly:

> Also scopes three DB-backed tests that asserted table-wide count(*) on review_log and
> media_blobs. They failed against any seeded database -- the messy-classroom seed leaves 5
> review_log rows and an avatar blob behind -- and tolerating a populated database is what
> CLAUDE.md §16 requires. Same class as #141.

The "5 review_log rows" figure matches the exact failure output quoted in #217's issue body
(`review_log count = 5, want 0`), confirming this is the same defect, already fixed before this
plan was requested.

## Why `card_id`, not `user_id = ANY($1)`

The issue's suggested fix (`WHERE user_id = ANY($1)`) is one valid scoping strategy, but the
existing fix scopes to `card_id = $1` instead, where `cardID` is the single card the test itself
created via `setupOneCard(t, tx, handler, ownerCookie)`. This is at least as tight a scope (one
card is narrower than "all of this test's users," since a stray row from a different test running
against the same user id — not possible here since each test uses fresh emails — would still be
excluded either way), and it matches the pre-existing style used at the sibling fix (#199,
`internal/apkg/dbwrite_test.go:567`, scoped to `WHERE sha256 = $1`): scope to the resource the
test created, not enumerate the users.

## Sibling issue #199 status

Also already fixed by the same commit (`d11a3adc`): `internal/apkg/dbwrite_test.go:567` scopes to
`WHERE sha256 = $1` (the blob the test itself wrote), with a matching CLAUDE.md §16 / #141 comment.
Out of scope for #217 (different file, different issue) — noted here only because the same commit
resolved both, which is what confirms #217 is already closed by prior work rather than coincidentally
similar code.

## Recommended action

No implementation work remains for #217. Recommend:
1. Verify `go test ./internal/http/... -run 'TestReviewBatch_MalformedIs400|TestReviewRoutes_AccessControl'`
   passes against a `cmd/seed`-seeded database (confirms the fix holds, doesn't just look right).
2. Close #217 with a comment pointing to `d11a3adc` as the commit that resolved it, per CLAUDE.md §15
   ("Add a one-sentence resolution comment before closing").
3. No PR needed — there is no diff to make.

## Open questions

None. The issue is fully resolved in the current working tree; this plan's only output is the
verification + close steps above.
