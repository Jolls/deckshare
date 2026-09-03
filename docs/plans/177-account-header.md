# #177 — Account name and settings gear in a persistent header

Issue: [#177](https://github.com/Jolls/deckshare/issues/177). Design source:
`docs/plans/175-multi-user-session-switching.md` §8 (header partial + CSS subsections only — the
dropdown, htmx roster fragment, and lock screen in that doc are later issues, not built here).

Ships only: a header bar with the active user's display name (left) and a settings gear (right),
rendered on every authenticated page except `/decks/{id}/review` and `/study`. No dropdown, no
account switching, no `internal/auth` change.

---

## Investigation findings (why each step below is what it is)

- `web/templates/layout.html` today has no header/nav at all — just `<body><main
  class="container">{{template "content" .}}</main></body>`.
- `internal/http/templates.go`'s `parseTemplates` builds each page's template set from
  `{"templates/layout.html", "templates/<name>.html", ...pagePartials[name]}`. Appending
  `templates/header.html` to that base slice makes a `{{define "header"}}` available to
  `layout.html` for every page, no `pagePartials` entry needed.
- All 39 `render(w, pages[...], ...)` call sites were checked. Exactly two (`login`, `signup`, in
  `internal/http/auth.go`) pass a `map[string]any` with no `"User"` key. Every other call site
  (`decks.go`, `notetypes.go`, `notes.go`, `access.go`, `settings.go`, `import.go`, `aiimport.go`,
  and the two in `review.go`) sets `"User": user` where `user` is a `db.User` value (from
  `auth.UserFromContext`, `internal/auth/middleware.go:21`, which returns `(db.User, bool)`, never
  a pointer). Because page data is `map[string]any`, `{{if .User}}` on a map with no `"User"` key
  evaluates false (nil interface), and evaluates true whenever the key is set — the struct's
  content doesn't matter. So `{{if .User}}` is a correct, zero-touch guard for exactly the
  login/signup pages, with no change needed to auth.go.
- `db.User` (`internal/db/models.go:132`) has field `DisplayName string` — same field
  `settings.html` already prints as `{{.User.DisplayName}}`.
- Settings.html's 10 render call sites in `settings.go` were individually checked — all 10 set
  `"User"`, no gap.
- `web/templates/review.html` and `web/templates/study.html` are rendered from exactly two call
  sites, both in `internal/http/review.go` (lines 64–66 and 113–116 as read for this plan). These
  are the only two page renders that need the hide behaviour.
- `web/static/app.css` has no existing page/body-class hook and no `:has()` usage anywhere in the
  codebase. The header renders *inside* `<main>`, above the content include, so it and the
  review/study content markup (`#review-stage` etc.) end up as sibling nodes inside the same
  `<main>` — CSS combinators can't reach backward from a later sibling to an earlier one, and
  there's no existing wrapper class to key off. The minimal, unambiguous hook is a `BodyClass` key
  in the two `review`/`study` data maps, rendered as a class on `<body>`, targeted with a plain
  descendant selector. This needs edits to exactly those two render calls (not all 39) and one
  line in `layout.html` — no new CSS feature, no per-page threading elsewhere.
- No existing test asserts exact/full rendered HTML for any page — `internal/http/*_test.go` uses
  `strings.Contains(body, ...)` substring checks (e.g. `decks_test.go`, `security_test.go`), so
  adding header markup does not break any existing assertion. No test changes required.

---

## File changes

### 1. `web/templates/header.html` (new file)

```html
{{define "header"}}
<header class="account-bar">
    <span>{{.User.DisplayName}}</span>
    <a href="/settings" aria-label="Account settings">⚙</a>
</header>
{{end}}
```

### 2. `web/templates/layout.html`

Replace:

```html
<body>
    <main class="container">
        {{template "content" .}}
    </main>
</body>
```

with:

```html
<body class="{{.BodyClass}}">
    <main class="container">
        {{if .User}}{{template "header" .}}{{end}}
        {{template "content" .}}
    </main>
</body>
```

(`.BodyClass` is absent from every page's data map except `review` and `study` after step 4 below;
a missing map key renders as an empty string, so `class=""` on every other page — harmless.)

### 3. `internal/http/templates.go`

In `parseTemplates`, change:

```go
files := append([]string{"templates/layout.html", "templates/" + name + ".html"}, pagePartials[name]...)
```

to:

```go
files := append([]string{"templates/layout.html", "templates/header.html", "templates/" + name + ".html"}, pagePartials[name]...)
```

One line, no `pagePartials` entries added.

### 4. `internal/http/review.go`

In the `GET /decks/{id}/review` handler, change:

```go
render(w, pages["review"], http.StatusOK, map[string]any{
    "User": user, "Deck": deck, "CSS": css, "Batch": toBatchView(batch),
})
```

to:

```go
render(w, pages["review"], http.StatusOK, map[string]any{
    "User": user, "Deck": deck, "CSS": css, "Batch": toBatchView(batch),
    "BodyClass": "hide-account-bar",
})
```

In the `GET /study` handler, change:

```go
render(w, pages["study"], http.StatusOK, map[string]any{
    "User": user, "CSS": css,
    "Batch": toBatchView(review.Batch{Cards: cards, Exhausted: true}),
})
```

to:

```go
render(w, pages["study"], http.StatusOK, map[string]any{
    "User": user, "CSS": css,
    "Batch": toBatchView(review.Batch{Cards: cards, Exhausted: true}),
    "BodyClass": "hide-account-bar",
})
```

### 5. `web/static/app.css`

Append at the end of the file:

```css
/* Persistent account bar (#177): active user's display name + settings gear, upper right of
   every authenticated page. */
.account-bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.5rem 0;
}

/* Resolved decision (#175 Q4): hidden on the reviewer and study-all pages -- a persistent bar
   costs vertical space on a phone, and a later issue hangs account-switch/lock controls off this
   bar, which must not sit one mis-tap from a grading button. layout.html sets this class on
   <body> only for the review and study pages (internal/http/review.go). */
body.hide-account-bar .account-bar {
    display: none;
}
```

No CSP change: `style-src` already covers `/static/` (per issue acceptance criteria).

---

## Verification

- `go build ./...` and `go vet ./...`.
- `go test ./...` — confirms no existing `strings.Contains` assertion breaks.
- Manual pass: load every page type at least once while signed in (decks, deck, deck_new,
  deck_edit, access, notetypes, notetype_form, note_form, import, import_ai, settings, review,
  study) and confirm the bar shows the signed-in display name and a working `/settings` gear link
  everywhere except `/decks/{id}/review` and `/study`; load `/login` and `/signup` signed out and
  confirm no bar and no template error.

No new automated test is proposed: this is a UI-only template/CSS change with no logic branch
worth a regression test (CLAUDE.md §10 rule 5 exception list — FSRS/`.apkg` — doesn't apply here).

---

## Out of scope (unchanged from issue body)

Account switching, lock, and any change to `internal/auth`. The dropdown, `/accounts/menu`
fragment, and `lock.html` described in `docs/plans/175-multi-user-session-switching.md` §8 belong
to issues C (#179) and D (#180).
