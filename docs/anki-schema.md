# Anki's own schema (reference)

This is Anki's SQLite schema, table by table — not DeckShare's. Companion to
[schema.md](schema.md), which is *our* schema and the one to edit when adding a table or
column here. This file exists so a reader can answer "what column holds X in a real
`.anki2`/`.anki21` file" without re-deriving it from [apkg-format.md](apkg-format.md)'s
import/export prose each time.

Division of labour between the three Anki-facing docs:

- **This file** — the table/column shapes: names, types, what each column means.
- [apkg-format.md](apkg-format.md) — the container (`.apkg`/`.colpkg` zip), which schema
  version ships in which export, and the encoding gotchas (`due`'s three meanings, negative
  `ivl`, `odue`/`odid` shadowing) that make reading these columns correctly hard. Read that
  file for *how to interpret* a value; read this one for *where it lives*.
- [schema-diagram.md](schema-diagram.md) / this file's own diagram — visual company to both.

> [!warning]
> **Same status as `apkg-format.md`: started as memory, unchecked against a live Anki build
> table-by-table.** The one verified export inspected so far (schema 11, legacy container —
> see `apkg-format.md`) confirms the *shapes* used by the reader (`col.models`/`col.decks`
> JSON, `notes`/`cards`/`revlog` columns) but this file also documents columns the reader
> doesn't touch (e.g. `graves`, most of `col.dconf`, all of schema 18's protobuf configs).
> Treat anything not cross-referenced against `apkg-format.md`'s ✅ list as ❓ — a map of where
> to look, not a contract. Correct in place when verified.

**No Anki-derived code applies to this file too, in spirit.** Nothing here is transcribed
from `ankitects/anki` source (CLAUDE.md §2.8) — SQLite schemas and file formats are not
copyrightable, and this is a clean-room reconstruction from the format's observable behaviour
and general public documentation, the same standard `apkg-format.md` holds itself to.

---

## Schema versions

Anki has shipped several `col.ver` values over its life; only the two boundaries this
codebase cares about are documented here (the same split `apkg-format.md` uses):

- **Schema 11 and earlier** ✅ — note types, decks, deck options and (in still-older versions)
  tags live as JSON blobs inside the single-row `col` table. This is what the one verified
  export (v53, exported ~2025-04) actually contains, and it is still what a shared deck
  commonly exports as, "support older Anki versions" or not.
- **Schema 18+** ❓ — note types, decks, deck options, and config move into real tables
  (`notetypes`, `fields`, `templates`, `decks`, `deck_config`, `config`), and their per-row
  settings column becomes a **protobuf blob**, not JSON. No real schema-18 file has been
  inspected yet; this section is the single highest-value gap to close with a fixture.

`notes`, `cards`, `revlog`, and `graves` keep the same shape across both — they were never
part of the schema-18 migration.

---

## Tables present in both schemas

### `col`

Exactly one row. Global collection state and, pre-18, the JSON blobs that hold everything
schema 18 promotes to real tables.

