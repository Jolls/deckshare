# Plan: Auth UI — signup/login/logout forms (#28)

## Current state (verified against code, not the issue text)

- `src/routes/(auth)/signup/+server.ts`, `login/+server.ts`, `logout/+server.ts` are
  POST-only JSON endpoints. No `+page.svelte` exists under `(auth)/`.
- `src/routes/(app)/` has only `.gitkeep` and `study/[deckId]/`. No layout, no nav, no
  landing/home page.
- Only UI in the whole app: `src/routes/+page.svelte`, the unmodified SvelteKit
  placeholder. `src/routes/+layout.svelte` only sets the favicon.
- `src/hooks.server.ts` calls `assertSameOrigin` (`src/lib/server/auth/csrf.ts`) on every
  non-GET/HEAD request: rejects (403) unless `Origin` header === `event.url.origin`.
  Modern browsers send `Origin` on same-origin `fetch` POSTs (not just cross-origin), so
  a same-origin `fetch('/signup', {method:'POST', ...})` passes this check with no extra
  token needed.
- All three endpoints return **JSON only**, never a redirect: success is `{ user: {...} }`
  (201 for signup, 200 for login, `{ ok: true }` for logout); failure is
  `{ error: string }` with a specific status.

## Decision: client-side `fetch`, not SvelteKit form actions

Plain HTML `<form method="POST" action="/signup">` would work past CSRF (same-origin
Origin header), but the response is JSON, not a redirect — the browser would navigate to
and render raw `{"user": {...}}` or `{"error": "..."}` text as the page. Making it behave
correctly would require either (a) rewriting the `+server.ts` endpoints to redirect, which
duplicates/forks the already-reviewed JSON contract (out of scope: "no new auth logic"),
or (b) adding `+page.server.ts` actions that call `signUp`/`logIn` directly, which
duplicates the endpoint's rate-limiting/cookie-setting logic in a second place.

Both violate "purely a UI wrapper over the existing implementation." Instead:
**`+page.svelte` files run client-side JS that `fetch()`s the existing endpoints
unchanged, then `goto()`s on success or renders `data.error` on failure.** No
`+page.server.ts` for signup/login. This is a hard requirement (JS-required forms); no
`<noscript>` fallback is in scope.

## Client-side validation rules (sourced from actual server code)

From `src/lib/server/auth/signup.ts`:
- `EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/`, `EMAIL_MAX_LENGTH = 255`
- `PASSWORD_MIN_LENGTH = 8`, `PASSWORD_MAX_LENGTH = 200`
- `DISPLAY_NAME_MAX_LENGTH = 255`, and non-empty after `.trim()`

From `src/lib/server/auth/login.ts`:
- Only checks `email.length > 255` / `password.length > 200` (both folded into the generic
  401 "Invalid email or password" — login does **not** validate email format, so the
  client must not invent a stricter format check for the login form than the server
  enforces).

