# #223 — Suspend, bury and flag a card: write path

## Summary

`user_card_state.suspended` / `.buried_until` / `.flag` exist and are honoured by every study
query, but nothing writes them. This plan adds:

1. A `flag` range CHECK constraint (migration, `smallint`, Anki's 0-7).
2. One route, `POST /decks/{id}/cards/state`, that upserts `suspended`, `buried_until`, and/or
   `flag` on the caller's own `user_card_state` row, authorised by `can_study` (not
   `can_edit_content` — this is the caller's own scheduling state, not deck content, §2.1).
3. A reviewer control (suspend / bury-until-tomorrow / cycle-flag buttons) that skips the card
   client-side without going through the grade batch endpoint.
4. A per-note "Suspend" control in the deck's note list, applying to all of that note's cards,
   visible to any `can_study` holder (independent of the existing `can_edit_content`-gated bulk
   toolbar).
5. Access-control test rows (CLAUDE.md §10 item 5).

Sibling-card auto-bury is explicitly out of scope (per the issue and per instruction).

## Decision: one route, not four

`internal/http`'s existing mutate routes are one verb-suffixed route per action
(`POST /decks/{id}/access/{userId}/edit`, `/decks/{id}/notes/bulk-tag-add`,
`/decks/{id}/flags/{flagId}/resolve`, `/decks/{id}/cards/flags`) — never one endpoint branching
on a body field. That argues for separate routes. But `flags.go`'s create route
(`POST /decks/{id}/cards/flags`) is the closer analogue: a card-scoped, `can_study`-gated,
htmx-driven write from inside the reviewer, deck id bound in the path, card id riding as a form
field because the reviewer only knows the current card client-side. This plan follows that
shape and uses **one route, `POST /decks/{id}/cards/state`**, taking `cardId` plus whichever of
`suspend` (bool), `bury` (bool, meaning "until tomorrow"), `unsuspend` (bool), and `flag` (int
0-7) fields are present in the form — because the reviewer's three controls (suspend, bury,
flag) all write the same row and the client already knows which one it's asking for, so a
single upsert handler that reads whichever fields are present is simpler than four handlers
each re-deriving the same row and re-running the same authorisation join, and it matches the
issue's own suggested shape. This mirrors `flags.go`'s `POST .../cards/flags` route directly
(same deck-scoped path shape, same `can_study` gate, same form-field-carries-card-id pattern)
rather than inventing a new style.

## Migration

**New file: `migrations/00021_user_card_state_flag_check.sql`** (00021 is the next unused
number; `00019_card_flags.sql` and `00020_notes_release_day.sql` are the last two).

```sql
-- +goose Up
-- #223: flag is a plain Anki-style colour code (0 = none, 1-7 = the seven flag colours), not the
-- comment-flag of #207/card_flags -- that's a different feature on a different table. No prior
-- CHECK exists (migration 00010), so out-of-range values have been silently accepted; add the
-- constraint with the first code path that ever writes this column.
ALTER TABLE user_card_state
    ADD CONSTRAINT user_card_state_flag_check CHECK (flag BETWEEN 0 AND 7);

-- +goose Down
ALTER TABLE user_card_state DROP CONSTRAINT user_card_state_flag_check;
```

No new columns — the issue is explicit that the three already exist.

## Query: `internal/db/queries/user_card_state.sql`

Add one upsert. `due` is `NOT NULL` with no default (migration 00010), so a never-seen card's
first insert must supply it; every other omittable column (`stability`, `difficulty`, `state`,
`reps`, `lapses`, `elapsed_days`, `scheduled_days`, `learning_steps`) has a `NOT NULL DEFAULT`
and needs no value here. On first insert, `due` is set to `now()` — the same "new card" zero
state `GradeBatch`'s never-seen path assumes (go-fsrs zero value, `state=0`). A `suspend` or
`flag` write on a never-seen card must not fabricate a fake `due` in the past/future that would
make the row look reviewed; `now()` is neutral and gets overwritten by the first real grade
(`UpsertUserCardStateOnReview`, which sets `due` unconditionally on every successful grade).

