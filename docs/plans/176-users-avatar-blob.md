# #176 — Add avatar blob reference to `users` (schema only)

**Issue:** [#176](https://github.com/Jolls/deckshare/issues/176) "Add blob in users for avatar or photo."

**Update:** the scope line below described the schema-only first commit on this branch. A
follow-up commit on the same branch/PR added the upload route (`POST /settings/avatar`), the
serving route (`GET /settings/avatar`, self-only), the settings-page UI, and the client-side
resize/re-encode step -- closing #176. See `docs/routes.md`'s Settings section and
`CHANGELOG.md`'s `0.2.20` entry for what actually shipped; this plan's body below is left
unedited as the historical record of the schema-only step.

**Scope (confirmed, do not re-litigate):** schema + queries + docs only. No HTTP upload route, no
serving route, no UI, no template change, no handler change. The column exists and is writable
through a query; nothing calls that query yet.

**Model recommendation:** Sonnet. Mechanical; every decision is resolved below.

---

## Decisions (all resolved — the implementer makes no judgment calls)

| Choice | Resolution | Reasoning |
|---|---|---|
| Column name | `avatar_sha256` | Mirrors `media_refs.sha256`; the `avatar_` prefix says which of a user's several possible blob references this is, leaving room for a future one without a rename. |
| Type / nullability | `text`, nullable | `media_blobs.sha256` is `text` (lowercase hex). NULL = no avatar. A `NOT NULL` column is impossible here anyway — every existing row would need a value. |
| `ON DELETE` | **RESTRICT** | Matches `media_refs.sha256 → media_blobs` exactly, and for the same reason recorded in #51 and `migrations/00014`: *a referenced blob is never deletable*. `SET NULL` is actively wrong here — it would let the hourly media GC sweep silently strip a user's avatar as a side effect of reclaiming what it wrongly believed was an orphan, which is exactly the class of silent data loss the RESTRICT philosophy exists to prevent. RESTRICT also keeps the FK usable as the GC's race check (see §3). |
| `ON UPDATE` | omitted (default `NO ACTION`) | `media_refs` omits it too; `media_blobs.sha256` is content-addressed and never updated. |
| Index | partial index on `users (avatar_sha256) WHERE avatar_sha256 IS NOT NULL` | Required, not optional: RESTRICT makes Postgres run `SELECT 1 FROM users WHERE avatar_sha256 = $1` on **every** `media_blobs` delete, i.e. on every GC tick, and without an index that is a seq scan of `users` per blob (`docs/schema.md` § "Indexes backing `ON DELETE RESTRICT`"). Partial rather than plain (`media_refs_sha256_idx` is plain) because the overwhelming majority of `users` rows will have NULL here; `avatar_sha256 = $1` implies `IS NOT NULL`, so the planner can still use a partial index for the RI probe. |
| `UpdateUserProfile` | **not touched** | Setting an avatar is a distinct operation from editing the profile form: different input (a file/blob digest, not text fields), different validation, and it must be independently clearable. Folding it in would force every profile save to restate the avatar and would make an accidental empty value wipe it. |
| New query | `UpdateUserAvatar :exec` | Follows the existing `UpdateUserPassword` precedent — a single-column update for a distinct operation. Setting NULL through it is how an avatar gets cleared, so no separate `ClearUserAvatar`. |
| `GetUser` / `GetUserByEmail` / `CreateUser` | **not touched** | They are `SELECT *` / `RETURNING *`; `sqlc` picks the column up automatically on regeneration. |
| Avatar *serving* authorisation | **out of scope** — do not add | `GetMediaBlobForUser` gates blob reads on a `deck_access` join, so it will not serve an avatar. That is correct for this issue (no serving route exists) and is the follow-up issue's problem, not this one's. Do not widen that query. |

---

## 1. New migration: `migrations/00017_users_avatar_sha256.sql`

Next number confirmed by `ls migrations/` (last is `00016_cards_import_due_position.sql`).
Create it with `goose -dir migrations create -s users_avatar_sha256 sql` and then replace the
generated body, or just write the file directly with this exact content:

```sql
-- +goose Up
-- #176: a user's avatar is a media blob like any other -- metadata row here, bytes on the
-- filesystem at ${MEDIA_ROOT}/<sha[0:2]>/<sha>, deduplicated across all users, never a bytea
-- column (architecture.md §12). RESTRICT for the same reason media_refs uses it (#51): a
-- referenced blob is never deletable, including by the GC sweep, which relies on this FK as its
-- race check (docs/schema.md, Media). NULL = no avatar.
ALTER TABLE users ADD COLUMN avatar_sha256 text
    REFERENCES media_blobs (sha256) ON DELETE RESTRICT;

-- Backs the RESTRICT check above: every media_blobs delete probes this column, so without an
-- index that is a sequential scan of users per blob on every GC tick. Partial because almost
-- every row is NULL, and `avatar_sha256 = $1` implies IS NOT NULL so the probe can still use it.
CREATE INDEX users_avatar_sha256_idx ON users (avatar_sha256) WHERE avatar_sha256 IS NOT NULL;

-- +goose Down
DROP INDEX users_avatar_sha256_idx;
ALTER TABLE users DROP COLUMN avatar_sha256;
```

Notes for the implementer:

- The inline `REFERENCES` gives the constraint Postgres's default name **`users_avatar_sha256_fkey`**.
  That exact string is used in §3 — do not add a `CONSTRAINT <name>` clause that changes it.
- The column is nullable, so the `NOT NULL`-on-populated-table hazard in `docs/schema.md`
  § Migrations checklist item 1 does not apply. A plain `ADD COLUMN` is safe here.
- Comment style matches `00016_cards_import_due_position.sql` (issue number first, then the
  invariant it does and does not touch).

## 2. `internal/db/queries/users.sql`

Append exactly this at the end of the file (after `UpdateUserPassword`):

```sql
-- Avatar changes are a distinct operation from a profile edit (#176): different input, and it must
-- be independently clearable -- passing NULL is how an avatar is removed, so there is no separate
-- clear query. The sha256 must already exist in media_blobs; the FK is what enforces that.
-- name: UpdateUserAvatar :exec
UPDATE users SET avatar_sha256 = $2 WHERE id = $1;
```

No other edit to this file. `GetUser`, `GetUserByEmail` and `CreateUser` gain the field for free
via `SELECT *` / `RETURNING *`.

## 3. `internal/db/queries/media_blobs.sql` — GC correctness (mandatory)

**This is the one non-obvious part of the change and it must not be skipped.** `users` is now a
second table that can reference a blob, and `ListUnreferencedMediaBlobs` only checks `media_refs`.
`internal/media/gc.go:74-82` **unlinks the file first and deletes the row second** (deliberately —
see the comment there). So without this change, the hourly sweep would unlink a live avatar's bytes
from disk, then get a `users_avatar_sha256_fkey` restrict violation on the row delete — which
`IsRestrictViolation(err, mediaRefsSha256FK)` does *not* match — and report it as an error, leaving
a `media_blobs` row and a `users.avatar_sha256` pointing at a file that no longer exists. Silent,
unrecoverable blob loss.

Replace the `ListUnreferencedMediaBlobs` block (currently lines 11–17) with:

```sql
-- The zero-ref half of the media GC sweep (#91, docs/plans/91-orphaned-media-blob-gc.md). A blob
-- outlives the deck that imported it: media_refs cascades away with its deck, media_blobs does not
-- (FK RESTRICT), so a deck delete is the only thing that strands a row here. No age filter -- an
-- import racing to re-reference one of these is handled by the FK, not by waiting (DeleteMediaBlob).
-- Both referencing tables must be checked: users.avatar_sha256 (#176) is a second RESTRICT
-- reference, and the sweep unlinks bytes before deleting the row, so a blob missing from this
-- WHERE clause loses its file before the FK can object.
-- name: ListUnreferencedMediaBlobs :many
SELECT mb.sha256 FROM media_blobs mb
WHERE NOT EXISTS (SELECT 1 FROM media_refs mr WHERE mr.sha256 = mb.sha256)
  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar_sha256 = mb.sha256);
```

## 4. `internal/media/gc.go` — cover the second FK in the skip check

Same reasoning as §3, for the interleaving the list query cannot close (an avatar set between the
list and the delete). This is not speculative hardening: it is the exact race `gc.go`'s existing
comment says the FK is there to catch, and the failure mode is the already-unlinked file above.

Change the constant block at `internal/media/gc.go:13-15` to:

```go
// The constraints that make a referenced blob undeletable, and so the ones whose violation the
// sweep reads as "re-referenced mid-sweep" rather than as a failure: a deck's media_refs row, or a
// user's avatar (#176).
const (
	mediaRefsSha256FK    = "media_refs_sha256_fkey"
	usersAvatarSha256FK  = "users_avatar_sha256_fkey"
)
```

and the check at line 79 to:

```go
		if db.IsRestrictViolation(err, mediaRefsSha256FK) || db.IsRestrictViolation(err, usersAvatarSha256FK) {
			continue
		}
```

Run `gofmt` on the file afterwards (the const block alignment above is illustrative).

## 5. Regenerate `sqlc` output

```
go generate ./internal/db/...
```

(the directive is `//go:generate sqlc generate -f ../../sqlc.yaml` in `internal/db/pool.go`).
Commit the regenerated files; never hand-edit them (CLAUDE.md §16). Expected diff:

- `internal/db/models.go` — `User` gains `AvatarSha256 pgtype.Text`.
- `internal/db/users.sql.go` — new `UpdateUserAvatar` method + `UpdateUserAvatarParams`; the three
  `SELECT *`/`RETURNING *` user queries gain the column in their column lists and `Scan` calls.
- `internal/db/media_blobs.sql.go` — updated `listUnreferencedMediaBlobs` SQL string.

Adding a field to `User` is additive; nothing in the repo builds a `db.User` with an unkeyed
struct literal (verified), so no call site should break. If `go build ./...` disagrees, fix the
literal rather than reshaping the query.

## 6. Regression test — `internal/media/gc_test.go`

Required: §3 is silent-break logic guarding blob loss (CLAUDE.md §10, working rule 5). Add one
test after `TestListUnreferencedMediaBlobs` (currently ends line 234), matching that file's
existing helpers and its rolled-back-transaction / random-digest isolation discipline:

```go
// #176: users.avatar_sha256 is a second RESTRICT reference to a blob. The sweep unlinks bytes
// before deleting the row, so an avatar's blob missing from the class-1 query would lose its file
// before the FK could object.
func TestListUnreferencedMediaBlobs_ExcludesAvatars(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()

	orphan, avatar := randomSHA(t), randomSHA(t)
	mustBlobRow(t, tx, orphan)
	mustBlobRow(t, tx, avatar)

	user := mustUser(t, tx)
	if _, err := tx.Exec(ctx, `UPDATE users SET avatar_sha256 = $2 WHERE id = $1`, user, avatar); err != nil {
		t.Fatalf("set avatar: %v", err)
	}

	shas, err := db.New(tx).ListUnreferencedMediaBlobs(ctx)
	if err != nil {
		t.Fatalf("ListUnreferencedMediaBlobs: %v", err)
	}

	found := map[string]bool{}
	for _, sha := range shas {
		found[sha] = true
	}
	if !found[orphan] {
		t.Error("unreferenced blob was not listed")
	}
	if found[avatar] {
		t.Error("avatar-referenced blob was listed as unreferenced")
	}
}
```

This is DB-backed, so it skips silently with `DATABASE_URL` unset — export it before trusting a
green run, and confirm with `go test ./internal/media/... -v | Select-String -Pattern skip`
(CLAUDE.md §16).

## 7. `docs/schema.md`

Three edits, all exact.

**(a) Deletion policy table** — insert a row immediately after the `media_refs.sha256 → media_blobs`
row (currently line 240, the last row of the table):

```
| `users.avatar_sha256 → media_blobs` | RESTRICT | A referenced blob is never deletable, same as `media_refs` — and `SET NULL` would let the GC sweep silently strip a user's avatar (#176). |
```

**(b) Per-user state block** — in the fenced `sql` block, replace the `users` entry (currently
lines 322–325):

```
users            id, email, password_hash, display_name, timezone,
                 day_start_hour smallint DEFAULT 4, avatar_sha256 NULL, created_at
                 -- UNIQUE on lower(email): one account per address, any casing
                 -- password_hash is argon2id (@node-rs/argon2), never a weaker algorithm
                 -- avatar_sha256 -> media_blobs, ON DELETE RESTRICT; NULL = no avatar (#176)
```

**(c) "Indexes backing `ON DELETE RESTRICT`" paragraph** — it currently ends with
"No new index is required by this change." and a sentence about `deck_access.user_id`. Append after
the `deck_access_user_id_idx` sentence (currently line 353):

```
`users.avatar_sha256` is `RESTRICT` and is backed by the partial index
`users_avatar_sha256_idx` (`WHERE avatar_sha256 IS NOT NULL`) — partial because almost every row is
NULL, and the RI probe's `avatar_sha256 = $1` implies `IS NOT NULL` so it can still use it (#176).
```

**(d) Media section** — after the two-line `sql` block listing `media_blobs` / `media_refs`
(currently lines 385–388) and its following paragraph, add one sentence to the end of the
"Two decks shipping an identical image store one blob." paragraph (line 392):

```
A user's avatar is the same mechanism reaching the same table from a different direction:
`users.avatar_sha256` references a blob directly rather than through a deck-scoped filename, so an
avatar shares storage with an identical image in any deck (#176). Both references are `RESTRICT`,
and the GC's `ListUnreferencedMediaBlobs` checks both — the sweep unlinks bytes before deleting the
row, so a referencing table it does not know about loses that blob's file.
```

## 8. `docs/schema-diagram.md`

Three edits.

**(a) Full schema diagram** — add an edge after `MEDIA_BLOBS ||--o{ MEDIA_REFS : "stored as"`
(line 43):

```
    MEDIA_BLOBS ||--o{ USERS : "avatar"
```

and add the column to the `USERS { }` block (lines 45–53), after `smallint day_start_hour`:

```
        text avatar_sha256 FK "NULL = no avatar"
```

**(b) "Media & auth" diagram** (lines 428–462) — add an edge after
`MEDIA_BLOBS ||--o{ MEDIA_REFS : "stored as"` (line 430):

```
    MEDIA_BLOBS ||--o{ USERS : "avatar"
```

and extend that section's `USERS { }` block (lines 451–454) to:

```
    USERS {
        uuid id PK
        text email
        text avatar_sha256 FK "NULL = no avatar"
    }
```

Then append to the closing paragraph (line 464–466, "Media is content-addressed and deduplicated
across *all* decks…"):

```
`USERS` reaches `MEDIA_BLOBS` directly through `avatar_sha256` rather than through a deck-scoped
filename, so an avatar dedups against deck media like anything else (#176).
```

**(c) "Access & sharing" diagram** (lines 376–406) — **no change**. Its `USERS` block is a
deliberate three-column subset (`id`, `email`, `display_name`) for that view; avatars are not part
of access and sharing. Leave it alone.

---

## Verification

1. `go generate ./internal/db/...`, then `go build ./...`, `go vet ./...`, `golangci-lint run`.
2. Fresh-database migration check: `goose -dir migrations postgres "$DATABASE_URL" up` against a
   fresh DB, then `... down` once and `... up` again to confirm the Down block is correct
   (index drop before column drop is not strictly required — dropping the column drops its index —
   but it is written explicitly and must not error).
3. `go test ./...` with `DATABASE_URL` exported. Confirm `TestListUnreferencedMediaBlobs_ExcludesAvatars`
   actually ran (see §6) rather than skipping.
4. Manual check the implementer should describe to the user rather than run (CLAUDE.md §14): none
   needed — there is no user-visible surface in this change.

## Changelog

One entry under a new `## [0.2.20] - <today>` heading in `CHANGELOG.md`:

```
### Added
- A `users.avatar_sha256` column referencing `media_blobs`, so a user's avatar is stored as a
  deduplicated content-addressed blob like any other media. Schema only -- no upload or serving
  route yet ([#176](https://github.com/Jolls/deckshare/issues/176)).
```

Tag `v0.2.20` after committing the bump.

## Out of scope (deliberately — do not build these here)

- Upload route, serving route, avatar rendering in the account header, image validation/resizing.
- Widening `GetMediaBlobForUser` to serve avatars — it gates on `deck_access` by design, and
  changing it is an authorisation decision belonging to the serving issue.
- Any `users`-row read path change. `GetUser` already returns the new field; nothing consumes it.

## Open questions

None. Every choice above is resolved.