These constants live in `src/lib/server/**`, which client code cannot import
(SvelteKit's server-only boundary). New file, isomorphic, duplicating them intentionally:

### New file: `src/lib/auth/validation.ts`

```ts
/**
 * Client-side mirror of the server's validation rules — NOT the source of truth, the
 * server (src/lib/server/auth/signup.ts, login.ts) is. This file exists only to avoid a
 * round-trip on obvious errors; keep it in sync by hand if those files change.
 */

export const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
export const EMAIL_MAX_LENGTH = 255;
export const PASSWORD_MIN_LENGTH = 8;
export const PASSWORD_MAX_LENGTH = 200;
export const DISPLAY_NAME_MAX_LENGTH = 255;

/** Mirrors signup.ts's email check. Also usable for login's max-length fast-reject. */
export function validateEmailFormat(email: string): string | null {
	if (!EMAIL_RE.test(email) || email.length > EMAIL_MAX_LENGTH) {
		return 'Invalid email address';
	}
	return null;
}

/** For login: server doesn't validate format, only rejects oversized input. */
export function validateEmailMaxLength(email: string): string | null {
	if (email.length > EMAIL_MAX_LENGTH) return 'Invalid email or password';
	return null;
}

/** Mirrors signup.ts's password length check. */
export function validatePasswordLength(password: string): string | null {
	if (password.length < PASSWORD_MIN_LENGTH || password.length > PASSWORD_MAX_LENGTH) {
		return `Password must be between ${PASSWORD_MIN_LENGTH} and ${PASSWORD_MAX_LENGTH} characters`;
	}
	return null;
}

/** For login: server only rejects oversized passwords, not short ones (existing accounts). */
export function validatePasswordMaxLength(password: string): string | null {
	if (password.length > PASSWORD_MAX_LENGTH) return 'Invalid email or password';
	return null;
}

/** Mirrors signup.ts's display-name check. */
export function validateDisplayName(displayName: string): string | null {
	if (!displayName.trim() || displayName.length > DISPLAY_NAME_MAX_LENGTH) {
		return 'Display name is required';
	}
	return null;
}
```

## Error surfacing (from actual `+server.ts` response shapes)

All three endpoints return `{ error: string }` on failure with these statuses:
- `400` — missing/malformed body (signup: "email, password, and displayName are
  required"; login: "email and password are required") — should be pre-empted by client
  validation, but still handled if it occurs.
- `400` — server-side validation failure, e.g. "Invalid email address", "Password must be
  between 8 and 200 characters", "Display name is required" (signup only).
- `401` — "Invalid email or password" (login only).
- `403` — "Cross-site request rejected" (CSRF; shouldn't occur under normal same-origin
  fetch, but handled defensively).
- `409` — "An account with that email already exists" (signup only).
- `429` — "Too many attempts. Try again later." (login) or "Too many signup attempts. Try
  again later." (signup).

**UI handling, uniform across all of the above:** render `data.error` verbatim in a single
inline error region above the form fields (`role="alert"`). No per-status branching in the
UI — the server's message is already the right user-facing string for every case,
including 429 (this resolves "what does a 429 look like" — it's the same generic error
banner with the server's own text, not a special UI). If the response is non-JSON or the
`fetch` itself throws (network failure), show a fixed fallback string: `"Something went
wrong. Try again."` Client-side validation errors render the same way, per-field, before
any request is sent.

## New/changed files

### 1. `src/routes/(auth)/signup/+page.svelte` (new)

- Svelte 5 runes (`$state`), fields: email, password, displayName.
- `onsubmit` (`preventDefault`): run `validateEmailFormat`, `validatePasswordLength`,
  `validateDisplayName` from `$lib/auth/validation`; if any fail, set local error state
  per field, do not fetch.
- Else `fetch('/signup', { method: 'POST', headers: {'content-type':'application/json'},
  body: JSON.stringify({ email, password, displayName }) })`.
- On `res.ok`: `goto('/decks')`.
- On failure: parse `await res.json().catch(() => null)`, show `body?.error ??`
  fallback string.
- Disable the submit button while the request is in flight (avoid double-submit racing
  the rate limiter).
- Plain unstyled markup matching the one existing precedent's minimalism (`study/
  [deckId]/+page.svelte` uses scoped `<style>` with no design system, no component
  library, no CSS framework in `package.json`) — labeled inputs, a submit button, a link
  to `/login` ("Already have an account? Log in").

### 2. `src/routes/(auth)/login/+page.svelte` (new)

- Same structure as signup, fields: email, password only.
- Client validation: `validateEmailMaxLength`, `validatePasswordMaxLength` (NOT
  `validateEmailFormat` — see rules above, login server doesn't check format).
- `fetch('/login', ...)`, same success/error handling, redirect target `/decks`.
- Link to `/signup` ("Need an account? Sign up").

### 3. `src/routes/(app)/+layout.svelte` (new)

`(app)/` currently has zero layout files, so this is new infrastructure, not an edit.
Scope kept to exactly what #28 asks for — a logout control reachable from the
authenticated shell — nothing else:

- Wraps `{@render children()}`.
- A logout button that on click: `fetch('/logout', { method: 'POST' })`, then
  `goto('/login')` regardless of response body (logout's only failure mode server-side is
  a no-op if there was no session).
- No nav bar, no branding, no other chrome.

### 4. `CHANGELOG.md`

Under the existing `## [Unreleased]` → `### Added` (top of that list, matching the
existing entry order/style):

```
- Browser-facing signup, login, and logout pages wrapping the existing `/(auth)/*` JSON endpoints, with client-side validation mirroring the server's email/password rules ([#28](https://github.com/Jolls/enshu/issues/28))
```

## Resolved decisions (from open questions — user confirmed)

- **Post-signup/login redirect target:** `/decks`. Issue #29 (landing in this same batch,
  right after #28) adds `src/routes/(app)/decks/+page.svelte` as a deck list — that is
  the real landing page for the whole batch. Do not build a separate `(app)/+page.svelte`
  in this issue — #29 owns that route. (`/decks` doesn't exist until #29's group applies;
  that's fine, both land in the same PR before anyone tests the tip.)
- **Logout redirect target:** `/login`.
- **`(app)/+layout.svelte` scope:** logout-only, as planned. No nav bar in this issue.
- **Already-authenticated visitors to `/signup` or `/login`:** no redirect-away check.
  Landing on either page while logged in just re-runs signup/login and re-sets the
  cookie — harmless, not worth the extra `+page.server.ts` load function this issue's
  client-fetch-only approach was chosen to avoid.
- **Visual/branding baseline:** match `study/[deckId]/+page.svelte`'s existing minimalism
  (labeled native inputs/buttons, scoped `<style>`, no CSS framework). No design pass.

## Testing

No new test files. Justification: CLAUDE.md §10's priority list doesn't cover UI, and
rule 5 says skip tests for UI-only changes absent non-obvious edge cases. Here:
- The validation *logic* being mirrored is already covered by
  `src/lib/server/auth/auth.test.ts` and friends — this change adds no new logic, only a
  thin duplicate of already-tested constants plus fetch/redirect glue.
- The one edge case with any subtlety (client/server validation drift) is caught by
  reading the source at implementation time, not by a test — a test asserting the mirror
  equals the server constants would just be re-encoding the duplication, not catching a
  real failure mode Playwright could observe.
- If this grows real interaction complexity (multi-step forms, optimistic UI) later,
  revisit; a Playwright e2e for the happy-path signup→redirect→logout flow would be the
  natural next addition once there's more of an app shell to assert against.

## Critical files for implementation

- `src/routes/(auth)/signup/+server.ts`
- `src/routes/(auth)/login/+server.ts`
- `src/lib/server/auth/signup.ts`
- `src/lib/server/auth/login.ts`
- `src/hooks.server.ts`
- `src/routes/(app)/study/[deckId]/+page.svelte` (styling precedent)
</content>
