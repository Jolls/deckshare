# Issue #91 — Orphaned media blob GC

Flagged and deferred in [docs/plans/51-deletion-policy.md §0.8](51-deletion-policy.md) ("blob
deletion is not a feature", because a GC has to delete a row and a file with no transaction
spanning them). The store landed with #60, so the deferral's blocker is gone and the two orphan
classes it predicted are now both reachable.

Scope: two sqlc queries, a `Delete`/`Walk` pair on `media.Store`, a `media.GC` with a `Sweep` and
an hourly ticker in `cmd/enshu/main.go`. No schema change, no migration, no HTTP surface.

---

## 1. The two orphan classes

**Class 1 — zero-ref `media_blobs` rows.** `media_refs` is keyed `(deck_id, filename)` and written
only by `importMedia` (`internal/apkg/dbwrite.go`); note edits never touch it, so dropping an
`<img>` from a field does *not* orphan a blob. The only producer is a deck delete: `media_refs`
cascades with its deck (`migrations/00014_media_refs.sql`), `media_blobs` does not (FK RESTRICT),
so the row and its bytes outlive the last reference. Found by SQL.

**Class 2 — file with no row.** `blobs.Put` writes bytes before the enclosing import transaction
commits (`importMedia` runs inside the tx opened by `internal/http/import.go`, and `Put` is a
filesystem call that transaction cannot roll back). A failed or rolled-back import therefore leaves
`${MEDIA_ROOT}/<sha[0:2]>/<sha>` populated with no `media_blobs` row at all. Invisible to any query
over `media_blobs`; findable only by walking `MEDIA_ROOT`.

Class 2 is why this is a sweep and not a hook — see §4.

---

## 2. Ordering: unlink the file, *then* delete the row

The DB half is already race-safe without help from us. A concurrent `CreateMediaRef` takes
`FOR KEY SHARE` on its parent `media_blobs` row as part of Postgres's own RESTRICT enforcement, so
a plain `DELETE FROM media_blobs WHERE sha256 = $1` fails if a ref was (re-)created after the sweep
listed that row as orphaned. **The SQLSTATE is `23001` (`restrict_violation`), not the `23503` a bad
child insert raises** — verified against this repo's Postgres 18, and matched by the new
`db.IsRestrictViolation` (which accepts both codes, since a server that reports RESTRICT as a plain
foreign-key violation must not silently turn an expected skip into a logged error).
No `NOT EXISTS` re-check in the DELETE
would add anything — the database is already performing exactly that check under a lock we cannot
take from Go. **That failure is an expected outcome, not an error**: someone re-referenced the blob
mid-sweep. Skip it and move on; the next tick re-lists it if it goes cold again.

The unlink is the half no lock covers, and the ordering decides which way a lost race breaks.
The relevant fact is `Put`'s existence check: it `Stat`s the path and no-ops if bytes are already
there, so an import can adopt a file that a sweep is in the middle of reclaiming.

**Row-delete-then-unlink strands live rows.** Once the delete *commits*, the row is gone as far as
every other transaction is concerned, so a concurrent import is no longer blocked by anything: its
`Put` no-ops on the file still sitting on disk, `CreateMediaBlob` re-inserts the row, `CreateMediaRef`
succeeds, and it commits. The sweep then unlinks the file out from under a fully live row and ref.
The window is wide — it runs from the import's `Put` to the sweep's unlink, and the sweep's own
commit *enables* the import rather than blocking it.

**Unlink-then-row-delete degrades to a retry.** In the common interleaving the import is still
in flight when the sweep's DELETE commits, and its later `CreateMediaRef` hits the now-missing
parent row: FK violation → `importMedia` returns an error → the whole import transaction rolls
back. A failed import the user can retry, with no live row pointing at missing bytes. The
symmetric case — the import committed its ref just before our DELETE ran — is the 23503 skip
above, and the sweep leaves the row alone.

**Residual window, accepted.** Neither ordering closes the hole completely, because `Put` decides
whether to write bytes by looking at the filesystem while the sweep decides whether to remove them
by looking at the database, and nothing joins those two observations. The narrow case that survives
is an import whose `Put` stats the file *before* the unlink and whose `CreateMediaBlob` runs *after*
the sweep's DELETE commits: it re-creates the row, refs it, and commits, with the bytes gone. Those
two calls are adjacent lines in `importMedia`, so the sweep's entire delete has to land between
them. Damage is a 404 from `GET /media/{sha256}` for one file, self-healing on the next import of
the same content (`Put` re-writes when the file is absent), and never a scheduling-state or
`review_log` problem. The durable fix is making `Put` commit-aware (stage into a temp area, publish
after the transaction commits) — a change to the import path, not to the GC, and out of scope here.