| Column | Type | Meaning |
|---|---|---|
| `id` | integer | Always `1`. |
| `crt` | integer | Collection creation time, **epoch seconds**. Anchor for `cards.due`'s day-offset meaning — used verbatim, see `apkg-format.md`. |
| `mod` | integer | Last modified, epoch **milliseconds**. |
| `scm` | integer | Schema-modification time, epoch ms. Bumped on any change that forces clients to do a full sync rather than incremental. |
| `ver` | integer | Schema version. **Not trustworthy alone** to detect schema 18 — `apkg-format.md` notes a repacked/downgraded package can claim 18 while still carrying only the legacy JSON blobs; detect from table presence instead. |
| `dty` | integer | "Dirty" flag. Legacy, unused in modern Anki — always `0`. |
| `usn` | integer | Update sequence number, for AnkiWeb sync. Irrelevant here (§9, no sync protocol). |
| `ls` | integer | Last-sync time, epoch ms. Irrelevant here for the same reason. |
| `conf` | text (JSON) | Collection-level config: current deck, active decks, day-cutoff timer settings, sort order, etc. Schema 18 empties this progressively as `config` (the table) takes over key by key. |
| `models` | text (JSON) | ❓ schema ≤11: object keyed by note-type id → note-type definition. See [Legacy JSON blob shapes](#legacy-json-blob-shapes-schema-11). Present but emptied on schema 18. |
| `decks` | text (JSON) | ❓ schema ≤11: object keyed by deck id → deck definition. Present but emptied on schema 18. |
| `dconf` | text (JSON) | ❓ schema ≤11: object keyed by deck-config id → deck options (new/review/lapse steps). Present but emptied on schema 18. |
| `tags` | text (JSON) | Legacy tag registry: object mapping tag name → usn. Superseded by the `tags` table even pre-18 in recent versions; kept for compatibility. |

### `notes`

One row per note (the field values; card generation happens via `cards`).

| Column | Type | Meaning |
|---|---|---|
| `id` | integer | PK. Epoch **milliseconds** at creation — this is also what `notes.guid` collisions get disambiguated against on Anki's side, not what we key on (we key on `guid`). ✅ |
| `guid` | text | Globally unique across the note's lifetime, survives export/import round-trips. The idempotency key CLAUDE.md §2.2 is built on. |
| `mid` | integer | Note-type id. FK into `col.models` (≤11) or `notetypes.id` (18+). |
| `mod` | integer | Last modified, epoch **seconds** (note: `notes.id`/`cards.id`/`revlog.id` are ms, `mod` columns are seconds — easy to cross, see `apkg-format.md`). |
| `usn` | integer | Sync bookkeeping, irrelevant here. |
| `tags` | text | Space-separated **and space-surrounded** (`" kanji jlpt-n5 "`) — a naive split on the first/last tag yields an empty string. ✅ |
| `flds` | text | All field values joined by `\x1f` (unit separator), in `fields.ord` order — not necessarily the order they appear in `col.models`'s JSON array on schema ≤11, where array order and `ord` can disagree. ✅ |
| `sfld` | text/integer | Sort field: the value of whichever field `models[mid].sortf` designates, duplicated here so the browser can sort/search without re-parsing `flds`. |
| `csum` | integer | Truncated SHA-1 of the first field, used for duplicate-note detection on import. ✅ |
| `flags` | integer | Reserved; unused in current Anki. |
| `data` | text | Reserved, unused. Not to be confused with `cards.data`, which is very much used (FSRS memory state). |

### `cards`

One row per generated card. Content addressing plus **current** scheduling state — the thing
DeckShare invariant §2.1 splits into `cards` (content) and `user_card_state` (per-user state).

| Column | Type | Meaning |
|---|---|---|
| `id` | integer | PK. Epoch **milliseconds**. |
| `nid` | integer | FK → `notes.id`. |
| `did` | integer | FK → deck id. **The filtered ("cram") deck while the card is in one** — see `odid` below. |
| `ord` | integer | Which template on the note type generated this card. Indexes `templates`/`col.models[mid].tmpls`. |
| `mod` | integer | Epoch seconds. |
| `usn` | integer | Sync bookkeeping. |
| `type` | integer | `0` new, `1` learning, `2` review, `3` relearning. **Not the `due` discriminator** — `queue` is, see `apkg-format.md`. |
| `queue` | integer | `0` new, `1` learning, `2` review, `3` day-learning, `4` preview, and negative for held cards: `-1` suspended, `-2` sched-buried, `-3` user-buried. |
| `due` | integer | Meaning depends on `queue` (position / epoch seconds / days-since-`col.crt`) — full table in `apkg-format.md`, not repeated here since that's a semantics doc, not a shape doc. |
| `ivl` | integer | Interval: **days when positive, seconds when negative** (sub-day learning steps). |
| `factor` | integer | SM-2 ease × 1000. Meaningless once a card is under FSRS. |
| `reps` | integer | Total review count. |
| `lapses` | integer | Times answered "Again" from a review state. |
| `left` | integer | Learning-step bookkeeping: steps remaining today encoded in the low 3 digits, steps remaining total in the rest (`stepsToday + steps*1000`, approximately — treat as opaque and re-derive from the scheduler rather than parsing, since the exact packing has shifted across versions). ❓ |
| `odue` | integer | Original `due`, stashed here while the card sits in a filtered deck and `due` holds the filtered deck's own ordering value. Shadows `due` whenever `odid != 0` — see `apkg-format.md`. |
| `odid` | integer | Original (home) deck id while filtered; `0` when not in a filtered deck. |
| `flags` | integer | Low 3 bits are the flag colour (0 none, 1–7 the seven flag colours); higher bits unused. |
| `data` | text (JSON) | On modern exports: FSRS memory state — short keys `s` (stability), `d` (difficulty), `dr` (desired retention), `pos` (preserved new-card position). Absent or unparseable on cards never scheduled by FSRS; treat as absent rather than an error (`apkg-format.md`). |

### `revlog`

Append-only review history — the direct ancestor of our `review_log` (CLAUDE.md §2.5). One
row per answer, never rolled up.

| Column | Type | Meaning |
|---|---|---|
| `id` | integer | PK. Epoch **milliseconds** — doubles as the review's timestamp. |
| `cid` | integer | FK → `cards.id`. |
| `usn` | integer | Sync bookkeeping. |
| `ease` | integer | Button pressed: `1`–`4`. `0` for a manual reschedule (not a real answer). |
| `ivl` | integer | Interval set by this review. Same day/second sign convention as `cards.ivl`. |
| `lastIvl` | integer | Interval the card had *before* this review. Same sign convention. |
| `factor` | integer | Ease factor after this review (SM-2, ×1000). `0` where not applicable (e.g. learning-step answers, or FSRS-scheduled cards). |
| `time` | integer | Milliseconds taken to answer, capped by the collection's "stop timer" setting. |
| `type` | integer | `0` learning, `1` review, `2` relearning, `3` cram/filtered-deck, `4` manual reschedule ("Set due date" / "Reset"). ❓ exact enum ordering unverified against a real export. |

### `graves`

Tombstones for sync-propagated deletion — irrelevant to DeckShare's import path (we don't sync)
but present in every collection.

