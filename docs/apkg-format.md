# `.apkg` / `.colpkg` format and mapping

Extracted from [architecture.md](architecture.md) §7. Read this before touching
`internal/apkg/` (architecture.md §4). Companion to the fixtures in `tests/fixtures/apkg/`.

> [!warning]
> **Everything below started as memory, unchecked against a live Anki build.** Treat an
> unverified claim as a map of what to look for, not a contract. Verify each against a real
> export before relying on it, and correct this file in place when you do.
>
> **Verification status as of 2026-08-07.** One real export has been inspected: a widely
> distributed community geography deck, v53, exported ~2025-04, 319 notes / 978 cards / 546
> media files. It is **schema 11 in a legacy container**, so it verifies that path and says
> nothing about schema 18. Claims below are tagged:
>
> - ✅ **verified** against that export
> - ❓ **unverified** — still memory only
>
> The whole of the schema-18 / protobuf section is ❓. Getting a schema-18 export (any
> `.colpkg`, or an `.apkg` exported with "support older Anki versions" switched off) is the
> single highest-value fixture still missing.

**No Anki-derived code.** File formats are not copyrightable, so the reader/writer is ours.
Copying `ankitects/anki` source (AGPLv3) would make the licence question permanent and
irreversible. Read format specs and clean-room parsers; never read Anki source into this
codebase.

---

## Container

`.apkg` is a zip containing:

- a SQLite collection — member named `collection.anki2`, `collection.anki21`, or
  `collection.anki21b` depending on the exporting version ✅
- a `media` file: a JSON map of index to filename, `{"0":"cat.jpg","1":"audio.mp3"}` ✅
- the media files themselves, named by those numeric indices ✅ (stored uncompressed)

✅ **A package can carry more than one collection, and the extra one is a trap.** The verified
export contains *both* `collection.anki21` (319 notes — the real deck) and `collection.anki2`
(**1 note**, a "please upgrade" placeholder for pre-2.1 Anki). Picking the wrong member imports
one note out of hundreds and reports success. `read.go` prefers `collection.anki21b`, then
`collection.anki21`, then `collection.anki2` — newest first — and that order is load-bearing,
not cosmetic. Regression test: `buildDowngradeStubPackage()`.

✅ **Legacy containers are still in active use.** The 2025 export's `meta` member is two bytes,
`08 02` — protobuf field 1 = **2**, not 3 — and nothing in the archive is zstd-compressed. Do
not assume a recent export implies the modern container.

Newer Anki exports zstd-compress entries and add a protobuf `meta` file. `.colpkg` is the
same container for a whole collection rather than a deck selection.

**Correction (2026-08-07, `feature/9-apkg-reader`).** The `media` map is only JSON in the
legacy container ✅. In the modern (`meta`-carrying, zstd) container it is a **protobuf list**
❓, where an entry's *position* in the list is the zip member name that the legacy JSON spelled
as an object key.

`internal/apkg/read.go` therefore does not branch on the package version at all: it
sniffs the zstd frame magic (`28 B5 2F FD`) and sniffs `{` versus a protobuf tag byte for the
media index. That is deliberate while this document is unverified — deriving the container
shape from the bytes cannot be wrong about a version number's meaning.

**Only the collection and the `media` index are zstd-compressed; the media files themselves
are stored as-is.** The reader sniffs those two members and no others. Sniffing every member
would be actively harmful: media bytes are arbitrary, so a legitimate image or audio file
whose first four bytes happen to be the zstd magic would be mangled or rejected. *(Which
members are compressed is itself unverified — if a real export turns out to compress media
too, this is the line to change.)*