`COALESCE(..., column)` on every mutable column means "leave unchanged unless this call is
setting it" — the handler passes `pgtype.Bool{}`/`pgtype.Int2{}` (invalid/null) for fields the
request didn't touch, so `ON CONFLICT DO UPDATE` only touches the columns the request named.
This is a settings write, not a scheduling write (per the existing doc comment on
`UpsertUserCardStateOnReview`: "suspended / buried_until / flag ... are never touched here"), so
it does **not** go through the last-write-wins-by-review-time guard — it is unconditional,
matching `UpsertUserCardStateFromReplay`'s unconditional shape rather than the guarded one.

```sql
-- Suspend/unsuspend/bury/flag a card (#223): a settings write on the caller's own row, not a
-- scheduling write, so unlike UpsertUserCardStateOnReview this is unconditional -- there is no
-- "newer review wins" question for a column FSRS never touches. A never-seen card has no row
-- yet (migration 00010's PK is (user_id, card_id)), so this must upsert; due defaults to now()
-- on first insert, the same neutral zero-state UpsertUserCardStateOnReview's own first write
-- would produce, and is overwritten by the first real grade. sqlc.narg columns are NULL when the
-- caller isn't setting that field, and COALESCE(EXCLUDED.x, user_card_state.x) on the UPDATE arm
-- leaves an unset field untouched rather than clobbering it back to a zero value.
-- name: UpsertUserCardStateSettings :one
INSERT INTO user_card_state (user_id, card_id, due, suspended, buried_until, flag)
VALUES (
    sqlc.arg(user_id), sqlc.arg(card_id), now(),
    COALESCE(sqlc.narg(suspended)::boolean, false),
    sqlc.narg(buried_until)::date,
    COALESCE(sqlc.narg(flag)::smallint, 0)
)
ON CONFLICT (user_id, card_id) DO UPDATE SET
    suspended    = COALESCE(sqlc.narg(suspended)::boolean, user_card_state.suspended),
    buried_until = CASE WHEN sqlc.arg(clear_buried)::boolean THEN NULL
                        ELSE COALESCE(sqlc.narg(buried_until)::date, user_card_state.buried_until) END,
    flag         = COALESCE(sqlc.narg(flag)::smallint, user_card_state.flag)
RETURNING *;
```

Note on `buried_until`'s `NULL` ambiguity: `sqlc.narg(buried_until)` being `NULL` must mean two
different things depending on the request — "don't touch it" (a suspend-only or flag-only
call) versus "un-bury it" (an explicit unbury). A single nullable arg can't distinguish those,
so a separate `clear_buried` boolean arg disambiguates: the handler sets it `true` only for an
explicit unbury action, `false` otherwise (including when `buried_until` is being set to a real
date). This keeps the query's three settings columns independent, matching the route's "set
whichever fields are present" contract. `unsuspend` needs no such flag — it is spelled as
`suspended = false`, an ordinary (non-NULL) value, so `sqlc.narg(suspended)` already
distinguishes "leave alone" (NULL) from "set false" (false) without help.

`GetUserCardState` (already exists, `user_card_state.sql:1`) is reused unchanged for the
"resolve card + read current row before responding" step described below; no new read query is
needed there.

## Handler: `internal/http/cards_state.go` (new file)

New file rather than adding to `flags.go` — a different concern (suspend/bury/flag settings,
not comment-flag feedback) even though the route shape is copied from it; `flags.go`'s own doc
comment already scopes it to #207/`card_flags`.

