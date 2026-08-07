# `.apkg` / `.colpkg` format and mapping

Extracted from [CLAUDE.md](../CLAUDE.md) §7. Read this before touching
`src/lib/server/apkg/`. Companion to the fixtures in `tests/fixtures/apkg/`.

> [!warning]
> **Everything below is from memory and has not been checked against a live Anki build or
> the format documentation.** Network access was unavailable when it was written. Treat it
> as a map of what to look for, not a contract. Verify each claim against a real export
> before relying on it, and correct this file in place when you do — noting which Anki
> version you verified against.

**No Anki-derived code.** File formats are not copyrightable, so the reader/writer is ours.
Copying `ankitects/anki` source (AGPLv3) would make the licence question permanent and
irreversible. Read format specs and clean-room parsers; never read Anki source into this
codebase.

---

## Container

`.apkg` is a zip containing:

- a SQLite collection — member named `collection.anki2`, `collection.anki21`, or
  `collection.anki21b` depending on the exporting version
- a `media` file: a JSON map of index to filename, `{"0":"cat.jpg","1":"audio.mp3"}`
- the media files themselves, named by those numeric indices

Newer Anki exports zstd-compress entries and add a protobuf `meta` file. `.colpkg` is the
same container for a whole collection rather than a deck selection.

---

## Collection schemas

Two must both be readable:

- **Schema 11 and earlier** — note types and decks live as JSON blobs in the single-row
  `col` table (`models`, `decks`, `dconf`, `conf`).
- **Schema 18+** — real tables: `notetypes`, `fields`, `templates`, `decks`, `deck_config`,
  `config`, `tags`.

Shared by both: `notes` (fields joined by `\x1f`, `guid`, `mid`, `csum`, `tags`), `cards`
(`nid`, `did`, `ord`, `type`, `queue`, `due`, `ivl`, `factor`, `reps`, `lapses`, `data`),
`revlog` (`cid`, `ease`, `ivl`, `lastIvl`, `factor`, `time`, `type`), `graves`.

---

## Field-level gotchas

Handle each of these explicitly in `read.ts`. They are the ones that produce plausible-
looking but wrong output if missed:

- `cards.due` means **days since `col.crt`** for review cards, but a **new-card position
  integer** for new cards. Converting requires `col.crt` and the rollover hour.
- `cards.ivl` is days when positive, **seconds when negative**.
- `cards.factor` is SM-2 ease × 1000 — meaningless under FSRS. Do not map it to difficulty.
- FSRS state on modern exports lives in `cards.data` as JSON (stability, difficulty, desired
  retention). Cards without it were never scheduled by FSRS.
- `queue` encodes suspension and burial as negatives (suspended, sched-buried, user-buried);
  these are separate concerns from `type`.
- `notes.csum` is a truncated SHA-1 of the first field, used for duplicate detection.
- Note fields are joined by `\x1f` (unit separator), not a printable delimiter.

---

## The intermediate representation

`src/lib/server/apkg/` produces and consumes an **IR**, never Drizzle rows directly:

```
import:  apkg -> IR -> db
export:  db  -> IR -> apkg
```

The IR is where format quirks are normalised, and it is what unit tests assert against.
Keeping it in the middle is what lets schema-11 and schema-18 readers converge before any
database code runs.

---

## Import

`apkg -> IR -> db`, with everything owned by the importing user.

- Cards with FSRS state in `cards.data` map straight into `user_card_state`.
- Cards with only SM-2 state get seeded — either FSRS defaults, or a replay of `revlog`
  through `ts-fsrs`, which is better and should be preferred when the log is present.
- `revlog` becomes `review_log`. It is the user's training data, and importing it is the
  difference between a cold start and a warm one.
- Re-import matches on `(deck_id, guid)` and **updates rather than inserting**. This is what
  makes re-import idempotent, and it only works because `guid` is stored on every note.

---

## Export

`db -> IR -> apkg` for one user, flattening their `user_card_state` back onto card rows.

Lossy in that direction by definition — a shared deck's other users' progress cannot be
represented in an Anki collection. That's fine and expected, but the UI should say so.

---

## Fixtures

`tests/fixtures/apkg/` with a README recording which Anki version produced each file and
what it exercises. **Collect these early** — they are the hardest test asset to produce
later and the format is where the unknown-unknowns live.

Coverage to aim for: schema 11 and schema 18+; with and without FSRS data; with media; with
cloze note types; with non-ASCII filenames; a `.colpkg` as well as `.apkg`.

Round-trip is the headline test: `import(export(import(f))) == import(f)`.
