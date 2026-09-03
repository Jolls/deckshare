# Plan: #51 — Deck/user deletion policy: resolve the FK-restrict deadlock

Phase 1, step 2 (architecture.md §11), applied **on top of [#50](https://github.com/Jolls/deckshare/issues/50)**
(`docs/plans/50-schema-migrations.md`), which ships the full DDL with every foreign key written
`ON DELETE RESTRICT`. This issue replaces that blanket stopgap with the terminal per-FK policy,
adds the deck-delete procedure the policy needs, and closes the last-`can_manage_access`/
`can_delete`-holder guard flagged open in schema.md and routes.md.

Supersedes closed #15. Settles the "one consequence to settle" paragraph that ends
architecture.md §20.

---

## 0. Resolved decisions (no judgment calls left downstream)

### 0.1 Where the SQL goes: edit #50's migration files in place, do not add a 15th migration

CLAUDE.md §17 says applied migrations are immutable and §9 says *"never edit an applied one."*
**Neither rule is triggered here.** "Immutable once merged" means merged to `main`; #50's 14
migration files are authored in the same uncommitted batch, on the same branch, in the same PR
as this change, against a schema with zero deployed databases and zero real migration history
(architecture.md §1: no schema exists yet). Editing a file that has never been committed, never
been applied to any database anyone else can reach, and never been seen by a reviewer is not
"fixing backwards" — it is finishing the file before it is written down.

The alternative — a `00015_deletion_policy.sql` holding 19 `ALTER TABLE … DROP CONSTRAINT … ADD
CONSTRAINT …` statements that rewrite constraints created three commits earlier in the same PR —
would leave every table definition in `migrations/` reading `ON DELETE RESTRICT` while the live
schema says `CASCADE`, and would leave 21 `-- #51 revisits this` comments committed permanently
in a state where #51 has already revisited them. That is worse documentation, not better process.

So: **edit the 14 files from #50 in place** (§2), replacing each FK's action and each
`-- #51 revisits this` comment with the terminal decision and its reason.

**Precondition, and the one thing that flips this:** if #50 has already been **committed to
`main`** by the time this is implemented, the in-place edit is off the table and the fallback
applies — a new migration `00015_deletion_policy.sql`, written out in full in **Appendix A**, so
the switch is mechanical and requires re-deriving nothing. Check `git log --oneline main --
migrations/` before starting.

*(Nit to fix while editing: `docs/plans/50-schema-migrations.md` §0.2 says the blanket RESTRICT is
"applied to all 17 foreign keys". There are 21. Correct the number in that sentence — it is the
same file being amended for this issue's decisions anyway if the plan is updated, and a wrong
count invites an incomplete audit.)*

### 0.2 The terminal FK policy, all 21 foreign keys

Two organising principles decide every row, so nothing here is taste:

1. **A row that has no independent meaning without its parent cascades.** A session without a
   user, a field without its note type, a `deck_access` grant on a deck that no longer exists, a
   `user_card_state` row for a card that no longer exists — none of these are content or history;
   they are structure that only exists to describe the parent.
2. **A row that another user can reach, or that is training data, never cascades.** A deck is
   reachable by everyone holding a `deck_access` row; a note is content; `review_log` is the
   optimiser's training set (CLAUDE.md §2.5). These block, or are decoupled — never cascaded away.

| # | FK | Action | Why |
|---|---|---|---|
| 1 | `sessions.user_id → users` | **CASCADE** | An auth artifact, not content. Nothing survives a user it authenticates. |
| 2 | `note_types.owner_id → users` | RESTRICT | User delete is not a supported operation (§0.3). |
| 3 | `fields.note_type_id → note_types` | **CASCADE** | A field definition has no meaning without its note type; the two are one object split across tables. |
| 4 | `templates.note_type_id → note_types` | **CASCADE** | Same. |
| 5 | `decks.owner_id → users` | RESTRICT | §0.3. |
| 6 | `deck_access.deck_id → decks` | **CASCADE** | A grant on a deleted deck grants nothing. |
| 7 | `deck_access.user_id → users` | RESTRICT | §0.3. |
| 8 | `notes.owner_id → users` | RESTRICT | §0.3. |
| 9 | `notes.note_type_id → note_types` | RESTRICT | This *is* routes.md's documented note-type-delete rule ("blocked while any note references it"), enforced at the only layer that can't be bypassed. Deleting a note type must never silently delete the notes written with it — that is unrecoverable authored content. |
| 10 | `notes.deck_id → decks` | RESTRICT | **Deliberate tripwire.** A bare `DELETE FROM decks` must fail loudly; deck deletion goes through the ordered transaction in §0.5, which clears these references first. See §0.5 for why a static cascade cannot express the correct behaviour. |
| 11 | `cards.note_id → notes` | **CASCADE** | Cards are generated from a note; routes.md already specifies "Delete note and its cards". |
| 12 | `cards.template_id → templates` | RESTRICT | Never fires spuriously — cards only exist via notes, and #9 blocks a note-type delete while notes exist, so templates can only cascade away when no cards reference them. Kept as a belt-and-braces assertion of that reasoning. |
| 13 | `cards.deck_id → decks` | **CASCADE** | architecture.md §20: *"deleting a deck deletes its cards."* |
| 14 | `user_card_state.user_id → users` | RESTRICT | §0.3. |
| 15 | `user_card_state.card_id → cards` | **CASCADE** | §0.4. |
| 16 | `review_log.user_id → users` | RESTRICT — **permanent** | A review belongs to a live user; this is the FK that gives CLAUDE.md §2.5 teeth against account deletion. Never relax. |
| 17 | `review_log.card_id → cards` | **FK dropped; column retained** | §0.4 — the crux of this issue. |
| 18 | `user_fsrs_params.user_id → users` | RESTRICT | §0.3. |
| 19 | `user_fsrs_params.deck_id → decks` | **CASCADE** | A per-deck retention override for a deleted deck is dead configuration. The user's global row (`deck_id IS NULL`) is untouched by any deck delete. |
| 20 | `media_refs.deck_id → decks` | **CASCADE** | A `media_refs` row is deck-scoped by its own primary key `(deck_id, filename)`. |
| 21 | `media_refs.sha256 → media_blobs` | RESTRICT | A blob may never be deleted while any deck references it. Blob deletion is not a feature (§0.7). |

Nine actions change from `RESTRICT` to `CASCADE`, one FK is dropped, eleven stay `RESTRICT` —
now as decisions, not as stopgaps. Every `-- #51 revisits this` comment is replaced.

### 0.3 User deletion is **not** a supported operation in Phase 1 — and this is the answer, not a deferral

No route deletes a user: routes.md's Settings section has profile, password, and FSRS-default
updates and nothing else, and architecture.md §11's build order never reaches account closure.
So the schema's job is to make user deletion *impossible*, loudly, rather than to guess a cascade
nobody has designed.

It is impossible because eight FKs restrict on `users`, and this is right on the merits
independent of "no route exists yet":

- **`decks.owner_id` must not cascade.** A deck is reachable by every user holding a `deck_access`
  row. Cascading deck deletion off account closure means a co-authored deck or a classroom deck
  evaporates for everyone the moment its creator closes their account — silent, cross-user data
  loss, and the exact scenario Phase 2 exists to serve. Account closure needs an ownership-transfer
  decision first; that decision does not exist, so the FK blocks.
- **`notes.owner_id`, `note_types.owner_id`** — same argument, one level down: authored content.
- **`user_card_state.user_id`, `user_fsrs_params.user_id`, `deck_access.user_id`** — these three
  *could* defensibly cascade (they are per-user derived state), but cascading them buys nothing
  while #2/#5/#8 block, and it would mean a future account-deletion feature silently destroys a
  user's whole scheduling history the first time someone relaxes one of the other three. Blocking
  keeps the decision explicit for whoever builds that feature.
- **`review_log.user_id` blocks permanently.** A user who has ever answered a card cannot be
  deleted. This is CLAUDE.md §2.5 ("no `DELETE` paths without a written decision") expressed as a
  constraint rather than a convention.

`sessions.user_id` is the single exception and cascades (#1): a user with no decks, no notes, no
reviews, and no shared decks — a signup that never studied — can be deleted with a plain
`DELETE FROM users`, and their sessions go with them. That is the only user delete the schema
permits, and it is the only one that is unambiguously safe.

**Follow-up issue to file (do not build here): "Account deletion / data export policy (Phase 2)."**
It must decide ownership transfer for shared decks and, separately, what happens to `review_log`
— anonymise-and-retain versus delete, which is a §2.5 decision requiring the user's sign-off.
Reference this plan from it.

### 0.4 The crux: `review_log` versus "deleting a deck deletes its cards"

**The collision.** architecture.md §20 settles that deleting a deck deletes its cards. CLAUDE.md
§2.5 settles that `review_log` is append-only training data. #50 implemented §2.5 as
`review_log.card_id … ON DELETE RESTRICT`. Together those mean **no deck anyone has ever studied
can ever be deleted** — the first review a user performs permanently welds the deck to the
database. That is not a defensible product, and it is worse than that: a rule nobody can comply
with is a rule someone eventually deletes their way around, and the way around it is a
`DELETE FROM review_log`, which is the one thing §2.5 forbids absolutely.

**The resolution: drop the `review_log.card_id` foreign key. Keep the column, `NOT NULL`, keep
its index, keep it in `UNIQUE (user_id, card_id, anki_id)`.**

Why this is the correct reading of the invariant, not a relaxation of it:

- **The invariant is "no `review_log` row is ever deleted."** RESTRICT enforces that by forbidding
  the *parent* delete. Dropping the FK enforces it by making the parent's fate irrelevant. The
  second is strictly stronger against the failure that actually matters: after this change there
  is no delete path anywhere in the schema that removes a `review_log` row, and no cascade that
  could ever be added to one by editing a different table. schema.md's own words are *"nothing may
  cascade training data away"* — nothing does, and now nothing can.
- **This is Anki's own shape, so CLAUDE.md §2.10 endorses it.** Anki's `revlog.cid` is not a
  foreign key. Deleting a deck in Anki removes cards and leaves their revlog rows in place — which
  is precisely why Anki's optimiser can fit against a collection's full history rather than only
  its surviving cards. Divergence needs justifying; conformance does not.
- **Nothing depends on the reference being live.** `card_id` is a UUIDv7 and ids are never reused,
  so it remains a sound grouping key forever. The per-card replay path (`replayReviews`,
  architecture.md §6) only ever runs for cards that exist. The optimiser reads by `user_id`. The
  re-import dedup key still works. A `review_log` row whose card is gone is inert historical data
  — which is exactly what it is.

**This overrides a line in #50's plan** ("these two are NOT provisional and #51 must not relax
them"). That instruction was written before the collision was worked through; it is right about
`user_id` (#16 above, permanent) and it is what produces the deadlock on `card_id`. Flagged in
§8 for the user's veto, with the fallback spelled out there.

**Rejected alternatives**, recorded so they are not re-proposed:

- *`ON DELETE SET NULL` on `card_id`* — destroys the per-card grouping the training data is only
  useful in, and breaks `UNIQUE (user_id, card_id, anki_id)` (Postgres treats NULLs as distinct,
  so re-import dedup silently stops deduping).
- *Soft-deleting decks* — every deck-scoped query in the codebase grows a predicate to satisfy one
  constraint, and it contradicts §20's "deleting a deck deletes its cards". Simplicity First.
- *Archiving rows to a `review_log_archive` table* — renames the problem and forces every optimiser
  read into a UNION.
- *Blocking deck delete when the deck has review history* — the status quo this issue exists to end.

**`user_card_state.card_id` cascades (#15).** Scheduling state for a card that no longer exists is
meaningless, and deck delete has to be able to finish. schema.md currently treats the RESTRICT as
a useful tripwire against the card-regeneration trap (drop-and-reinsert a note's cards on edit,
silently discarding progress); after this change that tripwire is gone and **the trap is live**.
Two things address it, and both are required:

- `review_log.card_id` having no FK does not make review history disappear — a re-created card
  gets a *new* id, so the old history is stranded from the new card. The corruption is therefore
  still real and still `sev: critical` the day the reviewer ships.
- schema.md's paragraph on this must be re-tensed from "either errors or silently cascades" to
  "silently cascades" (§5). The fix remains where schema.md already puts it: whichever change first
  makes cards stateful diffs ordinals and only adds/removes the cards that actually changed.

### 0.5 Deck delete is an ordered transaction in the query layer, not a cascade and not a trigger

**What the correct behaviour is** (architecture.md §20, and Anki's): deleting deck D deletes every
card whose `deck_id` is D; a note is deleted only when it has **no cards left anywhere** — not
merely because D was its home deck. A note whose cards span decks survives, and its `deck_id` must
be re-homed because D is about to stop existing and `notes.deck_id` is `NOT NULL`.

**Why no static FK expresses this.** "Delete the note when its last card goes" is a predicate over
sibling rows; `ON DELETE CASCADE` is a per-reference rewrite. Making `notes.deck_id` cascade
deletes notes that still have cards elsewhere and then takes those cards with them through
`cards.note_id` — the precise loss architecture.md §20 warns about.

**Why not a trigger.** An `AFTER DELETE` trigger on `cards` that deletes zero-card notes would fire
on *every* card delete, and the rule is wrong outside deck deletion: removing a cloze marker from
a note deletes a card, and a note left with zero cards there is a validation error the editor must
refuse (Anki refuses to save such a note), not a note to silently delete. A trigger cannot see
which context it is in without being told, and telling it means session variables — more machinery
than the transaction it replaces. Triggers also buy no race safety here (a trigger's `EXISTS`
check has exactly the same READ COMMITTED visibility problem as an application one), so the usual
argument for them does not apply.

**The procedure**, all four steps in one transaction, in this order:

1. **Lock and authorise** — `SELECT d.id … JOIN deck_access … WHERE d.id = $1 AND da.can_view AND
   da.can_delete FOR UPDATE OF d`. No row → `pgx.ErrNoRows` → the handler answers **404**, whether
   the deck is absent or merely invisible (schema.md: a deck you can't see must be
   indistinguishable from one that doesn't exist). The row lock serialises this against concurrent
   deck deletes and against the access-guard path (§0.6).
2. **Delete the notes that will be left with nothing** — every note that either is homed in D or
   has a card in D, and has no card outside D. This includes notes homed in D with no cards at all.
   Cascades through `cards.note_id` → `user_card_state.card_id`. `review_log` is untouched.
3. **Re-home the survivors** — every note still homed in D now provably has at least one card
   outside D (step 2's complement). Set `deck_id` to the deck of its lowest-ordinal surviving card,
   which is deterministic and matches §20's definition of a home deck ("where the notes list shows
   it"). If the subquery were ever NULL the `NOT NULL` column rejects it — the failure is loud.
4. **`DELETE FROM decks WHERE id = $1`** — cascades to `cards` (D's cards, including those of
   re-homed notes), `deck_access`, per-deck `user_fsrs_params`, and `media_refs`; the `notes.deck_id`
   RESTRICT now has nothing to block on.

**The post-condition is provable, and this is why the order matters:** step 4's cascade can only
delete cards in D. A note could only be left with zero cards if *all* its cards were in D, and every
such note was deleted in step 2. Therefore no note is ever left card-less by a deck delete. State
this in the code comment; it is the whole correctness argument.

Not handled, deliberately: a concurrent `INSERT` of a note or card into D by another user holding
`can_edit_content` does not take the deck lock, so it either lands and is cascaded away in step 4
or fails its FK. Both outcomes are acceptable for an operation gated on `can_delete`; adding
lock-taking to every content write to close it is not worth the contention.

### 0.6 The last-holder guard is application-level, in a transaction, behind a row lock

CLAUDE.md §9: *"Authorisation is explicit at the query layer."* The guard is an authorisation rule,
so it lives where every other authorisation rule lives, is written in the same style, and is
covered by the same table-driven access-control tests (CLAUDE.md §10.5). A `CONSTRAINT TRIGGER`
would put one authorisation rule in a place no one auditing authorisation would think to look, and
— as noted in §0.5 — would not be race-free either, since it faces the same visibility problem.

**Shape**, identical for revoke and for a flag downgrade, which is what makes it small:

1. Lock the deck row and check the caller's `can_manage_access` (`FOR UPDATE OF d`) → `ErrNoRows`
   → 404.
2. Perform the mutation (`DELETE` the row, or `UPDATE` its six flags), `:execrows` → 0 rows → the
   target had no `deck_access` row → `pgx.ErrNoRows`.
3. Re-count holders: `count(*) FILTER (WHERE can_manage_access)` and `… FILTER (WHERE can_delete)`
   for the deck. Either zero → return `ErrLastAccessHolder`; **the caller rolls back the
   transaction**, which is what makes it a guard rather than a report.

Mutate-then-check (rather than check-then-mutate) means one rule covers revocation, downgrade, and
any future path that touches flags, with no way to add a fourth path that forgets the check. The
deck row lock taken in step 1 is what makes it safe: without it, two concurrent revocations of the
last two holders each see the other's row still present and both commit, stranding the deck. It is
also the same lock `DeleteDeck` takes, so a delete and an access change on one deck serialise.

Consequences, both intended and both documented in schema.md and routes.md:

- The sole member of a deck cannot revoke their own access. They delete the deck instead — that
  is what `can_delete` is for.
- `decks.owner_id` is **not** a permission source and does not exempt anyone. Permission comes from
  `deck_access` only (schema.md), so the guard counts flag-holders and nothing else. An owner can be
  stripped of `can_manage_access` by another holder; the deck is still manageable, which is the
  property the guard protects.

### 0.7 Query-layer code ships in this issue; handlers do not

This issue is not schema-only, and it cannot be. `notes.deck_id` stays RESTRICT (§0.2 #10), so
deck deletion is *only* reachable through the transaction in §0.5 — shipping the FK policy without
that transaction would leave the deck-delete deadlock exactly where #51 found it, and would leave
the last-holder guard as prose in a doc. What ships:

- **`internal/db/queries/deletion.sql`** — six sqlc queries (§3).
- **`internal/db/deletion.go`** — three hand-written transaction helpers, `DeleteDeck`,
  `RevokeDeckAccess`, `SetDeckAccess` (§4).
- **`internal/db/deletion_test.go`** — DB-backed tests, which are the callers (§6).

What does not ship: any handler, any route, any `internal/http/` change. `POST /decks/{id}/delete`
belongs to Phase 1 step 5 (deck CRUD) and `access.go` to Phase 2; routes.md is updated to say the
query layer is ready and the handler is not.

The helpers **take a `pgx.Tx` and never open one**, so "this must run in a transaction" is a
compile-time fact rather than a comment, the caller owns commit/rollback (required by §0.6 step 3),
and tests can pass a rollback-only transaction. `sqlc`'s `DBTX` is satisfied by `pgx.Tx`, so
`New(tx)` is all the wiring needed. No new Go dependency: ids are `pgtype.UUID`, which
`sqlc` already emits with `sql_package: pgx/v5`.

Filename note: the hand-written `internal/db/deletion.go` does not collide with the generated
`internal/db/deletion.sql.go` — different paths, unlike the `db.go` collision #50 §0.10 handles.

### 0.8 Orphaned `media_blobs` cleanup is deferred, explicitly

A deck delete cascades its `media_refs` rows away and can leave a `media_blobs` row with zero
remaining references. **Nothing collects it, by design, in this issue.** Blob deletion is not a
row delete: the bytes live on the filesystem at `${MEDIA_ROOT}/<sha[0:2]>/<sha>` (schema.md, §Media)
and no store exists yet at all ([#60](https://github.com/Jolls/deckshare/issues/60)). A reference-counted
GC has to delete a row and a file with no transaction spanning them, which is a real design problem
and a separate feature. `media_refs.sha256` stays RESTRICT so nothing can delete a referenced blob
in the meantime, and the orphan is a bounded disk-space cost, not a correctness one.

**Follow-up issue to file: "Orphaned media blob GC"**, blocked on #60. Note it in schema.md's Media
section so it is not silently skipped.

---

## 1. Files touched

**Edited (from #50, in place — §0.1):** `migrations/00002_sessions.sql`, `00004_fields.sql`,
`00005_templates.sql`, `00007_deck_access.sql`, `00008_notes.sql`, `00009_cards.sql`,
`00010_user_card_state.sql`, `00011_review_log.sql`, `00012_user_fsrs_params.sql`,
`00014_media_refs.sql`. Comment-only touch-ups to `00003_note_types.sql` and `00006_decks.sql`
(replace `-- #51 revisits this`). `00001_users.sql` and `00013_media_blobs.sql` have no FKs and
are untouched.

**Created:** `internal/db/queries/deletion.sql`, `internal/db/deletion.go`,
`internal/db/deletion_test.go`.

**Regenerated and committed:** `internal/db/deletion.sql.go`, `internal/db/querier.go`
(new methods), `internal/db/models.go` (unchanged in content — FK actions are not part of a
generated model; confirm the diff is empty rather than assuming it).

**Docs:** `docs/schema.md`, `docs/architecture.md` §20, `docs/routes.md`, `CHANGELOG.md`.

**Not touched, and confirmed by reading:** `docs/schema-diagram.md` (records columns, not FK
actions — re-read after implementing to confirm), `CLAUDE.md` (§2.5's invariant is unchanged: no
`DELETE` path is added; §17's "applied migrations" rule is not engaged, per §0.1),
`.github/workflows/ci.yml` (#50 already adds the Postgres service, applies migrations before
`go test`, and exports `DATABASE_URL` at job level — #51's DB-backed tests need no CI change),
`internal/http/`, `compose.yaml`.

---

## 2. Migration edits

Each edit is one FK line plus its comment. The complete set, file by file. Nothing else in these
files changes.

**`00002_sessions.sql`**
```sql
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,  -- #51: an auth
                                          -- artifact, not content; nothing survives its user
```

**`00003_note_types.sql`**
```sql
    owner_id       uuid     NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: user delete
                            -- is not a supported operation (docs/plans/51-deletion-policy.md §0.3)
```

**`00004_fields.sql`**
```sql
    note_type_id uuid    NOT NULL REFERENCES note_types (id) ON DELETE CASCADE,  -- #51: a field
                         -- definition has no meaning without its note type
```

**`00005_templates.sql`**
```sql
    note_type_id uuid    NOT NULL REFERENCES note_types (id) ON DELETE CASCADE,  -- #51: as fields
```

**`00006_decks.sql`**
```sql
    owner_id    uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: NOT a
                            -- stopgap. Cascading here would evaporate a shared deck for every
                            -- user holding a deck_access row when its creator closes their
                            -- account. Account deletion needs an ownership-transfer decision
                            -- that does not exist yet (§0.3).
```

**`00007_deck_access.sql`**
```sql
    deck_id           uuid        NOT NULL REFERENCES decks (id) ON DELETE CASCADE,   -- #51: a
                                  -- grant on a deleted deck grants nothing
    user_id           uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: §0.3
```
Also append to the table's header comment:
```sql
-- A deck must always retain at least one can_manage_access holder and one can_delete holder.
-- That guard is enforced in the query layer (internal/db/deletion.go), not here: it is an
-- authorisation rule and it needs a row lock to be race-free, which a constraint trigger would
-- also need (docs/plans/51-deletion-policy.md §0.6).
```

**`00008_notes.sql`**
```sql
    owner_id     uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,       -- #51: §0.3
    note_type_id uuid        NOT NULL REFERENCES note_types (id) ON DELETE RESTRICT,  -- #51: a
                             -- note type cannot be deleted while notes are written with it
                             -- (routes.md's note-type delete rule, enforced where it can't be
                             -- bypassed)
    deck_id      uuid        NOT NULL REFERENCES decks (id) ON DELETE RESTRICT,       -- #51:
                             -- deliberate tripwire. Deck deletion goes through DeleteDeck
                             -- (internal/db/deletion.go), which deletes card-less notes and
                             -- re-homes the survivors BEFORE deleting the deck. A bare
                             -- DELETE FROM decks must fail here rather than take notes whose
                             -- cards live in other decks with it (architecture.md §20).
```
Also update the index comment:
```sql
CREATE INDEX notes_deck_id_idx ON notes (deck_id);   -- deck-scoped queries + the deck-delete
                                                     -- RESTRICT check and DeleteDeck's re-home step
```

**`00009_cards.sql`**
```sql
    note_id     uuid    NOT NULL REFERENCES notes (id) ON DELETE CASCADE,       -- #51: cards are
                        -- generated from their note and die with it
    template_id uuid    NOT NULL REFERENCES templates (id) ON DELETE RESTRICT,  -- #51: can only
                        -- fire vacuously (notes.note_type_id blocks first), kept as an assertion
    deck_id     uuid    NOT NULL REFERENCES decks (id) ON DELETE CASCADE,       -- #51: deleting a
                        -- deck deletes its cards (architecture.md §20)
```

**`00010_user_card_state.sql`**
```sql
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: §0.3
    card_id        uuid        NOT NULL REFERENCES cards (id) ON DELETE CASCADE,   -- #51:
                               -- scheduling state for a card that no longer exists is meaningless,
                               -- and deck delete has to be able to finish. NOTE: this makes the
                               -- card-regeneration trap in docs/schema.md live -- a note edit that
                               -- drops and re-creates cards now silently discards progress.
```

**`00011_review_log.sql`** — the substantive one. Header comment becomes:
```sql
-- APPEND-ONLY. This is the optimiser's training set, not bookkeeping. No DELETE path without a
-- written decision (docs/schema.md, CLAUDE.md §2.5).
--
-- user_id is ON DELETE RESTRICT permanently: a review belongs to a live user, and this FK is what
-- blocks account deletion for anyone who has ever answered a card.
--
-- card_id deliberately has NO foreign key (#51, docs/plans/51-deletion-policy.md §0.4). RESTRICT
-- there would have made any deck that had ever been studied undeletable, which collides with
-- architecture.md §20 ("deleting a deck deletes its cards") and pressures a future change into
-- adding the one DELETE path §2.5 forbids. Decoupling is strictly stronger: no delete anywhere in
-- this schema can remove a review_log row, and none can be made to. card_id stays NOT NULL and
-- stays a sound grouping key -- UUIDv7 ids are never reused -- so history for a deleted card
-- remains replayable and remains in the optimiser's training set. This is also Anki's own shape:
-- revlog.cid is not a foreign key there either (CLAUDE.md §2.10).
```
```sql
    user_id               uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    card_id               uuid        NOT NULL,   -- no FK, by decision -- see above
```
And the index comment:
```sql
-- The per-card replay path. No RI check to serve any more (card_id has no FK), but the ordering
-- is what replayReviews reads (architecture.md §6).
CREATE INDEX review_log_card_id_user_id_reviewed_at_idx
    ON review_log (card_id, user_id, reviewed_at);
```

**`00012_user_fsrs_params.sql`**
```sql
    user_id             uuid     NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: §0.3
    deck_id             uuid     REFERENCES decks (id) ON DELETE CASCADE,            -- #51: a
                                 -- per-deck override for a deleted deck is dead configuration.
                                 -- The user's global row (deck_id IS NULL) is never touched.
```

**`00014_media_refs.sql`**
```sql
    deck_id  uuid NOT NULL REFERENCES decks (id) ON DELETE CASCADE,            -- #51: deck-scoped
                  -- by its own primary key
    sha256   text NOT NULL REFERENCES media_blobs (sha256) ON DELETE RESTRICT, -- #51: a referenced
                  -- blob is never deletable. Orphaned blobs are NOT collected -- deferred, §0.8.
```

---

## 3. `internal/db/queries/deletion.sql`

```sql
-- Deck deletion and the deck_access last-holder guard. These are not standalone queries: each
-- one is a step of a transaction orchestrated in internal/db/deletion.go, and running any of
-- them alone is a bug. See docs/plans/51-deletion-policy.md §0.5 and §0.6.

-- Locks the deck row for the duration of the transaction and authorises the caller in one step.
-- No row means "absent OR invisible OR not permitted" -- deliberately indistinguishable, so a
-- 403 can never become an existence oracle (docs/schema.md, Access control).
-- name: LockDeckForDelete :one
SELECT d.id
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
  AND da.can_view
  AND da.can_delete
FOR UPDATE OF d;

-- Step 2 of DeleteDeck. Deletes every note that this deck delete would leave with no cards at
-- all: notes homed here, and notes homed elsewhere whose cards all live here -- but only when
-- nothing of theirs survives outside this deck. Cascades to cards and user_card_state.
-- review_log is untouched and has no FK to cards (#51 §0.4).
-- name: DeleteNotesOrphanedByDeckDelete :execrows
DELETE FROM notes n
WHERE (
        n.deck_id = sqlc.arg(deck_id)
        OR EXISTS (SELECT 1 FROM cards c WHERE c.note_id = n.id AND c.deck_id = sqlc.arg(deck_id))
      )
  AND NOT EXISTS (SELECT 1 FROM cards c WHERE c.note_id = n.id AND c.deck_id <> sqlc.arg(deck_id));

-- Step 3 of DeleteDeck. Every note still homed here provably has a surviving card elsewhere
-- (the complement of the statement above), so the subquery cannot be NULL -- and if it ever were,
-- the NOT NULL column rejects it loudly. Home deck = the deck of the lowest-ordinal surviving
-- card, which is deterministic and matches architecture.md §20's definition of a home deck.
-- name: RehomeNotesOffDeck :execrows
UPDATE notes n
SET deck_id = (
        SELECT c.deck_id
        FROM cards c
        WHERE c.note_id = n.id AND c.deck_id <> sqlc.arg(deck_id)
        ORDER BY c.ordinal, c.id
        LIMIT 1
    ),
    modified_at = now()
WHERE n.deck_id = sqlc.arg(deck_id);

-- Step 4 of DeleteDeck. Cascades to cards (and through them to user_card_state), deck_access,
-- per-deck user_fsrs_params, and media_refs. Authorisation happened under the lock above.
-- name: DeleteDeckRow :execrows
DELETE FROM decks WHERE id = sqlc.arg(deck_id);

-- Locks the deck and authorises an access change. Same 404-shaped no-row contract as
-- LockDeckForDelete; the shared lock is what serialises concurrent revocations, without which
-- two callers can each remove "the second-to-last" holder and strand the deck.
-- name: LockDeckForAccessChange :one
SELECT d.id
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
  AND da.can_manage_access
FOR UPDATE OF d;

-- name: DeleteDeckAccessRow :execrows
DELETE FROM deck_access WHERE deck_id = sqlc.arg(deck_id) AND user_id = sqlc.arg(target_user_id);

-- name: UpdateDeckAccessRow :execrows
UPDATE deck_access
SET can_view          = sqlc.arg(can_view),
    can_study         = sqlc.arg(can_study),
    can_edit_content  = sqlc.arg(can_edit_content),
    can_edit_settings = sqlc.arg(can_edit_settings),
    can_manage_access = sqlc.arg(can_manage_access),
    can_delete        = sqlc.arg(can_delete)
WHERE deck_id = sqlc.arg(deck_id) AND user_id = sqlc.arg(target_user_id);

-- The guard itself, run AFTER the mutation inside the same transaction. Zero of either count
-- means the deck has been stranded and the caller must roll back.
-- name: CountDeckAccessHolders :one
SELECT
    count(*) FILTER (WHERE can_manage_access) AS manage_count,
    count(*) FILTER (WHERE can_delete)        AS delete_count
FROM deck_access
WHERE deck_id = sqlc.arg(deck_id);
```

---

## 4. `internal/db/deletion.go`

Hand-written, ~110 lines. Not generated — add no `sqlc` header.

```go
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrLastAccessHolder is returned when an access change would leave a deck with no
// can_manage_access holder or no can_delete holder -- stranding it with nobody able to manage or
// delete it. The caller MUST roll back the transaction: the mutation has already been applied and
// this error is the only thing preventing it from committing.
var ErrLastAccessHolder = errors.New("db: deck would be left with no can_manage_access or can_delete holder")

// DeleteDeck deletes deckID on behalf of userID, in the order docs/schema.md's deletion policy
// requires. It must be called inside a transaction it does not own; the caller commits.
//
// Deleting a deck deletes the cards filed in it, and a note goes only when it has no cards left
// anywhere -- not merely because this was its home deck (architecture.md §20). Notes whose cards
// survive in other decks are re-homed instead.
//
// The post-condition -- no note is ever left with zero cards -- holds because the final DELETE
// can only cascade away cards whose deck_id is this deck, and every note whose cards were all in
// this deck was already removed in step 2.
//
// Returns pgx.ErrNoRows when the deck does not exist, is not visible to userID, or userID lacks
// can_delete. Those three are deliberately indistinguishable; handlers answer 404 for all of them.
func DeleteDeck(ctx context.Context, tx pgx.Tx, deckID, userID pgtype.UUID) error {
	q := New(tx)

	if _, err := q.LockDeckForDelete(ctx, LockDeckForDeleteParams{
		DeckID: deckID,
		UserID: userID,
	}); err != nil {
		return err
	}
	if _, err := q.DeleteNotesOrphanedByDeckDelete(ctx, deckID); err != nil {
		return fmt.Errorf("delete card-less notes: %w", err)
	}
	if _, err := q.RehomeNotesOffDeck(ctx, deckID); err != nil {
		return fmt.Errorf("re-home surviving notes: %w", err)
	}
	if _, err := q.DeleteDeckRow(ctx, deckID); err != nil {
		return fmt.Errorf("delete deck: %w", err)
	}
	return nil
}

// RevokeDeckAccess deletes targetUserID's deck_access row on behalf of userID, refusing to strand
// the deck. Must be called inside a transaction it does not own; on ErrLastAccessHolder the caller
// rolls back.
//
// A deck's sole member cannot revoke their own access -- they delete the deck instead. Ownership
// (decks.owner_id) is not a permission source and exempts nobody: the guard counts deck_access
// flag holders only (docs/schema.md).
func RevokeDeckAccess(ctx context.Context, tx pgx.Tx, deckID, userID, targetUserID pgtype.UUID) error {
	q := New(tx)

	if _, err := q.LockDeckForAccessChange(ctx, LockDeckForAccessChangeParams{
		DeckID: deckID,
		UserID: userID,
	}); err != nil {
		return err
	}
	n, err := q.DeleteDeckAccessRow(ctx, DeleteDeckAccessRowParams{
		DeckID:       deckID,
		TargetUserID: targetUserID,
	})
	if err != nil {
		return fmt.Errorf("revoke deck access: %w", err)
	}
	if n == 0 {
		return pgx.ErrNoRows
	}
	return assertDeckStillManageable(ctx, q, deckID)
}

// SetDeckAccess overwrites targetUserID's six permission flags on behalf of userID, refusing to
// strand the deck. Same transaction contract as RevokeDeckAccess.
func SetDeckAccess(ctx context.Context, tx pgx.Tx, userID pgtype.UUID, arg UpdateDeckAccessRowParams) error {
	q := New(tx)

	if _, err := q.LockDeckForAccessChange(ctx, LockDeckForAccessChangeParams{
		DeckID: arg.DeckID,
		UserID: userID,
	}); err != nil {
		return err
	}
	n, err := q.UpdateDeckAccessRow(ctx, arg)
	if err != nil {
		return fmt.Errorf("update deck access: %w", err)
	}
	if n == 0 {
		return pgx.ErrNoRows
	}
	return assertDeckStillManageable(ctx, q, arg.DeckID)
}

// assertDeckStillManageable runs after the mutation rather than before it: one check then covers
// revocation, downgrade, and any path added later, and there is no check-then-act window. The deck
// row lock taken by the callers is what makes it race-free -- without it two concurrent revocations
// each see the other's holder still present and both commit.
func assertDeckStillManageable(ctx context.Context, q *Queries, deckID pgtype.UUID) error {
	holders, err := q.CountDeckAccessHolders(ctx, deckID)
	if err != nil {
		return fmt.Errorf("count deck access holders: %w", err)
	}
	if holders.ManageCount == 0 || holders.DeleteCount == 0 {
		return ErrLastAccessHolder
	}
	return nil
}
```

Two things to confirm against the actual `sqlc` output rather than assume, and adjust if they
differ (they are the only shapes here that codegen decides, not us):

- single-argument queries (`DeleteNotesOrphanedByDeckDelete`, `RehomeNotesOffDeck`,
  `DeleteDeckRow`, `CountDeckAccessHolders`) take a bare `pgtype.UUID`, not a Params struct;
- `CountDeckAccessHolders`'s row struct field names (`ManageCount`, `DeleteCount`) and their type
  (`int64`).

---

## 5. Doc updates

**`docs/schema.md`**

1. Replace the **"Open guard, not yet enforced"** paragraph (in the Access control block, after the
   six-flag table) with:
   > **Last-holder guard — enforced in the query layer.** A deck must always retain at least one
   > `can_manage_access` holder and one `can_delete` holder, or it is stranded with nobody able to
   > manage access or delete it. `internal/db/deletion.go`'s `RevokeDeckAccess` and `SetDeckAccess`
   > apply the mutation and then re-count holders inside the same transaction, under a row lock on
   > the deck, and return `ErrLastAccessHolder` for the caller to roll back on. It is an
   > authorisation rule, so it lives where the others live (CLAUDE.md §9) rather than in a trigger
   > — and a trigger would need the same lock to be race-free anyway. Consequence: a deck's sole
   > member cannot revoke their own access; they delete the deck. `decks.owner_id` is not a
   > permission source and exempts nobody.
2. Add a new **Deletion policy** section after Access control, holding the §0.2 FK table, the §0.5
   four-step deck-delete procedure with its post-condition argument, the §0.3 statement that user
   deletion is unsupported and why, and the §0.8 deferral of blob GC. This is the section future
   readers will look for; it should not require opening a plan file.
3. In the `review_log` code block, replace `-- FKs are ON DELETE RESTRICT: nothing may cascade
   training data away` with:
   ```
   -- user_id is ON DELETE RESTRICT permanently; card_id has NO FK (#51) -- see Deletion policy.
   -- Either way nothing ever cascades training data away, and now nothing can be made to.
   ```
4. Update the **"Indexes backing `ON DELETE RESTRICT`"** paragraph: `review_log`'s
   `(card_id, user_id, reviewed_at)` index no longer backs an RI check (there is no FK) and exists
   for the replay path; `cards.template_id` and `notes.note_type_id` still do; add that the new
   `CASCADE` FKs need the same index support and already have it (`cards.deck_id`, `cards.note_id`
   via `UNIQUE (note_id, ordinal)`, `user_card_state.card_id`, `deck_access.user_id`,
   `user_fsrs_params.deck_id`, `media_refs.deck_id` via its PK, `fields`/`templates` on
   `note_type_id`) — no new index is required by this change.
5. Re-tense the **card-regeneration trap** paragraph. It currently offers two outcomes ("either
   hits the `ON DELETE RESTRICT` on `review_log.card_id` and the edit fails outright, or …
   cascade-deletes its `user_card_state` silently"). After #51 only the second happens, and the
   history left behind is stranded from the re-created card, which gets a new id. Say so plainly;
   the fix and its owner (whichever change first makes cards stateful) are unchanged.
6. In **Media**, add one sentence: a deck delete cascades its `media_refs` away and can orphan a
   `media_blobs` row; nothing collects it yet, and reference-counted GC is deferred behind #60 and
   its own follow-up issue.
7. **Migrations** section — #50 already rewrites this for goose. Add one line: FK actions are part
   of the table definition, so a change to a deletion policy is an `ALTER TABLE … DROP CONSTRAINT
   … ADD CONSTRAINT …` migration once the original has merged.

**`docs/architecture.md` §20**

Replace the closing paragraph — currently *"One consequence to settle while doing it: … Deletion
policy is already unreachable behind the FK restricts (#15), so settle both at once."* — with a
resolved-and-implemented note:

> One consequence, settled in [#51](https://github.com/Jolls/deckshare/issues/51): deleting a deck
> deletes the cards filed in it, and a note goes only when it has **no cards left anywhere** — so a
> note whose cards span decks survives its home deck's deletion and is re-homed to the deck of its
> lowest-ordinal surviving card. That is not expressible as a static FK cascade, so `cards.deck_id`
> cascades while `notes.deck_id` restricts, and deck deletion runs as an ordered transaction in
> `internal/db/deletion.go`. `review_log` keeps every row: its `card_id` is not a foreign key, the
> same shape Anki's `revlog.cid` has, which is what lets a studied deck be deletable without any
> `DELETE` path over training data (CLAUDE.md §2.5). Full policy: docs/schema.md, Deletion policy;
> reasoning: `docs/plans/51-deletion-policy.md`. Filing `cards.deck_id` from `IrCard.deckAnkiId`
> remains #33's job — the schema is ready for it.

Also, in §11's build order or §1's current-state note (whichever #50 leaves in place), record that
step 2 now includes the deletion policy. One line, no restructuring.

**`docs/routes.md`**

1. Decks table, `POST /decks/{id}/delete`: replace *"Delete — currently unreachable behind FK
   restricts, see #51"* with *"Delete deck, its cards, and any note left with no cards anywhere;
   notes with cards in other decks are re-homed. Query layer: `db.DeleteDeck` (#51); handler is
   Phase 1 step 5."*
2. Notes table, `POST /notes/{id}/delete`: note that cards cascade at the FK level, so this is a
   single-statement delete; `review_log` rows for the deleted cards persist.
3. Note-types table, `POST /note-types/{id}/delete`: *"blocked while any note references it"* is now
   enforced by `notes.note_type_id ON DELETE RESTRICT`; fields and templates cascade. Say which.
4. Access section: replace the **"Open question"** paragraph with the enforced guard and its
   `409`-on-`ErrLastAccessHolder` handler contract, plus the two consequences from §0.6.
5. Open questions list at the bottom: delete item 2 (last-holder guard) and renumber; items 1 and 3
   stay open and untouched.
6. **Explicitly not routed**: add *"No account-deletion route. User deletion is blocked at the FK
   level by design (#51) — a user's decks, notes, note types, access grants, scheduling state, and
   review history all restrict. Account closure needs an ownership-transfer decision for shared
   decks and a written `review_log` decision first."*

**`CHANGELOG.md`** — one entry per PR (CLAUDE.md §14). If #50 and #51 land in the same PR, fold
these lines into that PR's single version entry rather than adding a second:

```
### Added
- Deletion policy: deck delete removes the deck's cards and any note left with no cards anywhere,
  re-homing notes whose cards survive in other decks; `internal/db/deletion.go` runs it as an
  ordered transaction ([#51](https://github.com/Jolls/deckshare/issues/51))
- Last-`can_manage_access`/`can_delete`-holder guard on `deck_access`, enforced in the query layer
  under a deck row lock ([#51](https://github.com/Jolls/deckshare/issues/51))
- DB-backed tests for the cascade graph, the deck-delete procedure, and the access guard

### Changed
- Foreign keys carry their terminal `ON DELETE` behaviour instead of the blanket `RESTRICT`:
  sessions, fields, templates, `deck_access.deck_id`, `cards.note_id`/`deck_id`,
  `user_card_state.card_id`, per-deck `user_fsrs_params`, and `media_refs.deck_id` cascade;
  everything user-owned restricts ([#51](https://github.com/Jolls/deckshare/issues/51))
- `review_log.card_id` is no longer a foreign key — the column, its index, and the re-import dedup
  key are unchanged, and no `review_log` row is deletable by any path. This is what makes a studied
  deck deletable without a `DELETE` over training data, and it matches Anki's own `revlog.cid`
  ([#51](https://github.com/Jolls/deckshare/issues/51))

### Removed
- User deletion as a reachable operation: blocked at the FK level pending an account-closure
  design ([#51](https://github.com/Jolls/deckshare/issues/51))
```

---

## 6. Tests

#50 adds a Postgres service to CI, exports `DATABASE_URL` at job level, and applies migrations
before `go test ./...` — so **#51 can write DB-backed tests, which #50's plan could not**, and it
needs no CI change to do it. This is schema/constraint work, so CLAUDE.md §5's "suggest, don't
auto-write" applies (the FSRS/`.apkg` always-ships-a-test exception does not) — **present this list
to the user and write what they agree to.** Recommendation: all of it. Every case below is a silent
data-loss failure if it regresses, and none of them are reachable through any other test.

`internal/db/deletion_test.go`, package `db`, colocated per architecture.md §4. Harness:

- `t.Skip` unless `DATABASE_URL` is set, so `go test ./...` stays green on a machine with no
  Postgres; CI always has it.
- One `pgxpool` for the package; **every test runs in a `pgx.Tx` that is always rolled back**, so
  tests neither pollute the dev database nor depend on each other.
- Fixtures via raw `tx.Exec`/`QueryRow … RETURNING id` in the test file. Do not add insert queries
  to `internal/db/queries/` for this — those belong to the CRUD issues that need them (#52–#59).

| # | Case | Assert |
|---|---|---|
| 1 | Deck delete, plain | Deck, its cards, its notes, and their `user_card_state` rows gone |
| 2 | Deck delete, note with a card in another deck | Note survives; `deck_id` = the other deck; the other card survives; the in-deck card gone |
| 3 | Deck delete, note homed elsewhere with all cards in the deleted deck | Note deleted (the case a naive `notes.deck_id`-scoped query misses) |
| 4 | Deck delete, note homed in the deck with no cards at all | Note deleted |
| 5 | **Deck delete with review history** | `review_log` row count unchanged; the rows' `card_id` values unchanged; deck and cards gone. *The crux of §0.4 — if this fails, the invariant is broken in one direction or the other* |
| 6 | Deck delete, fan-out | `deck_access` rows gone; per-deck `user_fsrs_params` gone; the user's global row (`deck_id IS NULL`) survives; `media_refs` gone; `media_blobs` row **survives** (§0.8) |
| 7 | Deck delete without `can_delete`, or with no `deck_access` row, or with a nonexistent deck id | `pgx.ErrNoRows` in all three; nothing deleted. Table-driven per CLAUDE.md §10.5 |
| 8 | Bare `DELETE FROM decks` with a note homed in it | FK violation on `notes_deck_id_fkey` — the §0.2 #10 tripwire |
| 9 | Note delete | Cards and `user_card_state` cascade; `review_log` rows survive |
| 10 | User delete with a deck / a note / a note type / an access row / review history | FK violation in every case (§0.3), table-driven per referencing table |
| 11 | User delete with only a session | Succeeds; the session row is gone (proves #1 cascades) |
| 12 | Note-type delete while a note references it | FK violation; with no notes, `fields` and `templates` cascade |
| 13 | Revoke the only `can_manage_access` holder | `ErrLastAccessHolder`; after rollback the row still exists |
| 14 | Revoke a holder while a second `can_manage_access` **and** `can_delete` holder exists | Succeeds |
| 15 | Revoke the only `can_delete` holder while another user holds `can_manage_access` | `ErrLastAccessHolder` — both counts are checked, not just one |
| 16 | `SetDeckAccess` clearing `can_manage_access` on the last holder | `ErrLastAccessHolder` (downgrade, not just removal) |
| 17 | Access change by a caller without `can_manage_access` | `pgx.ErrNoRows`; nothing changed |

Not tested here, deliberately: concurrency of two simultaneous revocations. The `FOR UPDATE` lock
is the mechanism, and a deterministic two-connection test for it is disproportionate to the risk at
this stage. If the user wants it, it needs two pools and a barrier — say so rather than writing a
flaky one.

---

## 7. Verification

Order matters — the migration edits must land before `sqlc generate`, and codegen before the Go.

1. Edit the 12 migration files (§2).
2. Fresh database, full cycle:
   ```
   docker compose down -v && docker compose up -d db
   goose -dir migrations postgres "$DATABASE_URL" up
   goose -dir migrations postgres "$DATABASE_URL" status      # 14 applied, none pending
   goose -dir migrations postgres "$DATABASE_URL" down-to 0
   goose -dir migrations postgres "$DATABASE_URL" up
   ```
3. Confirm the actual FK actions against the intended table, rather than reading the DDL back to
   yourself — this is the check that catches an edit missed in one of twelve files:
   ```sql
   SELECT c.conrelid::regclass AS tbl, c.conname, c.confdeltype
   FROM pg_constraint c WHERE c.contype = 'f' ORDER BY 1, 2;
   ```
   Expect exactly **20** rows (21 minus the dropped `review_log_card_id_fkey`): `confdeltype = 'c'`
   for the nine cascades in §0.2, `'r'` for the eleven restricts, and no row whose `conrelid` is
   `review_log` and `conname` mentions `card_id`.
4. `sqlc generate`; commit the output. Confirm `internal/db/models.go` is unchanged by the diff.
5. Write `internal/db/deletion.go` and the tests the user agreed to.
6. `go build ./... && go vet ./... && golangci-lint run && go test ./...`.
7. Re-read `docs/schema-diagram.md` and confirm it still matches (it records columns, not FK
   actions, so it should need no edit — confirm rather than assume).
8. Manual spot-check, because it is the one behaviour a reader will not believe from the DDL alone:
   create a deck with a note whose two cards sit in two different decks, delete one deck, and
   confirm the note survives with the surviving card and a re-homed `deck_id`.

Per CLAUDE.md §14, this diff is schema + cross-cutting: recommend `/code-review high` before
commit, and pause for the user's manual testing.

Follow-up issues to file with the PR (neither is built here): **account deletion / data export
policy** (§0.3) and **orphaned media blob GC** (§0.8), the latter blocked on #60.

---

## 8. Open questions for the user

Two confirmations, no unresolved design.

1. **Dropping the `review_log.card_id` foreign key (§0.4) overrides an explicit instruction in
   #50's plan** ("#51 must not relax them"). It is the only way to satisfy both architecture.md
   §20 and CLAUDE.md §2.5 without soft-deleting decks, it keeps every `review_log` row
   undeletable, and it matches Anki's `revlog.cid`. **If vetoed**, the consequence must be accepted
   explicitly and documented in schema.md and routes.md rather than discovered later: deck delete
   fails whenever any card in the deck has ever been answered, so a studied deck can never be
   deleted; `DeleteDeck` then needs a distinct `ErrDeckHasReviewHistory` (handler: 409) and a
   pre-flight `EXISTS` check so the failure is a clear message rather than a raw FK violation; and
   architecture.md §20's "deleting a deck deletes its cards" acquires a permanent exception.
2. **Whether #50 and #51 land in one PR.** If yes (the assumption throughout — this batch is
   implementing both on one branch, one PR), §2's in-place edits apply. If #50 has already merged
   to `main`, switch to Appendix A unchanged — no other part of this plan moves.

---

## Appendix A — fallback: `migrations/00015_deletion_policy.sql`

Use **only** if #50 has already merged to `main` (§0.1). Created with
`goose -dir migrations create -s deletion_policy sql`. Constraint names are Postgres's automatic
`<table>_<column>_fkey`, which #50's inline `REFERENCES` clauses produce.

```sql
-- +goose Up
-- Replaces #50's provisional blanket ON DELETE RESTRICT with the terminal deletion policy.
-- Reasoning per constraint: docs/plans/51-deletion-policy.md §0.2. Deck deletion itself is an
-- ordered transaction (internal/db/deletion.go), not a cascade -- "a note goes when it has no
-- cards left ANYWHERE" is not expressible as a static FK action, which is why notes.deck_id
-- stays RESTRICT while cards.deck_id cascades.

ALTER TABLE sessions DROP CONSTRAINT sessions_user_id_fkey;
ALTER TABLE sessions ADD CONSTRAINT sessions_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;

ALTER TABLE fields DROP CONSTRAINT fields_note_type_id_fkey;
ALTER TABLE fields ADD CONSTRAINT fields_note_type_id_fkey
    FOREIGN KEY (note_type_id) REFERENCES note_types (id) ON DELETE CASCADE;

ALTER TABLE templates DROP CONSTRAINT templates_note_type_id_fkey;
ALTER TABLE templates ADD CONSTRAINT templates_note_type_id_fkey
    FOREIGN KEY (note_type_id) REFERENCES note_types (id) ON DELETE CASCADE;

ALTER TABLE deck_access DROP CONSTRAINT deck_access_deck_id_fkey;
ALTER TABLE deck_access ADD CONSTRAINT deck_access_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE CASCADE;

ALTER TABLE cards DROP CONSTRAINT cards_note_id_fkey;
ALTER TABLE cards ADD CONSTRAINT cards_note_id_fkey
    FOREIGN KEY (note_id) REFERENCES notes (id) ON DELETE CASCADE;

ALTER TABLE cards DROP CONSTRAINT cards_deck_id_fkey;
ALTER TABLE cards ADD CONSTRAINT cards_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE CASCADE;

ALTER TABLE user_card_state DROP CONSTRAINT user_card_state_card_id_fkey;
ALTER TABLE user_card_state ADD CONSTRAINT user_card_state_card_id_fkey
    FOREIGN KEY (card_id) REFERENCES cards (id) ON DELETE CASCADE;

ALTER TABLE user_fsrs_params DROP CONSTRAINT user_fsrs_params_deck_id_fkey;
ALTER TABLE user_fsrs_params ADD CONSTRAINT user_fsrs_params_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE CASCADE;

ALTER TABLE media_refs DROP CONSTRAINT media_refs_deck_id_fkey;
ALTER TABLE media_refs ADD CONSTRAINT media_refs_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE CASCADE;

-- review_log.card_id loses its foreign key entirely. The column, its NOT NULL, its index, and
-- UNIQUE (user_id, card_id, anki_id) are all unchanged, and no review_log row becomes deletable by
-- any path -- the opposite: no delete anywhere in this schema can remove one, and none can be made
-- to. RESTRICT here would have made any deck that had ever been studied permanently undeletable,
-- colliding with architecture.md §20. Anki's revlog.cid is not a foreign key either
-- (CLAUDE.md §2.10). See docs/plans/51-deletion-policy.md §0.4.
ALTER TABLE review_log DROP CONSTRAINT review_log_card_id_fkey;

-- review_log.user_id keeps ON DELETE RESTRICT, permanently. Every other FK not named above keeps
-- RESTRICT as its terminal answer, not as a stopgap.

-- +goose Down
ALTER TABLE review_log ADD CONSTRAINT review_log_card_id_fkey
    FOREIGN KEY (card_id) REFERENCES cards (id) ON DELETE RESTRICT;

ALTER TABLE media_refs DROP CONSTRAINT media_refs_deck_id_fkey;
ALTER TABLE media_refs ADD CONSTRAINT media_refs_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE RESTRICT;

ALTER TABLE user_fsrs_params DROP CONSTRAINT user_fsrs_params_deck_id_fkey;
ALTER TABLE user_fsrs_params ADD CONSTRAINT user_fsrs_params_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE RESTRICT;

ALTER TABLE user_card_state DROP CONSTRAINT user_card_state_card_id_fkey;
ALTER TABLE user_card_state ADD CONSTRAINT user_card_state_card_id_fkey
    FOREIGN KEY (card_id) REFERENCES cards (id) ON DELETE RESTRICT;

ALTER TABLE cards DROP CONSTRAINT cards_deck_id_fkey;
ALTER TABLE cards ADD CONSTRAINT cards_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE RESTRICT;

ALTER TABLE cards DROP CONSTRAINT cards_note_id_fkey;
ALTER TABLE cards ADD CONSTRAINT cards_note_id_fkey
    FOREIGN KEY (note_id) REFERENCES notes (id) ON DELETE RESTRICT;

ALTER TABLE deck_access DROP CONSTRAINT deck_access_deck_id_fkey;
ALTER TABLE deck_access ADD CONSTRAINT deck_access_deck_id_fkey
    FOREIGN KEY (deck_id) REFERENCES decks (id) ON DELETE RESTRICT;

ALTER TABLE templates DROP CONSTRAINT templates_note_type_id_fkey;
ALTER TABLE templates ADD CONSTRAINT templates_note_type_id_fkey
    FOREIGN KEY (note_type_id) REFERENCES note_types (id) ON DELETE RESTRICT;

ALTER TABLE fields DROP CONSTRAINT fields_note_type_id_fkey;
ALTER TABLE fields ADD CONSTRAINT fields_note_type_id_fkey
    FOREIGN KEY (note_type_id) REFERENCES note_types (id) ON DELETE RESTRICT;

ALTER TABLE sessions DROP CONSTRAINT sessions_user_id_fkey;
ALTER TABLE sessions ADD CONSTRAINT sessions_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT;
```

Note if this path is taken: the `Down` re-adding `review_log_card_id_fkey` fails if any
`review_log` row references a deleted card — correct and intended. A down-migration that would
have to delete training data to succeed must fail instead.