```go
package http

// registerCardStateRoutes wires POST /decks/{id}/cards/state (#223): suspend/unsuspend/bury/
// flag a card, writing the caller's own user_card_state row. can_study, not can_edit_content --
// this writes the caller's own scheduling state, not deck content (§2.1), and needs no new
// access flag. Called from the reviewer (review.js) via a plain POST (not htmx-fragment-typed
// like flags.go's create route, since the client applies the result to its own in-memory queue
// rather than swapping in server-rendered HTML) and from the deck note list's per-note control.
func registerCardStateRoutes(mux *http.ServeMux, store db.Beginner) {
	mux.Handle("POST /decks/{id}/cards/state", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}
		var cardID pgtype.UUID
		if err := cardID.Scan(r.PostForm.Get("cardId")); err != nil {
			badRequest(w)
			return
		}

		q := db.New(store)
		// Authorise-and-fetch, same shape as flags.go's GetCardForFlag: confirms card_id belongs
		// to deck_id and the caller holds can_study on it. A card missing, invisible, or
		// unstudyable all collapse to zero rows -> pgx.ErrNoRows -> 404 (docs/schema.md).
		card, err := q.GetCardForFlag(r.Context(), db.GetCardForFlagParams{
			UserID: user.ID, CardID: cardID, DeckID: deckID,
		})
		if handleQueryErr(w, r, err) {
			return
		}

		params := db.UpsertUserCardStateSettingsParams{UserID: user.ID, CardID: card.ID}
		switch {
		case r.PostForm.Has("suspend"):
			params.Suspended = pgtype.Bool{Bool: true, Valid: true}
		case r.PostForm.Has("unsuspend"):
			params.Suspended = pgtype.Bool{Bool: false, Valid: true}
		}
		if r.PostForm.Has("bury") {
			day, err := studyTomorrow(r.Context(), q, user.ID, time.Now())
			if err != nil {
				serverError(w, r, err)
				return
			}
			params.BuriedUntil = pgtype.Date{Time: day, Valid: true}
		} else if r.PostForm.Has("unbury") {
			params.ClearBuried = true
		}
		if raw := r.PostForm.Get("flag"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 7 {
				badRequest(w)
				return
			}
			params.Flag = pgtype.Int2{Int16: int16(n), Valid: true}
		}
		if !params.Suspended.Valid && !params.BuriedUntil.Valid && !params.ClearBuried && !params.Flag.Valid {
			badRequest(w) // no recognised field present
			return
		}

		row, err := q.UpsertUserCardStateSettings(r.Context(), params)
		if err != nil {
			serverError(w, r, err)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"suspended":   row.Suspended,
			"buriedUntil": row.BuriedUntil.Time.Format("2006-01-02"),
			"flag":        row.Flag,
		})
	})))
}
```

`studyTomorrow` (new small helper, same file):

```go
// studyTomorrow computes "one day past the caller's own study day" (issue #223's bury
// semantics): GetStudyDayWindow (reviews.sql) is the one place the user's timezone/
// day_start_hour arithmetic already lives (architecture.md §5, reused rather than
// reimplemented per CLAUDE.md §9's day-boundary rule); ListDueCardsForStudy/CountQueueForDeck
// then compare buried_until against `study_day_start::date` -- a plain cast on the timestamptz,
// not GetStudyDayWindow's own study_day_local_date column, which is computed via a different
// (AT TIME ZONE) path and is not guaranteed to agree with a plain ::date cast under a
// non-UTC session timezone. Mirroring the read filters' exact cast, in the same query, is what
// keeps this write's idea of "tomorrow" identical to what the read side will later test it
// against, regardless of what the session's TimeZone GUC happens to be.
func studyTomorrow(ctx context.Context, q *db.Queries, userID pgtype.UUID, now time.Time) (time.Time, error) {
	window, err := q.GetStudyDayWindow(ctx, db.GetStudyDayWindowParams{
		UserID: userID, Now: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return time.Time{}, err
	}
	return window.StudyDayStart.Time, nil // see below: the +1 day happens in SQL, not here
}
```

