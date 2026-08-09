# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]
### Added
- `.apkg` / `.colpkg` reader: package → intermediate representation, covering collection schemas 11 and 18+, the legacy and zstd containers, and content-addressed media ([#9](https://github.com/Jolls/enshu/issues/9))
- Synthetic `.apkg` fixtures built to `docs/apkg-format.md`, pending real Anki exports ([#9](https://github.com/Jolls/enshu/issues/9))
- Collection-member preference regression test: a package carrying both `collection.anki21` and a one-note `collection.anki2` downgrade stub must read the former ([#9](https://github.com/Jolls/enshu/issues/9))
- The reviewer and the write queue: one-request session start, synchronous local grading, a durable `localStorage` queue draining to an idempotent `POST /api/reviews/batch`, and the server-side `review_log` recompute path ([#13](https://github.com/Jolls/enshu/issues/13))
- `defaultFsrsParams()` — the `ts-fsrs` defaults a user schedules with before their first optimiser fit ([#13](https://github.com/Jolls/enshu/issues/13))
- Browser-facing signup, login, and logout pages wrapping the existing `/(auth)/*` JSON endpoints, with client-side validation mirroring the server's email/password rules ([#28](https://github.com/Jolls/enshu/issues/28))
- Deck list/create, an auto-seeded default "Basic" note type, add-note form, and note list/delete pages under `src/routes/(app)/decks/` — the minimum UI to create study material without `curl` ([#29](https://github.com/Jolls/enshu/issues/29))
- Re-import dedup keys: `UNIQUE (owner_id, name)` on `decks` and `note_types`, `notes.owner_id` with `UNIQUE (owner_id, guid)`, and `review_log.anki_id` with `UNIQUE (user_id, card_id, anki_id)` ([#32](https://github.com/Jolls/enshu/issues/32))
- `decks.anki_id`, for `.apkg` round-trip export fidelity ([#32](https://github.com/Jolls/enshu/issues/32))
- `DuplicateNameError` → HTTP 409 on the deck and note-type create/rename endpoints, now that a name is a uniqueness key ([#32](https://github.com/Jolls/enshu/issues/32))

### Changed
- Note identity is per owner, not per deck: `notes (deck_id, guid)` → `notes (owner_id, guid)`, with a plain `notes (deck_id)` index kept for deck-scoped queries. Amends the CLAUDE.md §2.2 invariant — `guid` is globally unique in Anki, so a deck-scoped key made note idempotency depend on deck dedup landing first ([#32](https://github.com/Jolls/enshu/issues/32))
- Deck and note-type re-import dedup on name rather than `anki_id`: Anki ids are per-collection (deck id 1 is `Default` in every collection), so an `anki_id` key would silently merge two unrelated imported decks and two different note types ([#32](https://github.com/Jolls/enshu/issues/32))

### Fixed
- `docs/apkg-format.md`: `cards.due` has three meanings discriminated by `queue`, not two by `type`, and `cards.odue` shadows it entirely for cards in a filtered deck; the negative-`ivl` seconds encoding also applies to `revlog`; schema-18 configs are protobuf; deck-name separators and field ordering differ per schema; the modern media index is protobuf ([#9](https://github.com/Jolls/enshu/issues/9))

### Security
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