**Packages are untrusted input** (shared decks are other users' bytes, architecture.md §8), so the
reader enforces ceilings on member count, per-member decompressed size and total decompressed
size, and checks a zstd frame's declared `Frame_Content_Size` *before* decompressing — the
native decompress call is one uninterruptible allocation, so a post-hoc check is too late.
See `ArchiveLimits` in `read.go`.

---

## Collection schemas

Two must both be readable:

- **Schema 11 and earlier** ✅ — note types and decks live as JSON blobs in the single-row
  `col` table (`models`, `decks`, `dconf`, `conf`). Still what a 2025 shared deck exports.
- **Schema 18+** ❓ — real tables: `notetypes`, `fields`, `templates`, `decks`, `deck_config`,
  `config`, `tags`. **No real schema-18 file has been inspected.**

Shared by both: `notes` (fields joined by `\x1f`, `guid`, `mid`, `csum`, `tags`), `cards`
(`nid`, `did`, `ord`, `type`, `queue`, `due`, `ivl`, `factor`, `reps`, `lapses`, `odid`,
`flags`, `data`), `revlog` (`cid`, `ease`, `ivl`, `lastIvl`, `factor`, `time`, `type`),
`graves`.

**Corrections (2026-08-07, `feature/9-apkg-reader`).** "Real tables" understates the work.
**Everything in this list about schema 18 is ❓** — the verified export was schema 11, so none
of it has been checked against a real file:

- Schema 18's configuration columns — `notetypes.config`, `fields.config`,
  `templates.config`, `decks.common`, `decks.kind` — are **protobuf BLOBs, not JSON**. Reading
  a schema-18 collection needs a protobuf decoder, which is why
  `internal/apkg/protobuf.go` exists. The field numbers it is driven by are recorded in
  `ankischema.go` and are themselves unverified.
- **Deck names spell their hierarchy differently in each schema**: schema 11's JSON name uses
  `::`, schema 18's `decks.name` column uses `\x1f`. The IR normalises both to `::`. Missing
  this silently flattens or mangles a deck tree.
- Schema 18 keeps the `col.models` / `col.decks` columns but empties them, so a reader that
  only checks whether they parse will see an empty collection rather than an error. Conversely,
  **`col.ver` is not trustworthy on its own**: a repacked or downgraded package can claim 18
  while carrying only the JSON blobs. `read.ts` decides from table presence — all four of
  `notetypes`, `fields`, `templates`, `decks` — and never from the version number.
- **`fields.ord` / `templates.ord` are authoritative, array order is not.** Schema 11 stores
  them as JSON arrays whose order can disagree with the `ord` values, while schema 18 has them
  as real rows. `notes.flds` is indexed by `ord`, so a reader trusting array order maps every
  field value onto the wrong field name — and the two schema readers silently disagree.
- Real schema-18 collections declare `COLLATE unicase` on the name columns. `better-sqlite3`
  (the old TypeScript reader's driver) could not register that collation, so any query that
  would *use* it failed. Whether `modernc.org/sqlite` (the Go reader's driver, architecture.md
  §4) can register it is unchecked — until verified, keep ordering by integer id only, same as
  before, and do not add `ORDER BY name`.

---

## Field-level gotchas

Handle each of these explicitly in `read.go`. They are the ones that produce plausible-
looking but wrong output if missed:

- `cards.due` means **days since `col.crt`** for review cards, but a **new-card position
  integer** for new cards. Converting requires `col.crt` and the rollover hour.

  **Correction (2026-08-07, `feature/9-apkg-reader`): there is a third meaning, `queue` — not
  `type` — is the discriminator, and for a displaced card the value is not even in this
  column.**

  Also retracted: *"Converting requires `col.crt` and the rollover hour."* ✅ It requires `crt`
  alone — `crt + days × 86400`, with `users.day_start_hour` **not** applied on top. Doing so
  would shift every imported review card. The rollover hour matters for *our* queue queries
  (`docs/schema.md`), not for this conversion.

  > **Superseded sub-claim (same day, corrected by the real export).** A first pass at this
  > retraction asserted `col.crt` "is already the rollover instant, not midnight — 04:00 local".
  > That is not supportable. The verified export's `crt` is `1362182400` =
  > **2013-03-02T00:00:00Z, exactly midnight UTC** and exactly divisible by 86400. It may be
  > 04:00 local for a UTC+4 author, or Anki may normalise `crt` on export; neither can be told
  > from one file. Treat `crt` as an **opaque anchor**: use it verbatim, add nothing to it, and
  > do not assume it encodes any particular local hour. ❓ what it means; ✅ that using it
  > verbatim is what the format wants.

  | `queue` | `due` means | Example (`crt` = 2024-01-01T04:00Z) |
  |---|---|---|
  | `0` new | queue **position**, no calendar meaning | `3` → the fourth new card |
  | `1` learning, `4` preview | **epoch seconds** | `1704085200` → 2024-01-01T05:00Z |
  | `2` review, `3` day-learning | **days since `col.crt`** | `5` → 2024-01-06T04:00Z |
  | `-1`/`-2`/`-3` suspended, buried | ambiguous — the hold overwrote the queue | see below |
  | *any, when `odid != 0`* | **read `odue` instead** — see below | `due = -12345`, `odue = 7` → 2024-01-08T04:00Z |

  Using `type` as the discriminator is wrong in both directions: a `type = 2` (review) card
  sits in `queue = 3` (day-learning) mid-relearn with a *day offset*, and a `type = 3`
  (relearning) card sits in `queue = 1` with an *epoch-seconds timestamp*.

  **`cards.odue` shadows `cards.due` whenever `odid != 0`.** Moving a card into a filtered
  ("cram") deck overwrites `due` with the filtered deck's own ordering value and stashes the
  card's real due value in `odue`. A reader that takes `due` for those cards imports a schedule
  that was never theirs — the same class of silent wrongness as the two unit traps, and it
  affects every card that is currently in a filtered deck, not a rare corner. How the value is
  *interpreted* is unchanged by this; only which column it is read from. (`odid` is also what
  says where the card actually lives: `did` is the filtered deck, `odid` the real home.)

  A hold is the one genuinely ambiguous case, because the negative `queue` has replaced the
  queue the card came from. `type = 0` still means a position; otherwise the magnitude is the
  only signal, and `read.go` treats `due ≥ 1_000_000_000` as epoch seconds (day offsets are
  counted from collection creation and stay in the thousands).

- `cards.ivl` is days when positive, **seconds when negative**.

  **Correction (2026-08-07):** the same encoding applies to **`revlog.ivl` and
  `revlog.lastIvl`**, not just `cards.ivl`. A negative value is how a sub-day learning step is
  represented: `ivl = -600` is ten minutes. Read naively it becomes a 600-day interval — an
  86,400× error that looks entirely plausible in a card browser. The IR carries seconds
  throughout so the distinction cannot reappear.
- `cards.factor` is SM-2 ease × 1000 — meaningless under FSRS. Do not map it to difficulty.
- FSRS state on modern exports lives in `cards.data` as JSON (stability, difficulty, desired
  retention). Cards without it were never scheduled by FSRS.
- `queue` encodes suspension and burial as negatives (suspended, sched-buried, user-buried);
  these are separate concerns from `type`.
- `notes.csum` is a truncated SHA-1 of the first field, used for duplicate detection.
- Note fields are joined by `\x1f` (unit separator), not a printable delimiter.

Added 2026-08-07 while writing the reader, same unverified status as the rest:

- **`notes.id`, `cards.id` and `revlog.id` are epoch *milliseconds*; `mod` columns are epoch
  *seconds*.** Mixing them up is a three-orders-of-magnitude timestamp error. `revlog.id`
  doubles as the review instant.
- **`cards.odid` / `cards.odue`** are both set while a card sits in a filtered deck: `did` is
  then the filtered deck and `odid` the card's real home, and `odue` holds the real due value
  that `due` has been overwritten with. We have no filtered decks, so the IR files the card
  under `odid`, keeps `did` only as `filteredDeckAnkiId`, and resolves the due date from
  `odue`. See the `cards.due` correction above.
- **A note's cards can span several decks.** Anki scopes decks to cards; we scope them to notes
  (`notes.deck_id` is a single column), so the reader must pick one. Policy: the resolved home deck of
  the note's **lowest-numbered card** — deterministic across re-imports, which a
  majority-of-cards or first-row rule is not. See `IrNote.primaryDeckAnkiId`.
- **`cards.flags`**: only the low three bits are the flag colour.
- **`notes.tags`** is space-separated *and* space-surrounded (`" kanji jlpt-n5 "`), so a naive
  split yields empty tags.
- **Media filenames are NFC.** A package produced on macOS can carry NFD in the index; without
  normalising, the filename will not match the `[sound:…]` / `<img src=…>` reference in the
  note field it belongs to.
- **`cards.data`** is JSON with short keys — `pos` (preserved new-card position), `s`, `d`,
  `dr` (FSRS stability, difficulty, desired retention). These key names are among the least
  verified claims here; `read.go` treats an unparseable or unrecognised `data` as absent rather
  than failing the import.

---

## The intermediate representation

`internal/apkg/` produces and consumes an **IR**, never `sqlc`-generated rows directly:

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
  through `go-fsrs`, which is better and should be preferred when the log is present.
- `revlog` becomes `review_log`. It is the user's training data, and importing it is the
  difference between a cold start and a warm one.
- Re-import matches on `(owner_id, guid)` and **updates rather than inserting**. This is what
  makes re-import idempotent, and it only works because `guid` is stored on every note.

### Anki keeps both current state and full history

Worth being explicit, because it is what makes a warm import possible at all. Anki does **not**
roll history into a running state and discard it — it keeps two things:

- **Current scheduling state**, on the `cards` row (`due`, `ivl`, `factor`, `reps`, `lapses`,
  `queue`, `type`, and FSRS memory state inside `cards.data`).
- **A complete append-only `revlog`**, one row per answer, holding the button pressed, the
  interval before and after, the answer duration, and the review type.

`revlog` is not pruned in normal use, which is exactly why the FSRS optimiser can fit
parameters against a collection someone has been using for years. Our split is the same idea
with the multiuser seam cut through it: their `cards` row becomes our `user_card_state`
(per user), and their `revlog` becomes our `review_log` (per user, append-only, CLAUDE.md
§2.5).

> [!warning]
> **Whether a given export carries `revlog` is a separate question from whether Anki stores
> it.** A `.colpkg` collection backup includes it; a `.apkg` deck export includes scheduling
> only when the user ticks the option, and the one real export inspected so far had **0 revlog
> rows** (see below). So the importer must treat an absent or empty `revlog` as the normal
> case and fall back to seeding from `cards.data`, not as a malformed package. Exactly which
> export options produce which tables is unverified — confirm it against fixtures.

---

## Export

`db -> IR -> apkg` for one user, flattening their `user_card_state` back onto card rows.

Lossy in that direction by definition — a shared deck's other users' progress cannot be
represented in an Anki collection. That's fine and expected, but the UI should say so.

---

## What the one real export confirmed

Inspected 2026-08-07 (a widely distributed community geography deck, v53, exported ~2025-04).
The deck itself is not committed — it is third-party content under its own licence, which is
exactly the open question in architecture.md §12.

Shape: schema 11, legacy container, `meta` = version 2, 1 note type (8 fields, 4 templates),
2 decks, 319 notes, 978 cards, 0 revlog rows, 546 media files.

Confirmed by reading it end to end with no errors and no warnings:

- The schema-11 JSON blob layout: `col.models` / `col.decks`, field and template shapes,
  `flds`/`tmpls` array members, `sortf`, `type`, `css`, deck `desc` and `dyn`.
- `notes.flds` splitting on `\x1f` — every one of the 319 notes yields exactly 8 fields,
  matching the note type's field count, including empty ones in the middle.
- `notes.tags` space-surrounded parsing, and `notes.id` as epoch **milliseconds**.
- **Card generation matches the templates semantically**, which is the strongest single check:
  ordinal 3 (an unconditional template) produced 319 cards, one per note, while the three
  `{{#Field}}`-conditional templates produced 219 / 219 / 221. A field-ordering or
  template-parsing error would not land on those numbers.
- `cards.due` as a **position** for new cards — all 978 are new, positions 1..1398, sparse
  exactly because conditional templates skip ordinals.
- **The media index resolves perfectly**: 546 entries, 546 distinct filenames referenced by
  `<img src=…>` in note content, **zero dangling and zero unreferenced** in either direction.
- Media stored uncompressed; mimes 319 PNG + 227 SVG.
- Reading the same file twice produces an identical IR.

Not exercised by it, and therefore still ❓: everything schema-18 (real tables, all protobuf
field numbers, `COLLATE unicase`), the zstd container, the protobuf media index, FSRS state in
`cards.data`, `revlog`, `odue`/filtered decks, negative `ivl`, suspension and burial, and
non-ASCII media filenames — this deck has none of them.

---

## Fixtures

`tests/fixtures/apkg/` with a README recording which Anki version produced each file and
what it exercises. **Collect these early** — they are the hardest test asset to produce
later and the format is where the unknown-unknowns live.

Coverage to aim for: schema 11 and schema 18+; with and without FSRS data; with media; with
cloze note types; with non-ASCII filenames; a `.colpkg` as well as `.apkg`.

Round-trip is the headline test: `import(export(import(f))) == import(f)`.