Correction to keep the cast identical to the read side: rather than adding a Go-side `+24h`
(which risks disagreeing with Postgres's own `::date` truncation under DST at a boundary),
`UpsertUserCardStateSettings`'s `buried_until` argument should itself be the *already-shifted*
timestamptz, computed the same way the read queries compare it — i.e. the handler passes
`study_day_start + 24h` as a `timestamptz` and the query casts it, so:

```sql
    COALESCE((sqlc.narg(buried_until)::timestamptz)::date, ...)
```

with the handler setting `params.BuriedUntil = pgtype.Timestamptz{Time: window.StudyDayStart.Time.Add(24*time.Hour), Valid: true}`
(param type becomes `pgtype.Timestamptz`, and the query's INSERT/UPDATE both cast it with
`::date` — matching `ucs.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date` in
`reviews.sql` byte-for-byte in cast shape). This is the version to implement; the plain
`sqlc.narg(buried_until)::date` shown in the query block above should be changed to
`(sqlc.narg(buried_until)::timestamptz)::date` accordingly, and `studyTomorrow` above returns
`window.StudyDayStart.Time.Add(24 * time.Hour)`, not `window.StudyDayStart.Time` unmodified.

`respondJSON` — check whether an existing small JSON-response helper exists in `internal/http`
(`respond.go`) before adding one; if none exists, add a two-line one there (`w.Header().Set
("Content-Type", "application/json"); w.WriteHeader(status); json.NewEncoder(w).Encode(body)`),
since the reviewer's fetch call needs the resulting `suspended`/`buriedUntil`/`flag` values back
to update its own UI state, unlike `flags.go`'s route which only needs a rendered confirmation
string.

Register in `internal/http/http.go` alongside the other `register*Routes` calls (any order is
fine; the existing calls are not alphabetised or dependency-ordered).

### Note-list bulk action: `internal/http/notes.go` / new query

**Design decision** (not left open, since the existing note-list mechanics settle it): the
note list (`deck.html`) shows one row per **note**, with a `CardCount` column — a note can back
more than one card (per note-type template), and the list exposes no per-card id today. Adding
true per-card granularity to that table is a larger UI change than this issue's "ideally"
scope calls for. This plan instead adds a **per-note "Suspend"/"Unsuspend" toggle** that applies
to every card generated from that note, for the current viewer only — the same grain the
existing bulk actions already use for notes → cards (`bulk-delete` cascades to a note's cards
via the FK; `bulk-release-day` sets a note-level property that every one of its cards reads).
Bury and flag are per-card concepts in Anki and are not exposed at note grain here — only
suspend, which is the one of the three that meaningfully applies "to a note's cards" as a unit
(you suspend a broken note's cards together; you don't bury or colour-flag them together). This
keeps the note-list control small and consistent with what already exists, and defers a
per-card note-list UI to a future issue if wanted.

Gating: **`can_study`, independent of `can_edit_content`.** The existing bulk toolbar
(`{{if .Deck.CanEditContent}}`) is content editing and must stay separate; suspend is the
viewer's own study state, so it must render for a `can_study`-only viewer (a student on a
shared deck) too, not just an editor. Add a new per-row cell/column in `deck.html`, gated on
`.Deck.CanStudy`, independent of the `CanEditContent`-gated checkbox column:

```html
<td>{{if $.Deck.CanStudy}}
    <form method="post" action="/decks/{{$.Deck.ID}}/notes/{{.ID}}/suspend-cards" style="display:inline">
        <button type="submit" class="outline btn-sm">{{if .AllSuspended}}Unsuspend{{else}}Suspend{{end}}</button>
    </form>
{{end}}</td>
```

This needs `ListNotesInDeck` (`notes.sql`) to also report, per note, whether every one of its
cards is currently suspended *for the requesting user* (`AllSuspended bool`), so the button can
show the right label. Add a `LEFT JOIN user_card_state ucs ON ucs.card_id = c.id AND ucs.user_id
= sqlc.arg(user_id)` plus a `bool_and(COALESCE(ucs.suspended, false))` aggregate (or
`every(...)`) to that query, grouped per note — exact column list to be written against
`notes.sql`'s current `ListNotesInDeck` body at implementation time (not reproduced here since
this file wasn't opened during planning; the implementer should read it first and add the
aggregate without altering its existing pagination/columns).

New route, `internal/http/notes.go`:

```go
mux.Handle("POST /decks/{deckId}/notes/{id}/suspend-cards", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    user, _ := auth.UserFromContext(r.Context())
    deckID, ok := pathUUID(r, "deckId")
    if !ok { notFoundPage(w, pages, user); return }
    noteID, ok := pathUUID(r, "id")
    if !ok { notFoundPage(w, pages, user); return }
    q := db.New(store)
    // Same can_study-authorise-and-fetch shape as GetCardForFlag, but for every card under one
    // note; toggles based on the note's current all-suspended state so double-submits are inert.
    if _, err := q.ToggleSuspendCardsForNote(r.Context(), db.ToggleSuspendCardsForNoteParams{
        UserID: user.ID, NoteID: noteID, DeckID: deckID,
    }); err != nil {
        serverError(w, r, err)
        return
    }
    http.Redirect(w, r, "/decks/"+deckID.String()+"#notes", http.StatusSeeOther)
})))
```

New query, `internal/db/queries/notes.sql` (append near the other bulk queries):