| Column | Type | Meaning |
|---|---|---|
| `usn` | integer | Sync bookkeeping. |
| `oid` | integer | Id of the deleted object (a note, card, or deck id, per `type`). |
| `type` | integer | `0` card, `1` note, `2` deck. ❓ |

---

## Legacy JSON blob shapes (schema ≤11)

`col.models`, `col.decks`, `col.dconf` are each a JSON **object** keyed by the entity's id
(as a string) rather than an array — this is why schema-18's real tables are a cleaner shape
to read, and why the IR normalises both into the same internal form (`apkg-format.md`).

### `col.models[id]` — note type

```
{
  id, name, type,        // type: 0 = standard, 1 = cloze
  mod, usn,
  sortf,                 // ordinal of the sort field
  did,                   // default deck for new cards of this type
  tmpls: [{ name, ord, qfmt, afmt, did, bqfmt, bafmt, bfont, bsize }],
  flds:  [{ name, ord, sticky, rtl, font, size }],
  css,
  latexPre, latexPost,
  req: [[ord, "any"|"all"|"none", [fieldOrds]]],  // which fields must be filled for a
                                                    // conditional template to generate a card
  tags, vers
}
```

`tmpls[].ord` and `flds[].ord` are authoritative; the *array* position can disagree with
`ord` (`apkg-format.md`), which is the trap a reader keying on array index falls into.

### `col.decks[id]` — deck

```
{
  id, name,               // hierarchy separator is "::" in this JSON form
  mod, usn,
  collapsed, browserCollapsed,
  newToday, revToday, lrnToday, timeToday,  // [usn, count] pairs, legacy per-day counters
  conf,                    // deck-config id, FK into col.dconf
  desc,
  dyn,                      // 0 normal, 1 filtered ("cram") deck
  extendRev, extendNew       // filtered-deck-only fields when dyn = 1
}
```