A rejected middle option: `BEGIN; DELETE …; unlink; COMMIT`, holding the row lock across the
unlink. It narrows the window (a concurrent ref insert blocks on the lock and then fails) but does
not close it — the `Put`/`CreateMediaBlob` straddle above survives it unchanged — and it buys that
narrowing by doing filesystem I/O inside an open transaction holding a lock. Not worth it for a
failure mode that resolves itself on re-import.

**Class 2's walk carries the same shape and the same acceptance**: check for a row, and unlink only
if there is none. An import that adopts the file between the check and the unlink loses its bytes
the same way, with the same self-healing.

---

## 3. Grace period: 24 hours, class 2 only

The walk cannot distinguish "orphaned by a rolled-back import last week" from "just written by an
import that has not committed yet" — both are a file with no row. So it skips any file whose mtime
is newer than the grace period.

24 hours: three orders of magnitude above the longest plausible import transaction (a large `.apkg`
is seconds, and the HTTP path bounds it well below a minute), which is what makes it safe rather
than merely likely-safe; and orphan bytes held one extra day cost nothing, since the whole point of
the class is that they are already invisible and unreferenced. A short grace would buy faster
reclamation of disk space nobody is waiting on, against a real risk of eating a live import's
freshly written bytes.

Class 1 gets no age filter. A row reaches zero refs only after a deck delete commits, and an import
racing to re-reference it is handled by the RESTRICT FK (§2), not by waiting.

---

## 4. Cadence: a ticker, not a hook

A post-`DeleteDeck` hook cannot reach class 2 at all — a filesystem-only leak has no mutation point
to hang off, by definition. It would also need a post-commit callback — running the unlink inside
the deck-delete transaction means a rollback leaves bytes destroyed for a deck that still exists —
plus filesystem I/O inside a request handler.

A ticker covers both classes with one mechanism, retries naturally on the next tick, and matches the
existing precedent — `auth.Service.Run(ctx, interval)`, wired as `go authSvc.Run(ctx, time.Hour)`
in `cmd/enshu/main.go`. Same shape, same hourly interval, its own goroutine: `log.Printf` and carry
on, never `log.Fatal` from inside the loop, since a sweep failure is never worth taking the server
down for.

---

## 5. Files touched

| File | Change |
|---|---|
| `internal/db/queries/media_blobs.sql` | `ListUnreferencedMediaBlobs` (class 1's SELECT), `DeleteMediaBlob` (the plain DELETE §2 relies on), and `ListExistingMediaBlobs` (class 2's batched existence check — one round trip for the walk's whole page of candidates, not one per file). `go generate ./...` regenerates `media_blobs.sql.go`. |
| `internal/db/errors.go` | `IsRestrictViolation`, beside the existing `IsUniqueViolation`/`IsForeignKeyViolation` — the RESTRICT block has its own SQLSTATE (§2). |
| `internal/media/store.go` | `Delete` (idempotent — `os.ErrNotExist` is success) and `Walk` (yields each stored digest and its mtime; anything not a 64-char lowercase hex name is skipped, which covers `Put`'s in-flight `.tmp-*` scratch files). |
| `internal/media/gc.go` | `GC` over a `db.DBTX` plus a `*Store`: `Sweep(ctx)` runs both classes, `Run(ctx, interval)` is the ticker. |
| `cmd/enshu/main.go` | `go media.NewGC(pool, blobs, 24*time.Hour).Run(ctx, time.Hour)`, alongside the auth ticker. |
| `docs/schema.md`, `docs/architecture.md` | Replace the "cleanup is deferred" notes with what now collects them. |

## 6. Tests

- `internal/media/store_test.go` — `Delete` removes, is idempotent on a missing blob, and rejects a
  malformed digest; `Walk` yields stored digests, skips `.tmp-*` and other non-digest names, skips
  nested junk, and tolerates a store root that does not exist yet.
- `internal/media/gc_test.go` (DB-backed, `pgx.Tx`-scoped like `internal/db/deletion_test.go`) —
  the zero-ref query finds only this test's orphan and not its referenced blob; the sweep unlinks
  the file and deletes the row; **a ref created between the list and the delete makes the DELETE
  fail with 23001 and the sweep skips the blob rather than erroring**; the class-2 walk deletes an
  aged file with no row and spares a fresh one, a referenced one, and a `.tmp-*` one.

  That third test simulates the interleaving rather than racing it: a test-only `DBTX` wrapper
  inserts the ref immediately before the sweep's `DeleteMediaBlob` — the one point where a real
  concurrent writer would have to land to matter — and gives each `Exec` its own savepoint so the
  provoked error aborts only that statement, as it would on a pooled connection in production. A
  two-goroutine version would have to *commit* its ref to be visible to the sweep, which a
  rolled-back test transaction cannot do without leaving rows behind, and would still depend on
  lock-wait timing to be deterministic.