```sql
-- #223: suspend (or unsuspend, toggling) every card under one note, for the caller's own
-- user_card_state rows only. Authorised on can_study (not can_edit_content, unlike the other
-- bulk-* routes in this file, which edit deck content) -- this writes the caller's own
-- scheduling state. A never-seen card has no row yet, so this upserts one per card with the
-- same due=now() neutral default as UpsertUserCardStateSettings.
-- name: ToggleSuspendCardsForNote :execrows
WITH target_cards AS (
    SELECT c.id AS card_id
    FROM cards c
    JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                       AND da.can_view AND da.can_study
    WHERE c.note_id = sqlc.arg(note_id) AND c.deck_id = sqlc.arg(deck_id)
), currently AS (
    SELECT bool_and(COALESCE(ucs.suspended, false)) AS all_suspended
    FROM target_cards tc
    LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = tc.card_id
)
INSERT INTO user_card_state (user_id, card_id, due, suspended)
SELECT sqlc.arg(user_id), tc.card_id, now(), NOT currently.all_suspended
FROM target_cards tc, currently
ON CONFLICT (user_id, card_id) DO UPDATE
SET suspended = EXCLUDED.suspended;
```

This is intentionally its own upsert (not a call to `UpsertUserCardStateSettings` in a loop) —
it needs to flip every card to the *same* new state (all-suspend or all-unsuspend) computed
once from their current combined state, which a per-card loop calling the single-row upsert
could not do atomically without a first read pass.

## Reviewer UI: `web/templates/review.html`

Add controls beside the existing flag control (same `<section id="review-stage">`, same
`class="flag-control"` block or a sibling `<p class="state-controls">`):

```html
<p class="state-controls">
  <button type="button" data-suspend>Suspend</button>
  <button type="button" data-bury>Bury</button>
  <button type="button" data-flag-cycle>🚩</button>
</p>
```

`data-flag-cycle` cycles through a small fixed set of flag values on repeated clicks (e.g.
0 → 1 → 0, "flagged"/"not flagged" — Anki's 7-colour picker is out of scope for a single button;
see Open Questions) rather than exposing all 8 values, since the issue only requires *a*
control, not colour selection UI.

## Reviewer JS: `web/static/review.js`

Add handling in `onStageClick` (same function that already dispatches on
`[data-flag-toggle]`/`[data-flag-cancel]`, `review.js:263`) for `[data-suspend]`,
`[data-bury]`, `[data-flag-cycle]`. Each:

1. Reads `state.queue[state.current]` for the current card (same access `grade()` uses,
   `review.js:311`).
2. POSTs to `/decks/{deckId}/cards/state` (deck id already available via
   `script[data-deck-id]`, same attribute `deckshareReview.deckId()` already reads) with
   `cardId` and the relevant field (`suspend=1`, `bury=1`, or `flag=<n>`).