Schema 18's `decks.name` column instead separates hierarchy with `\x1f` — a difference the IR
must normalise, not just pass through (`apkg-format.md`).

### `col.dconf[id]` — deck options

```
{
  id, name, mod, usn,
  maxTaken, autoplay, timer, replayq,
  new:   { bury, delays, initialFactor, ints, order, perDay },
  rev:   { bury, ease4, fuzz, ivlFct, maxIvl, minSpace, perDay },
  lapse: { delays, leechAction, leechFails, minInt, mult },
  dyn
}
```

FSRS parameters and desired retention, where present on a modern collection, live in a deck
option group's config too — on schema ≤11 this would be additional keys on this object; on
schema 18+ they're fields in `deck_config.config`'s protobuf. Neither has been confirmed
against a real export with FSRS-optimised deck options.

---

## Tables introduced in schema 18+ (all ❓, unverified)

Real tables replacing the `col.models`/`col.decks`/`col.dconf`/`col.tags` JSON blobs. Each
row's settings live in a `config`/`common`/`kind` column that is a **protobuf message, not
JSON** — decoding it needs a schema (field numbers), which is what
`internal/apkg/protobuf.go` and `ankischema.go` exist to hold, and both are themselves
unverified against a real schema-18 file.

| Table | Key columns | Config column(s) | Notes |
|---|---|---|---|
| `notetypes` | `id`, `name`, `mtime_secs`, `usn` | `config` (protobuf) | Replaces `col.models[id]`'s top-level fields (`type`/`sortf`/`css`/`req` move into the protobuf). |
| `fields` | `ntid` (FK), `ord`, `name` | `config` (protobuf) | Replaces `flds[]` entries. Protobuf carries `sticky`/`rtl`/`font`/`size` plus newer options (description, plaintext, exclude-from-search) `col.models` never had. |
| `templates` | `ntid` (FK), `ord`, `name`, `mtime_secs`, `usn` | `config` (protobuf) | Replaces `tmpls[]` entries: `q_format`/`a_format`/browser variants/target deck. |
| `decks` | `id`, `name` (`\x1f`-separated hierarchy), `mtime_secs`, `usn` | `common` (protobuf), `kind` (protobuf oneof: normal vs. filtered params) | Replaces `col.decks[id]`. |
| `deck_config` | `id`, `name`, `mtime_secs`, `usn` | `config` (protobuf) | Replaces `col.dconf[id]`. New/review/lapse steps, timer, and — on collections that have used FSRS — the fitted parameter array and desired retention live here. |
| `config` | `key` (PK, text), `usn`, `val` (blob) | — | Replaces `col.conf`'s JSON, one row per setting, migrated incrementally rather than all at once. |
| `tags` | `tag` (PK, text), `usn` | — | Replaces `col.tags`'s JSON registry. |

Notes carried over from `apkg-format.md`, restated here because they're schema-shape facts
rather than import-semantics ones:

- `COLLATE unicase` is declared on schema-18 name columns. `better-sqlite3` (the old
  TypeScript reader's driver) couldn't register that collation, so any query relying on it
  (`ORDER BY name`) failed. Whether `modernc.org/sqlite` (the Go reader's driver,
  architecture.md §4) can is unchecked — order by integer id instead until verified.
- `col.models`/`col.decks` still exist as columns on schema 18 but are emptied, not dropped —
  a reader that only checks "does this parse" sees an empty collection, not an error.

---

## What the one real export confirmed, mapped to this file

From `apkg-format.md`'s verified-export section (schema 11, legacy container, v53,
~2025-04): the `col.models`/`col.decks` JSON shapes above, `notes.flds` splitting cleanly on
`\x1f` into exactly the note type's field count, `notes.tags` space-surrounded parsing,
`notes.id` as epoch ms, `cards.due` as a new-card position, and reading twice producing an
identical result. Everything tagged ❓ in this file — all of schema 18, `graves`, `revlog.type`
enum values, `cards.left`'s exact packing, deck-config JSON keys beyond the well-known ones —
is not yet checked against a real file. Getting a schema-18 fixture remains the single
highest-value gap (`apkg-format.md`).
