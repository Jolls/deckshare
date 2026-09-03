# #175 — Multi-user login: signed-in account switching, lock, and the account header

> Issue: [#175](https://github.com/Jolls/deckshare/issues/175) — *"Add multi-user login where you can
> 'switch user' and 'logged in users' can swap around without re-entering username. Need to make
> the active user very obvious so add a username and user settings 'gear' icon in the upper right."*

Architecture only. No code written yet. §12 proposes splitting the work across four issues; this
document is the shared design all four implement against.

---

## 0. Resolved decisions

All four open questions from the first draft were put to the human and answered. Nothing below is
left as a judgment call.

### Q1 — Is a switch credential-free? → **yes, and a `lock` state is where passwords live**

**Answer (verbatim intent):** credential-free switching, plus a three-way control: **lock**,
**logout**, **switch**. Lock requires a password to come back, *but* the still-signed-in accounts
remain a one-click selection box on the lock screen — you click your name, you type your password.

This resolves the ambiguity the first draft flagged rather than choosing a side of it. The issue's
two readings turn out to be **two different states**, not two interpretations:

| State | Switch to another signed-in account costs |
|---|---|
| Unlocked | nothing — one click, no credentials (the strong reading) |
| Locked | one click to pick the name, then that account's password (the weak reading) |

So "swap around without re-entering username" is literally true in both: the username is *never*
re-typed. Only the password ever is, and only from a locked browser. Lock is the feature that makes
credential-free switching safe to leave running on a shared machine, which is why answering Q1 this
way expands the design instead of simplifying it — see §7 and §8.

### Q2 — Is the `?u=` cross-tab guard in this PR? → **its own issue, landed first**

Confirmed. It becomes issue **B** in §12's slate, and the switcher (**C**) must not merge before it.

### Q3 — Cap on accounts per set? → **unbounded**

Confirmed. No cap, no config knob. The roster is self-limiting: each member cost someone a password
entry once.

### Q4 — Header on the reviewer page? → **render it, hide it on review/study via CSS**

Confirmed, per the first draft's recommendation. This turns out to be load-bearing beyond
aesthetics — it is also what keeps a lock/switch control from sitting one mis-tap from a grading
button, and it removes the mid-review flush problem in §7.4.

---

## 1. What is being built

Three deliverables, separable and separately shippable (§12):

- **Header chrome.** Active user's display name plus a settings gear, upper right, on every
  authenticated page. Today `web/templates/layout.html` has *no* nav at all — the only way to know
  who you are is to open `/settings`.
- **Account switching.** One browser holds live sessions for several accounts and swaps the acting
  identity with no credential prompt.
- **Lock.** A browser-wide "step away from the keyboard" state that suspends every account signed
  in on it, with a name-tile unlock screen.

---

## 2. Core design: the session set

Add a `set_id uuid` column to `sessions`. Every session row created by the same browser carries the
same `set_id`; a **session set** is therefore just "the accounts this browser is signed in to". The
cookie is unchanged — still one `__Host-enshu_session` holding the raw token of the **active**
session only. Switching to account B means: read the active session's `set_id`, confirm a live
`sessions` row exists for `(set_id, B)`, mint a **fresh** token for B in that same set, delete B's
old row, and write the new token to the cookie. The presence of a live sibling row *is* the
entitlement — no new credential, no second cookie, no raw token stored anywhere but the one cookie,
and no new table.

```
                       sessions
  set_id = S1   +------------------------------------------+
                | id=sha256(tokA)  user=A  expires...      |  <- inactive, token unknown to anyone
                | id=sha256(tokB)  user=B  expires...      |  <- ACTIVE: cookie holds tokB
                | id=sha256(tokC)  user=C  expires...      |  <- inactive
                +------------------------------------------+
  set_id = S2   +------------------------------------------+
                | id=sha256(tokA') user=A  expires...      |  <- same human, phone. Independent set.
                +------------------------------------------+

  switch S1 -> A :  INSERT (sha256(tokA2), A, S1) ; DELETE sha256(tokA) ; Set-Cookie: tokA2
```

Invariant of the design: **at most one live `sessions` row per `(set_id, user_id)`**, enforced by a
unique index. That keeps the roster query trivial and makes "switch" idempotent.

Why this shape rather than the obvious alternatives — see §13.

---

## 3. Data model

### Migration `00017_sessions_set_id.sql` (issue C)

```sql
-- +goose Up
-- A session set is one browser's collection of signed-in accounts (#175). The cookie carries the
-- active member's token; sibling rows in the same set are what entitle a switch to them without
-- re-authenticating. Existing sessions each become a singleton set, so the column is backfilled
-- by the default and no session is invalidated by this migration.
ALTER TABLE sessions ADD COLUMN set_id uuid NOT NULL DEFAULT gen_random_uuid();

-- The roster lookup (every switcher menu and every lock screen) and the "sign out everywhere on
-- this browser" delete both key on set_id alone.
CREATE INDEX sessions_set_id_idx ON sessions (set_id);

-- One live session per account per set. Switching mints a replacement token and drops the old
-- row; this index is what makes that an invariant rather than a convention, and what stops a
-- repeated "add account" from stacking duplicate rows for the same user in one set.
CREATE UNIQUE INDEX sessions_set_user_key ON sessions (set_id, user_id);

-- +goose Down
DROP INDEX sessions_set_user_key;
DROP INDEX sessions_set_id_idx;
ALTER TABLE sessions DROP COLUMN set_id;
```

### Migration `00018_sessions_locked_at.sql` (issue D)

```sql
-- +goose Up
-- Lock state is per row, set on every member at once and cleared one member at a time (#175):
-- unlocking as A must not unlock B, or a housemate who knows their own password could unlock the
-- browser and then switch to A for free -- which is exactly what lock exists to prevent.
-- NULL = unlocked. A timestamp rather than a boolean because it costs the same column and answers
-- "since when" for free; nothing reads it yet.
ALTER TABLE sessions ADD COLUMN locked_at timestamptz;

-- +goose Down
ALTER TABLE sessions DROP COLUMN locked_at;
```

Notes on both:

- `DEFAULT gen_random_uuid()` is deliberate and permanent, not just a backfill device: a
  `CreateSession` that forgets to pass a set still produces a valid singleton set rather than a
  NULL. `users.id` uses `uuidv7()` with the same "DB default is a safety net" comment
  (`migrations/00001_users.sql`); this follows that precedent but uses v4 — a set id is an opaque
  grouping key, never sorted or paged, so v7's time-ordering buys nothing and leaks creation time.
- **No new table.** A `session_sets` row would carry no columns worth having: creation time is
  already on the oldest member, expiry is per-member, and lock is per-member by §3's reasoning.
- `ON DELETE CASCADE` from `users` is untouched — deleting an account removes it from every set it
  is in, on every browser, which is correct.
- The set has no independent lifetime. When its last member expires the set stops existing by virtue
  of having no rows; `DeleteExpiredSessions` (`internal/auth/cleanup.go`) already reaps it. A set
  whose members all expire *while locked* simply becomes a signed-out browser — the lock screen
  finds no members and redirects to `/login`.

### Queries — `internal/db/queries/sessions.sql`

`CreateSession` gains a `set_id` parameter (all call sites pass one explicitly). New queries:

```sql
-- name: GetSessionSet :one
-- The acting session's set, resolved from the cookie's token hash. Deliberately does NOT filter on
-- locked_at: the lock screen has to resolve the set from a locked session in order to render the
-- roster at all.
SELECT set_id FROM sessions WHERE id = $1 AND expires_at > now();

-- name: ListSetMembers :many
-- Roster for the switcher menu and the lock screen. locked_at comes back so the switcher can mark
-- a member that will re-prompt, and so the lock screen can render every tile.
SELECT u.id, u.display_name, u.email, s.locked_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.set_id = $1 AND s.expires_at > now()
ORDER BY lower(u.display_name);

-- name: SetMemberExists :one
-- The switch entitlement check. Nothing else authorises a credential-free switch.
SELECT EXISTS (
    SELECT 1 FROM sessions
    WHERE set_id = $1 AND user_id = $2 AND expires_at > now() AND locked_at IS NULL
);

-- name: GetLockedSetMember :one
-- Unlock: the target's row plus the password hash to verify against, in one read.
SELECT sqlc.embed(u), s.id
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.set_id = $1 AND s.user_id = $2 AND s.expires_at > now();

-- name: LockSet :execrows
UPDATE sessions SET locked_at = now() WHERE set_id = $1 AND locked_at IS NULL;

-- name: UnlockSetMember :execrows
UPDATE sessions SET locked_at = NULL WHERE set_id = $1 AND user_id = $2;

-- name: DeleteSetMember :execrows
DELETE FROM sessions WHERE set_id = $1 AND user_id = $2;

-- name: DeleteSet :execrows
DELETE FROM sessions WHERE set_id = $1;
```

`GetSessionUser` gains `s.set_id` and `s.locked_at` in its select list, so the middleware can resolve
both without a second round trip.

Note `SetMemberExists` filters `locked_at IS NULL` while `GetLockedSetMember` does not. That
asymmetry is the whole lock mechanism in two lines: a locked member cannot be reached by a
credential-free switch, only by the unlock path.

---

## 4. `internal/auth` surface

Every new method takes the raw cookie token and derives the set itself — no handler ever handles a
`set_id` it did not get from a live session.

```go
// SwitchUser makes target the acting account for the browser holding token. It fails unless target
// already has an unlocked live session in token's set: that sibling row is the entire entitlement,
// and it is why the caller is never asked for a password. Returns the raw token of a freshly minted
// session for target; the old token stays valid for its own (unchanged) account.
func (s *Service) SwitchUser(ctx context.Context, token string, target pgtype.UUID) (string, error)

// SetMembers lists the accounts signed in on this browser, for the switcher menu and the lock
// screen. Callable with a locked token -- rendering the lock screen depends on it.
func (s *Service) SetMembers(ctx context.Context, token string) ([]db.ListSetMembersRow, error)

// LogoutSet deletes every session in token's set -- "sign out of all accounts on this device".
func (s *Service) LogoutSet(ctx context.Context, token string) error

// Lock suspends every member of token's set. The cookie is untouched: the browser stays signed in,
// it just cannot act until Unlock.
func (s *Service) Lock(ctx context.Context, token string) error

// Unlock verifies password against target's hash and clears the lock for that member ONLY, then
// mints a fresh session for it and returns the raw token. Rate-limited like Login, and it reuses
// Login's constant-time comparison -- it is a password-guessing surface with a known-valid account,
// so the limiter is the only thing standing in front of it.
func (s *Service) Unlock(ctx context.Context, ip, token string, target pgtype.UUID, password string) (string, error)
```

Changes to existing methods:

| Method | Change |
|---|---|
| `Signup` | Always starts a **new** set. A signup is by definition a new account; joining the current browser's set is `Login`'s job. |
| `Login` | Takes the caller's existing token (may be empty). If it resolves to a live session the new session joins **that** set, otherwise a new set. This is what makes "Add account" work with no extra endpoint — and it works from a locked browser too (§7.3). |
| `Logout` | Unchanged signature; the handler's behaviour changes — see §5. |
| `ChangePassword` | **No change, and that is the point.** It already calls `DeleteSessionsForUser`, which now evicts that account from *every* set on *every* browser while minting the acting browser's replacement. Exactly the desired blast-radius control (§7.1), for free. |
| `createSession` | Signature gains `setID pgtype.UUID`. New sessions are always created unlocked. |
| `Middleware` | Must **not** populate the user context for a locked session — see §6. |

`SwitchUser` and `Unlock` are each one transaction: check → `DeleteSetMember` → `CreateSession`
(plus `UnlockSetMember` for the latter). The unique index makes the delete-then-insert ordering
load-bearing; doing it in a transaction is what stops a double-submitted switch from leaving the
browser with no live row for the target.

---

## 5. Route surface

Add to `docs/routes.md` under the auth section.

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/accounts/menu` | session | htmx fragment: the switcher roster. Lazily fetched when the header dropdown opens — see §8 for why it is not baked into every page render. |
| POST | `/accounts/switch` | session | Body `user_id`. Switch acting account; 303 → `/decks`. 403 if the target is not an **unlocked** live member of the caller's set. |
| POST | `/accounts/remove` | session | Body `user_id`. Sign one *other* account out of this browser without switching to it. |
| POST | `/logout` | session | **Behaviour change.** Deletes the active session; if siblings remain, switches to the most recently created unlocked one and 303 → `/decks`; otherwise clears the cookie and 303 → `/login`. |
| POST | `/logout/all` | session | Deletes the whole set, clears the cookie, 303 → `/login`. |
| POST | `/lock` | session | Locks every member of the set. 303 → `/lock`. |
| GET | `/lock` | locked session | The unlock screen: one tile per set member, display names only. Redirects to `/decks` if the acting session is not locked, and to `/login` if the set has no live members left. |
| POST | `/lock/unlock` | locked session | Body `user_id` + `password`. On success clears that member's lock, mints its session, 303 → `/decks`. On failure re-renders `/lock` with a flat error. |
| GET | `/login?add=1` | public | Does **not** redirect an already-signed-in caller to `/decks` (the current behaviour). Renders the login form with an "adding an account to this browser" heading and a Cancel link. |
| POST | `/login` | public | Unchanged contract. If the request carries a valid session cookie the new session joins that cookie's set instead of starting one. |

**Why `POST /login` joining unconditionally is safe.** The only ways to reach it with a live cookie
are the `?add=1` flow or hand-typing the URL — `GET /login` still bounces a signed-in caller to
`/decks` otherwise. So there is no hidden `add=1` form field to forge and no ambiguity at POST time.
The alternative (a hidden field) adds a forgeable input that decides whether two accounts share a
browser, for no gain.

**Every mutating route above is a POST**, which puts it behind the existing `Origin` check in
`auth.Service.Middleware`. This is not stylistic — see §7.2.

**`/logout` falling back to a sibling** is the one behaviour change to an existing route. It is the
right default (signing out of B on a shared laptop should return you to A, not to a login page you
then have to escape), but it means `POST /logout` no longer always lands on `/login`. Existing
assertions in `internal/http/auth_test.go` need splitting into a sibling-less case (unchanged) and a
new sibling case.

---

## 6. How lock is enforced — one rule

> **A locked session is an unauthenticated session everywhere except `/lock` and `/lock/unlock`.**

`auth.Service.Middleware` resolves the session as it does today, and then: if `locked_at IS NOT
NULL`, it puts the **set id and a locked marker** on the context and *does not* put the `db.User`
there. `UserFromContext` therefore returns `false`, and every existing handler in the application is
correct by default with zero changes — they already all handle "no user".

`RequireUser` gains three lines: if the context carries a locked marker, redirect to `/lock` rather
than `/login`, so a locked browser lands somewhere that explains itself.

Consequences that fall out of this framing rather than needing their own code:

- `/static/*` and `/healthz` stay reachable while locked (they never required a user), so the lock
  screen gets its CSS.
- `/api/reviews/*` returns its existing 401-JSON to a locked browser — no special case.
- Sliding session renewal is skipped while locked: renewal happens on authenticated traffic, and a
  locked browser being polled by a background tab should not keep sessions alive indefinitely. A
  browser left locked past `SessionLifetime` simply needs a full login, which is correct.

This is the single most important implementation rule in the lock issue. Get it right in the
middleware and there is nothing else to audit; get it wrong and every handler is a leak.

---

## 7. Security analysis

### 7.1 Blast radius — accepted, with named mitigations

Today one stolen `__Host-enshu_session` cookie yields one account. After this change it yields every
account in that set. **This is inherent to the feature, not to this implementation** — any design
that lets a browser swap accounts without a credential gives the browser's cookie jar authority over
all of them. Roster-in-a-cookie designs (§13) are strictly worse, not better, at this.

What bounds it:

- **Lock** is the direct mitigation for the realistic threat (a shared or unattended machine, not a
  cookie exfiltration): it reduces a walk-up attacker's authority from "every account in the set" to
  "nothing, without a password". It is the reason credential-free switching is defensible at all.
- `ChangePassword` already purges *all* of a user's sessions. Post-change that also means "evict me
  from every browser's set, everywhere" — the recovery action for a stolen laptop works unchanged
  and now does more.
- `POST /logout/all` clears the set in one action, surfaced in the dropdown rather than buried in
  settings.
- `POST /accounts/remove` lets a user drop one account from a browser without signing out of the
  others — the "I added my account on a friend's machine" case.
- Sessions still expire independently on their own `expires_at`; only the active member gets sliding
  renewal, so an inactive member ages out on schedule rather than being kept alive by another
  account's traffic. **Deliberate** — it makes an abandoned set shrink on its own.

### 7.2 CSRF on the switch, lock, and unlock endpoints

A `GET /accounts/switch?user_id=...` link would be a one-click cross-site attack that **silently
changes who the victim's next review is attributed to**. Given `review_log` is unrecoverable
training data (§2.5) and misattributed rows are invisible until an optimiser fit comes out wrong,
that lands squarely in the `sev: critical` reflex in CLAUDE.md §15. Hence: switch, remove, both
logouts, lock, and unlock are all POST, so `auth.Service.Middleware`'s existing `Origin` check covers
them and the attack requires same-origin.

`POST /lock` is worth a second look because it is the one endpoint whose cross-site version looks
*harmless* — the attack is only a denial of service (lock someone out mid-session). It is still a
POST, for the same reason and at no extra cost.

### 7.3 What lock does and does not protect

- **Unlock is per-member, never set-wide.** Unlocking as A leaves B locked. Otherwise a housemate
  who knows their own password could unlock the browser and then switch to A for free, which is
  precisely the attack lock exists to stop. This is the single most important decision in the lock
  design and the reason `locked_at` is a per-row column rather than set state.
- **Switching to a still-locked member re-prompts.** `SetMemberExists` filters `locked_at IS NULL`,
  so after a lock the switcher's entries for other members route through the unlock screen. The
  menu marks them, so it is not a surprise.
- **The lock screen shows display names, not emails.** Whoever is at the keyboard learns which
  accounts use this browser; that is inherent to a name-tile unlock screen and matches every OS
  lock screen. Withholding the email is free and withholds the more useful identifier. The switcher
  menu, which is only reachable post-auth, can show emails to disambiguate two people with the same
  display name.
- **A locked browser can still `POST /login`.** Someone can sign in as themselves on a locked
  machine and join the set. This is harmless — it costs their own password, and per-member unlock
  means it grants nothing over the existing members. Called out because it looks like a hole and is
  not one.
- **Lock is not encryption.** It is a server-enforced gate on a live session, not a re-derivation of
  anything. A stolen cookie plus a stolen database still yields everything; lock defends the walk-up
  case only. Worth stating so nobody later mistakes it for more.

### 7.4 Cross-tab misattribution — issue B, lands first

Switching is browser-wide (one cookie), so a tab left open on `/decks/{id}/review` as user A keeps
grading after another tab switches to B. Its next `POST /api/reviews/batch` is authorised as B.

- If B lacks `deck_access` on that deck the batch returns `forbidden` per event and nothing is
  written — the existing authorisation already saves us.
- **If the deck is shared and B has `can_study`** (the normal Milestone-2 case) the grades are
  accepted and written into **B's** `user_card_state` and `review_log`. Silent, plausible-looking,
  unrecoverable corruption of B's training data.

The guard: the reviewer page renders the acting user id and `web/static/review.js` appends it as a
query parameter — `POST /api/reviews/batch?u=<uuid>` — which the server compares to the session
user, returning **409 and writing nothing** on mismatch.

- Query parameter, **not a body field**, on purpose. §10.1's test — "any field in the request body
  other than `{id, cardId, rating, reviewedAt, durationMs}` is ignored" — stays literally true, and
  the batch body is untouched.
- It has to be the query string rather than a header because the pagehide flush path uses
  `navigator.sendBeacon` (`web/static/review.js`), which cannot set headers.
- The value is **incapable of forging anything**: a mismatch can only cause a rejection, never a
  write, and a matching value grants exactly what the session already granted. It is a staleness
  check, not an authorisation input.

The same guard covers lock: a locked session's batch POST is unauthenticated (§6) and gets the
existing 401, so those pending grades are dropped rather than misattributed. Dropping them is the
correct outcome and a minor UX loss, not corruption — and Q4's answer (no header on review/study
pages) means the lock control is not reachable from a page that has pending grades in the first
place.

Q2 resolved this as its own issue, landing **before** the switcher — the switcher is what makes the
bug reachable.

### 7.5 Session fixation and enumeration

- Switching and unlocking both mint a new token rather than reusing a stored one; there is no path by
  which an attacker-chosen token becomes active.
- `/accounts/switch` with a `user_id` outside the caller's set returns a flat 403 with no body
  distinction between "no such user" and "not in your set", so it is not an account-existence oracle.
- `/lock/unlock` returns one flat error for every failure — wrong password, non-member, expired
  member — consistent with `Login`'s posture.
- The set id never leaves the server. It is not in a cookie, a form, or a URL. The only way to name a
  set is to hold a live token in it.

---

## 8. UI

### Header partial — `web/templates/header.html` (issue A)

Rendered by `layout.html` above `{{template "content" .}}`, guarded by `{{if .User}}` so the
unauthenticated `login`/`signup` pages (the only two renders in `internal/http/` that pass no `User`
key — verified across all 39 `render` call sites) simply omit it. All page data is `map[string]any`,
so a missing key is a safe nil, not a template error. Per Q4, `app.css` hides the bar on the review
and study pages.

```
+-------------------------------------------------------------+
| Enshu                            Ada Lovelace  v      [gear] |
+-------------------------------------------------------------+
                                   \__ Pico <details class="dropdown">   (issue C)
                                        |- (*) Ada Lovelace   current
                                        |-     Grace Hopper   [Switch]
                                        |-     Alan Turing    [Switch]
                                        |- ------------------
                                        |-     Add another account...
                                        |- ------------------
                                        |-     Lock                      (issue D)
                                        |-     Sign out
                                        \-     Sign out of all accounts
```

- **Making the active user obvious** (the issue's explicit requirement) is the *always-visible*
  display name in the bar, not the dropdown. The dropdown is for acting; the bar is for knowing.
- The gear is a plain `<a href="/settings">` with `aria-label="Account settings"`, kept separate from
  the name — the issue asks for both, and folding the gear into the dropdown would hide the thing it
  asked to make visible.
- Issue A ships the bar with the name and gear only; the dropdown arrives with C and grows a Lock
  item in D. The bar is useful on its own, which is what makes A separable.
- **No new JS.** Pico v2's native `<details class="dropdown">` gives the menu; Alpine is not needed
  (its use in this repo is confined to `confirm()` guards and the graded-count badge). Each roster
  entry is a one-button `<form method="post" action="/accounts/switch">` with a hidden `user_id` —
  works with JS off, and gets the CSRF `Origin` check for free.
- `templates/header.html` is appended to the base file list in `parseTemplates`
  (`internal/http/templates.go`), next to `layout.html` — **one line, no per-page `pagePartials`
  entries.**

### Lock screen — `web/templates/lock.html` (issue D)

Renders through `layout.html` with no header bar (there is no acting user to name). One tile per set
member; clicking a tile reveals that tile's password field rather than navigating, so the whole
screen is one page with no round trip:

```
+-------------------------------------------------------------+
|                        Locked                               |
|                                                             |
|     [ AL ]            [ GH ]            [ AT ]              |
|  Ada Lovelace     Grace Hopper      Alan Turing             |
|                                                             |
|  +-------------------------------------------------------+  |
|  | Password for Ada Lovelace  [__________]  [ Unlock ]   |  |
|  +-------------------------------------------------------+  |
|                                                             |
|                 Sign out of all accounts                    |
+-------------------------------------------------------------+
```

The reveal is the one place a little Alpine `x-data` is warranted — it is presentation only; each
tile is still a real form posting `user_id` + `password` to `/lock/unlock`, so it degrades to three
stacked password forms with JS off. Avatar squares are the display name's initials, no image
storage.

### Why the roster is lazy-loaded

The header needs the *active* user (already in every page's data map) but the dropdown needs the
*whole roster*. Threading a roster through all ~39 `render(w, pages[...], ...)` call sites, or
type-asserting and mutating the data map inside `render`, are both worse than one fragment endpoint.
So:

```html
<details class="dropdown">
  <summary hx-get="/accounts/menu" hx-target="#account-menu" hx-trigger="click once">...</summary>
  <ul id="account-menu"><li aria-busy="true">Loading...</li></ul>
</details>
```

htmx is already vendored and `connect-src 'self'` already permits the XHR. Existing handlers are
untouched. `parseFragments` gains `"accounts_menu"` alongside `"review_cards"`.

### CSS — `web/static/app.css`

Two blocks: the flex bar (`justify-content: space-between`) plus its review/study hide rule (Q4),
and the lock-screen tile grid. `style-src` already allows `/static/` and `'unsafe-inline'`; **no CSP
change is needed for any part of this feature.** Worth stating explicitly, because a header or lock
screen that needed a new CSP source would be a reason to redesign it.

---

## 9. Invariant check (CLAUDE.md §2)

| Invariant | Effect |
|---|---|
| §2.1 scheduling state off the card row | Untouched. This is auth plumbing; `user_card_state` is not read or written. |
| §2.5 `review_log` append-only | No new `DELETE` path. §7.4 is about *not* writing rows under the wrong user — it protects this invariant rather than bending it. |
| §2.7 client asserts, server derives | Preserved. The `?u=` guard is a rejection-only staleness check; it can refuse a write but never cause or shape one. |
| §2.9 no sync protocol | Untouched — one browser, one server, no device-to-device anything. |
| §2.10 follow Anki's model | Not applicable in the usual sense: Anki has no account system, so there is nothing to conform to or diverge from. **No new row in architecture.md §20** — §20 records deviations from Anki's data model, and a session set has no Anki counterpart to deviate from. |

---

## 10. Test plan

Mapped to the CLAUDE.md §10 priority order. All DB-backed tests scope to rows they create themselves
(§16) — the roster queries key on a `set_id` the test generated, so they are naturally isolated;
**no `count(*)` over `sessions` and no unscoped `LIMIT 1`.**

**Priority 1 — the client cannot write scheduling state** (issue B). Extend the existing §10.1 test
with the cross-tab case: a batch POST whose session belongs to B, carrying `?u=<A>`, writes nothing
and returns 409. Assert `review_log` gains **zero** rows for both A and B. Add the locked case: a
locked session's batch POST returns 401 and writes nothing.

**Priority 5 — access control** (`internal/http/access_test.go` is the table-driven home):

- switch to an unlocked member → 303, cookie changes, `GetSessionUser` on the new cookie returns the
  target.
- switch to a user **not** in the set → 403, cookie unchanged.
- switch to a member whose sibling session has **expired** → 403 (roster and entitlement must agree
  on `expires_at > now()`).
- switch to a **locked** member → 403, no session minted. This is the test that keeps lock from
  being bypassable by the switcher, and it is the highest-value single test in the lock issue.
- switch with no session cookie → redirect to `/login`, no row written.
- `GET /accounts/switch` → 405; the same request cross-origin → 403 from the middleware. Same for
  `/lock` and `/lock/unlock`.
- `/accounts/remove` cannot remove a member of a *different* set.
- **A locked session reaches no application route**: table-driven over a representative set of
  authenticated GET and POST routes, assert every one redirects to `/lock` (HTML) or 401s (API) and
  writes nothing. This is the §6 rule under test, and it is the one that must not regress silently
  as routes are added.

**`internal/auth` unit tests:**

- Login with a live cookie joins that set; login without one starts a fresh set; signup always starts
  a fresh set; login from a *locked* browser joins the set and is itself unlocked (§7.3).
- Switching A→B→A leaves exactly one live row per `(set_id, user_id)` — asserts the unique index
  holds under the mint-then-delete ordering.
- `Lock` sets `locked_at` on every member; `Unlock` as A clears **only** A's, leaving B locked.
- `Unlock` with the wrong password leaves `locked_at` set and mints nothing, and is rate-limited on
  the same budget shape as `Login`.
- `ChangePassword` on A drops A from every set on every browser while the changing browser stays
  signed in as A (regression on an interaction that comes free today and could break silently).
- `Logout` with siblings falls back to the newest unlocked sibling; without siblings it clears the
  cookie.
- `DeleteExpiredSessions` reaping the last member of a locked set leaves a browser that lands on
  `/login`, not a wedged lock screen.

**Migrations:** each applies cleanly to a database with existing sessions, and every pre-existing
session survives `00017` with a **distinct** `set_id` — accidentally merging two users' browsers into
one set would be a live privilege escalation, so it is worth an explicit assertion. `00018` leaves
every existing session unlocked.

**E2E (Playwright, §10.6):** sign in as A, add B, switch, assert the header name changes and `/decks`
shows B's decks; sign out once and land back on A. Second scenario: lock, assert `/decks` bounces to
`/lock`, unlock as A, assert B is still marked locked in the switcher.

---

## 11. Build order within each issue

Steps that ship no user-visible change are marked ∅ — they are the ones safe to land ahead of the UI.

**Issue B — `?u=` guard**
1. ∅ Server-side comparison and 409 on `/api/reviews/batch`, tolerant of a missing `u` (nothing sends
   it yet). *Verify:* priority-1 test.
2. `review.js` sends it on both the `fetch` and `sendBeacon` paths; reviewer template renders the id.
   *Verify:* E2E grading still round-trips.

**Issue A — header chrome**
1. `header.html`, `layout.html`, `templates.go` one-line base-file change, `app.css` bar + Q4 hide
   rule. *Verify:* every page renders including login/signup (the `{{if .User}}` guard); manual pass.

**Issue C — session sets and switching**
1. ∅ Migration `00017` + `go generate`. *Verify:* `goose up` against a fresh and a populated DB, then
   `go build ./...`.
2. ∅ `auth`: `createSession` set parameter, `Login` set-joining, `SwitchUser`, `SetMembers`,
   `LogoutSet`. *Verify:* the `internal/auth` unit tests. Behaviour is unchanged for a single-account
   browser — everything gets a singleton set.
3. Routes: `/accounts/menu`, `/accounts/switch`, `/accounts/remove`, `/logout` fallback,
   `/logout/all`, `GET /login?add=1`. *Verify:* the access-control table.
4. Dropdown in `header.html` + `accounts_menu.html` fragment. *Verify:* manual pass.

**Issue D — lock**
1. ∅ Migration `00018` + `go generate`.
2. ∅ `auth`: `Lock`, `Unlock`, the `locked_at IS NULL` filter on `SetMemberExists`, unlock rate
   limiter. *Verify:* auth unit tests.
3. ∅ **Middleware §6 rule** and `RequireUser`'s `/lock` redirect. *Verify:* the locked-session
   route table — this step is the security boundary and deserves its own review pass.
4. `/lock`, `/lock/unlock`, `lock.html`, Lock item in the dropdown. *Verify:* E2E lock scenario.

Model guidance (CLAUDE.md working rule 7): **Opus** for B, C steps 1–3, and all of D — schema, auth,
and the security boundary. Sonnet is fine for A, C step 4, and docs.

Docs to update in whichever issue touches them: `docs/routes.md` auth table, `docs/architecture.md`
§12 where the session shape is described, `CHANGELOG.md` once per PR.

---

## 12. Proposed issue split

Four issues rather than one. The dependency edges are real, not bookkeeping — B before C is a
security ordering (§7.4), and C before D is structural (lock needs a set to lock).

Filed as #177, #178, #179, #180.

```
  A #177  header chrome        ─┐
                                ├──>  C #179  session sets + switching  ──>  D #180  lock
  B #178  ?u= cross-tab guard  ─┘
```

| | Issue | Labels | Depends on | Notes |
|---|---|---|---|---|
| **A** | [#177](https://github.com/Jolls/deckshare/issues/177) — Account name and settings gear in a persistent header | `area: refactor` | — | Pure UI, ~1 template + ~1 CSS block. Ships value alone: today there is no way to see who you are without opening `/settings`. Also the only piece with no security surface. |
| **B** | [#178](https://github.com/Jolls/deckshare/issues/178) — Reject review grades sent under a switched-away session | `sev: high`, `area: fsrs`, `area: security` | — | Q2. Must merge before C. Written as a standalone defect: it is *latent* today (one session per browser) and becomes reachable the moment C lands. |
| **C** | [#179](https://github.com/Jolls/deckshare/issues/179) — Session sets: switch between signed-in accounts without re-authenticating | `area: security`, `area: db` | #177, #178 | The core of #175. Migration `00017`, the `auth` methods, the account routes, the dropdown. |
| **D** | [#180](https://github.com/Jolls/deckshare/issues/180) — Lock the browser without signing out | `area: security` | #179 | Migration `00018`, the §6 middleware rule, the tile unlock screen. |

**#175 itself** stays open as the tracking issue and closes when #180 merges; the four get
`closes #NNN` for themselves and reference #175 in the body.

### Further issues worth filing, not part of this slate

Suggestions, in the order I would rank them:

1. **Signed-in devices list in `/settings`** — "signed in on 3 browsers, sign out everywhere". Once
   `set_id` exists this is one query and one button, and it is the natural home for the account-
   recovery story that §7.1 currently answers with "change your password". Small, high value,
   `area: security`.
2. **Auto-lock after idle** — a follow-up to D and the thing that makes lock actually get used, since
   nobody remembers to click it. Needs a decision on whether the timer is client-side (a JS timer
   posting `/lock`) or server-side (a `last_seen_at` column checked in the middleware); the latter is
   more honest and costs a write per request unless throttled. Deliberately kept out of D — D is
   already the largest security surface in the slate.
3. **Per-user accent colour or avatar initial in the header** — #175's "make the active user very
   obvious" is satisfied by the display name, but a colour derived from the user id makes a
   wrong-account moment pre-attentive rather than something you have to read. Cosmetic, `sev: low`,
   and it shares the initials-tile code with D's lock screen, so filing it after D is cheaper.
4. **Remember the last active account per browser** — after a full logout and re-login, land on the
   account you were last using rather than the one you typed. Only worth it if the set routinely has
   3+ members; file it, do not build it yet.

None of these are blockers and none should be folded into A–D.

---

## 13. Alternatives considered and rejected

**Roster of raw tokens in a second cookie.** Cookie carries `{userA: tokA, userB: tokB, ...}`;
switching is client-side. Rejected: multiplies raw tokens outside the DB (today exactly one exists,
in one cookie), grows unboundedly with accounts, has no server-side revocation of the roster itself,
and **cannot express lock at all** — a client-side roster has no state the server can suspend. That
last point is what makes it not merely worse but unable to deliver Q1's answer.

**A `session_sets` table with an `active_user_id` pointer, keyed by a second `__Host-` cookie.**
Switching becomes one `UPDATE` with no token churn, and lock becomes one column on the set row.
Rejected: it creates a *second* credential (the set cookie) strictly more powerful than the session
cookie, so there are two things to get right instead of one. It also gets lock *wrong for free* — a
set-level lock flag makes unlock set-wide, which §7.3 shows is the insecure variant.

**Set-wide unlock** (any member's password unlocks the browser for everyone). Simpler and matches a
naive "screen lock" mental model. Rejected: a housemate who knows their own password could unlock and
then switch to your account for free, which is the exact attack lock exists to stop. Per-member
unlock costs one `WHERE user_id = $2` clause.

**Per-account URL prefixes (`/u/0/decks`, Gmail-style).** Genuinely better in one respect — it fixes
§7.4 outright by making the acting identity part of the URL, so a stale tab keeps its own identity.
Rejected as disproportionate: it re-homes every route, every template link, every redirect, and every
test, for a two-or-three-account single-household scale. Worth revisiting only if Milestone 3's
classroom use makes many-account browsers routine — **and if it is ever revisited, issue B's guard
becomes unnecessary**, which is the tell that these two are solutions to the same problem at
different prices.

**Re-prompting for the password on every switch.** Rejected by Q1: it contradicts the issue's own
words, and lock delivers the same protection only when it is actually wanted.