3. On success, marks `card.done = true` and calls `showNext()` — the same two bookkeeping
   calls `grade()` makes (`review.js:316`, `:331`) — but **does not** push to `state.pending`,
   does not dispatch `card-graded`, and does not call `scheduleFlush()`: suspend/bury/flag are
   not reviews and must never reach `POST /api/reviews/batch` or `review_log` (§2.7 — the
   client only ever asserts *what happened*, and suspending isn't a grade).
4. On failure, leaves the card in place and shows `#review-error` (already present,
   `review.html:7`) rather than silently advancing past a card whose suspend/bury didn't
   actually take.

Flag-cycle additionally needs to update the button's own label/state to reflect the new value
returned by the endpoint (the JSON response's `flag` field), matching `resetFlagControl`'s
pattern of resetting per-card UI state when `showNext()` swaps in a new card.

## Access-control tests: `internal/http/cards_state_test.go` (new file)

Following `flags_test.go`'s exact shape (`TestFlagRoutes_GoldenPath` /
`TestFlagRoutes_AccessControl`, using `setupOneCard`, `loginCookie`, `doRequest`, `countRows`
helpers already in the test package):

- **Golden path**: student with `can_study` suspends a never-seen card (no prior
  `user_card_state` row) → 200, row created with `suspended = true`, other columns at their
  defaults (`due` present, `state = 0`). Unsuspend on the same card → row updated, `suspended =
  false`. Bury → `buried_until` set to tomorrow's study day per `GetStudyDayWindow`; assert via
  a direct row read against the same SQL expression the read queries use
  (`(buried_until)::date` compared to `(study_day_start)::date`), not by re-deriving the date in
  Go with `time.Now()`, which could disagree with the DB's session timezone assumption --- the
  test should call the same `GetStudyDayWindow` query itself for its expected value, not
  duplicate the arithmetic. Flag → `flag` set to the requested value 0-7.
- **Flag range**: `flag=8` and `flag=-1` → 400 (or a DB constraint violation surfaced as 500 if
  the handler doesn't pre-validate — the plan's handler above validates in Go before the query
  runs, so this should be 400).
- **Access control table**, mirroring `TestFlagRoutes_AccessControl`:
  - stranger (no `deck_access` row) → 404
  - view-only (`can_view` only, no `can_study`) → 404
  - student (`can_view` + `can_study`) → 200
  - manager (`can_manage_access` only, no `can_study`) → 404
  - a card real but belonging to a different deck than the URL's `{id}` → 404 (the URL's deck id
    is bound into `GetCardForFlag`'s join, same as `flags.go` reuses it for)
- **Never-seen-card upsert**: assert no `pgx.ErrNoRows`/constraint violation when suspending a
  card the user has never reviewed (this is the scenario the issue calls out explicitly:
  "A never-seen card has no user_card_state row, so suspending one has to upsert, not update").
- **Note-list suspend-cards**: golden path (note with 2 cards, both suspended together, then
  unsuspended together) + the same access-control table shape, in
  `internal/http/notes_test.go` (existing file, alongside the other bulk-action tests) rather
  than a new file, matching where `bulk-delete`/`bulk-tag-add` tests already live.

## CHANGELOG.md

Add an `### Added` bullet under the next `## [0.3.11] - YYYY-MM-DD` entry (current head is
`[0.3.10] - 2026-09-10`; patch increments per PR per CLAUDE.md §14):

```
### Added
- Suspend, bury, and flag controls: a route to write `user_card_state.suspended`/
  `.buried_until`/`.flag` (previously read-only dead columns), plus reviewer and note-list
  controls ([#223](https://github.com/Jolls/deckshare/issues/223))
```

## docs/architecture.md §20

No new row needed — nothing here diverges from Anki's model (suspend/bury/flag are Anki
concepts; scoping them to `user_card_state` per-user rather than per-card is already covered by
the existing §2.1 seam, not a new divergence). Sibling-card auto-bury, if built later, is what
would earn a §20 row per the issue's own note — not this plan.

## File change summary

| File | Change |
|---|---|
| `migrations/00021_user_card_state_flag_check.sql` | New. `CHECK (flag BETWEEN 0 AND 7)`. |
| `internal/db/queries/user_card_state.sql` | Add `UpsertUserCardStateSettings :one`. |
| `internal/db/queries/notes.sql` | Add `ToggleSuspendCardsForNote :execrows`; extend `ListNotesInDeck` with an `AllSuspended` aggregate for the current user. |
| `internal/http/cards_state.go` | New. `registerCardStateRoutes`, `studyTomorrow` helper. |
| `internal/http/http.go` | Register `registerCardStateRoutes`. |
| `internal/http/notes.go` | Add `POST /decks/{deckId}/notes/{id}/suspend-cards`. |
| `internal/http/respond.go` | Add a small JSON-response helper if none exists yet. |
| `web/templates/review.html` | Add suspend/bury/flag-cycle buttons. |
| `web/templates/deck.html` | Add per-note suspend/unsuspend button, gated on `.Deck.CanStudy`. |
| `web/static/review.js` | Handle the three new buttons in `onStageClick`; skip-card bookkeeping without touching the grade/flush path. |
| `internal/http/cards_state_test.go` | New. Golden path + access-control table + never-seen-card upsert. |
| `internal/http/notes_test.go` | Add suspend-cards golden path + access-control rows. |
| `CHANGELOG.md` | `### Added` bullet under the next `0.3.x` entry. |
| (regenerate) `internal/db/*.sql.go`, `models.go` | Via `go generate` after the `.sql` changes — do not hand-edit. |

## Resolved decisions

1. **Flag UI: single cycling toggle, confirmed.** No multi-value/colour picker in this pass.
2. **`ListNotesInDeck` column list for `AllSuspended`:** left for the implementer to fit against
   the query's current body (read `notes.sql` first, add the aggregate without disturbing
   existing pagination/cursor columns) — this is mechanical, not a judgment call.
