# Phase 1 — track plan

**Prerequisites:** `01-scaffold.md` merged

Everything after the scaffold. Three tracks run in parallel, then converge on the reviewer.
Each track below is a session-startable unit; file one issue per heading.

---

## Dependency graph

```
        Step 1 scaffold  (blocks all)
                 │
      ┌──────────┼──────────┐
      │          │          │
   Track A    Track B    Track C
   schema     lib/fsrs   lib/render
      │          │          │
   auth ──┐      │          │
      │   │      │          │
   CRUD ──┴──────┴──────────┘
                 │
        reviewer + write queue
                 │
        apkg import → export
                 │
        parameter optimisation
```

A, B, and C touch disjoint directories and — because Step 1 front-loaded dependencies —
disjoint files. Run them as separate branches, or concurrent worktrees (`EnterWorktree`) if
you want them genuinely simultaneous. **A is the long pole**: it continues into auth and
CRUD while B and C finish in a session or two each.

---

## Track A — Schema

**Model: Opus · Effort: high** · Labels: `area: db`

The load-bearing step. Explicit Opus trigger (CLAUDE.md §7), and the one place where "looks
fine" and "is fine" diverge silently.

Implement `docs/schema.md` in full in `src/lib/server/db/schema.ts` — **including
`deck_access` and the `user_id` in `user_card_state`**, even though Phase 1 has one user per
deck. Those columns are free now and structural later; adding them after users exist is the
rewrite CLAUDE.md §2.1 exists to prevent.

Checklist per `docs/schema.md`: content tables carry no scheduling state · UUIDv7 ids ·
`anki_id` preserved for export fidelity · `UNIQUE (deck_id, guid)` · `review_log` append-only
· `user_fsrs_params.params` as JSON + explicit `fsrs_version` · the partial index
`(user_id, due) WHERE NOT suspended` · `users.timezone` + `day_start_hour`.

Also implement the day-boundary query helper (a per-user 04:00-local rollover, not midnight
UTC) — it's small, it belongs with the schema, and every queue query depends on it.

**Verify:** migration applies to a fresh database; a test asserts `cards` has no scheduling
columns; a test asserts inserting the same `(deck_id, guid)` twice updates rather than
duplicates; day-boundary helper has tests across a DST transition and a timezone change.

**Review before merge:** `/code-review high`. Schema is the definition of cross-cutting.

---

## Track B — `lib/fsrs/`

**Model: Opus · Effort: high** · Labels: `area: fsrs`

CLAUDE.md §10's #1 test priority. Highest silent-wrongness cost in the codebase.

Isomorphic wrapper over `ts-fsrs`: pure functions, no DB, no `fetch`, no browser globals.
That constraint is what guarantees client and server schedule identically — it is the whole
point of the module, so enforce it with a lint rule, not a comment.

Surface: given card state + rating + `now` + user parameters, return the next state. Plus
the inverse — replay a `review_log` sequence to rebuild `user_card_state`, which import
backfill and client-bug repair both need (CLAUDE.md §6).

**Verify:** property-based test over random review sequences asserting the client path and
the server replay path produce byte-identical state. If this ever fails, everything else
stops.

**Review before merge:** `/code-review high`.

---

## Track C — `lib/render/`

**Model: Sonnet · Effort: medium** · Labels: `area: ux`

Anki templates are a small language: `{{Field}}`, `{{#Field}}…{{/Field}}` and `{{^Field}}`
conditionals, `{{FrontSide}}`, filters (`{{text:}}`, `{{furigana:}}`, `{{type:}}`), and
cloze deletion `{{c1::hidden::hint}}` where one note generates N cards by ordinal.

Pure `(template, fields) -> html`, isomorphic, golden-file test per construct. Mechanical
once the construct list is fixed — hence Sonnet.

### Carve-out: sanitisation

**Model: Opus · Effort: medium** · Labels: `area: security`, `sev: high`

Split this from the engine work and review it separately.

Note content is user-authored HTML, and shared decks mean it is *other users'* HTML. **Card
content is untrusted input in the multiuser model** — a threat Anki does not have, because
there every card is your own. Getting the template engine right and the sanitiser wrong
ships a stored-XSS vector to every user of a public deck.

**Verify:** an XSS corpus renders inert. Include the cases that survive naive sanitisers —
`javascript:` URLs, `onerror` on `<img>`, SVG event handlers, `<style>` exfiltration.

---

## Then, serial

### Auth + accounts

**Model: Sonnet · Effort: medium** · Depends on Track A · Labels: `area: security`

Resolve the open question in CLAUDE.md §12 first (Lucia-style hand-rolled sessions vs
Auth.js) and record the choice there. Keep it behind `src/lib/server/auth/` either way so
the decision stays reversible.

**↳ Security review: Opus, high.** Separate pass before merge. CLAUDE.md §7 lists
auth/CSRF/session as an Opus trigger, and reviewing your own implementation at the same
altitude that produced it catches little.

### Deck / note / card CRUD

**Model: Sonnet · Effort: medium** · Depends on Track A

Straightforward once the schema exists. The one rule that isn't: **every query takes a
`user_id` and joins `deck_access`** (`docs/schema.md`). Route guards are not sufficient —
"readable by some users" is the normal case here, not the exception.

**Verify:** table-driven access-control test — for each (role, resource, operation), assert
allow/deny. Add a row per new endpoint from here on.

---

## Convergence — reviewer + write queue

**Model: Opus · Effort: high** · Depends on A + B + C · Labels: `area: fsrs`, `area: ux`

Cross-cutting client architecture, invariant §2.6, and the thing CLAUDE.md §11 says
everything else is downstream of. Protocol is fully specified in CLAUDE.md §6 — implement
it, don't redesign it.

**Don't start this early against stubs.** Its data flow is set by real FSRS behaviour and
real state shapes; building it against mocks produces a design you then have to redo.

**Verify:** the §10 list — write-queue idempotency (replay, reorder, and interleave a later
review all converge to the same `user_card_state`), plus a Playwright run where the network
is cut mid-session and reviews survive.

---

## `.apkg` import → export

**Depends on A + B** · Labels: `area: apkg`

- **Reader: Opus, high.** `docs/apkg-format.md`'s traps — `cards.due` meaning two different
  things, negative `ivl` meaning seconds — are exactly where a competent-looking
  implementation goes quietly wrong. Correct that doc in place as real fixtures are
  inspected; it is currently unverified.
- **Writer: Sonnet, medium**, once the IR is proven by the reader.

**Verify:** `import(export(import(f))) == import(f)` across fixtures from several Anki
versions.

---

## Parameter optimisation

**Model: Opus · Effort: medium** · Last in Phase 1

**Resolve CLAUDE.md §12's optimiser question before starting**, not during: `fsrs-rs-nodejs`
(native binding — deployment complexity, not maintenance burden) vs a pure-TS optimiser vs
an out-of-process job.

Ship defaults and require ~1,000 reviews before switching a user onto their own fitted
parameters. **Never fit one parameter set across a cohort** (§2.4).
