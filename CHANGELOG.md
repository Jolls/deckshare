# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.2] - 2026-08-08
### Added
- Isomorphic `lib/fsrs/` wrapper over `ts-fsrs` — pure functions, no DB, no `fetch`, no browser globals, so client and server schedule identically ([#5](https://github.com/Jolls/enshu/issues/5))
- `lib/render/` note-type template engine: `{{Field}}`, `{{#Field}}`/`{{^Field}}` conditionals, `{{FrontSide}}`, filters, and cloze fan-out by ordinal ([#12](https://github.com/Jolls/enshu/issues/12))
- Auth + accounts: `argon2id` password hashing, sessions keyed by the SHA-256 hash of the cookie token, `Origin`-header CSRF checks, and login rate limiting ([#7](https://github.com/Jolls/enshu/issues/7))
- Deck / note-type / note / card CRUD query layer and JSON API routes, with `deck_access` authorisation enforced at the query layer ([#8](https://github.com/Jolls/enshu/issues/8))
- `.apkg` / `.colpkg` reader: package → intermediate representation, covering collection schemas 11 and 18+, the legacy and zstd containers, and content-addressed media ([#9](https://github.com/Jolls/enshu/issues/9))
- Synthetic `.apkg` fixtures built to `docs/apkg-format.md`, pending real Anki exports ([#9](https://github.com/Jolls/enshu/issues/9))
- Collection-member preference regression test: a package carrying both `collection.anki21` and a one-note `collection.anki2` downgrade stub must read the former ([#9](https://github.com/Jolls/enshu/issues/9))
- The reviewer and the write queue: one-request session start, synchronous local grading, a durable `localStorage` queue draining to an idempotent `POST /api/reviews/batch`, and the server-side `review_log` recompute path ([#13](https://github.com/Jolls/enshu/issues/13))
- `defaultFsrsParams()` — the `ts-fsrs` defaults a user schedules with before their first optimiser fit ([#13](https://github.com/Jolls/enshu/issues/13))
- Browser-facing signup, login, and logout pages wrapping the existing `/(auth)/*` JSON endpoints, with client-side validation mirroring the server's email/password rules ([#28](https://github.com/Jolls/enshu/issues/28))
- Deck list/create, an auto-seeded default "Basic" note type, add-note form, and note list/delete pages under `src/routes/(app)/decks/` — the minimum UI to create study material without `curl` ([#29](https://github.com/Jolls/enshu/issues/29))

### Fixed
- `docs/apkg-format.md`: `cards.due` has three meanings discriminated by `queue`, not two by `type`, and `cards.odue` shadows it entirely for cards in a filtered deck; the negative-`ivl` seconds encoding also applies to `revlog`; schema-18 configs are protobuf; deck-name separators and field ordering differ per schema; the modern media index is protobuf ([#9](https://github.com/Jolls/enshu/issues/9))

### Security
- Card content is sanitised on render — note HTML is user-authored and, under shared decks, *other users'* HTML ([#6](https://github.com/Jolls/enshu/issues/6))
- `.apkg` reading enforces archive ceilings (member count, per-member and total decompressed size) and checks a zstd frame's declared size before decompressing, so an uploaded package cannot exhaust memory ([#9](https://github.com/Jolls/enshu/issues/9))

## [0.1.1] - 2026-08-07
### Added
- Full data model in Drizzle: content tables, `deck_access`, `user_card_state`, append-only `review_log`, `user_fsrs_params`, and content-addressed media ([#4](https://github.com/Jolls/enshu/issues/4))
- `studyDayStart()` / `studyDayEnd()` — per-user 04:00-local study-day boundary computed in SQL ([#4](https://github.com/Jolls/enshu/issues/4))
- Isomorphic `uuidv7()` for client-generated, time-ordered ids ([#4](https://github.com/Jolls/enshu/issues/4))

### Removed
- The scaffold's placeholder `task` table ([#4](https://github.com/Jolls/enshu/issues/4))

## [0.1.0] - 2026-08-07
### Added
- SvelteKit + TypeScript (strict) scaffold: ESLint, Prettier, Vitest, Playwright, Drizzle + PostgreSQL, CI ([#3](https://github.com/Jolls/enshu/issues/3))
