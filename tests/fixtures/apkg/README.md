# `.apkg` fixtures

Real exports from multiple Anki versions, used by the round-trip tests in CLAUDE.md §10.2.
Collect these early — per CLAUDE.md §10, they are the hardest test asset to produce later and
the format is where the unknown-unknowns live.

For each fixture, record here:

- **File name**
- **Anki version** that produced it (and OS, if it might matter)
- **Collection schema** — 11 (note types/decks as JSON blobs in `col`) or 18+ (real tables)
- **What it exercises** — e.g. cloze notes, media, non-ASCII filenames, FSRS data present,
  multiple note types, empty decks

| File | Anki version | Schema | Exercises |
|---|---|---|---|
| _(none committed)_ | | | |

### Inspected but not committed

A real widely-distributed community geography deck (v53, exported ~2025-04) was read end to end
on 2026-08-07 to verify the format. **It is not in the repo and must not be**: it is
third-party deck content under its own licence, which is precisely the open question in
CLAUDE.md §12. Findings are recorded in words in
[`docs/apkg-format.md`](../../../docs/apkg-format.md) under "What the one real export
confirmed", and the two real-world traps it exposed are now covered by synthetic regression
tests (`buildDowngradeStubPackage()`).

**Still wanted: any schema-18 export** — a `.colpkg`, or an `.apkg` exported with "support
older Anki versions" turned *off*. That is the one fixture that would close issue #25. A
self-made deck of two notes is enough; it does not need to be anyone's real collection, which
also sidesteps the licensing problem entirely.

---

## Synthetic fixtures — `synthetic.ts`

> **These are not real Anki exports.** No Anki build was available when the reader was written
> (`feature/9-apkg-reader`), so `synthetic.ts` builds packages **to the description in
> [`docs/apkg-format.md`](../../../docs/apkg-format.md)**, which is itself explicitly
> unverified (CLAUDE.md §7). Every claim they encode — table DDL, JSON member names, protobuf
> field numbers, `cards.data` key names — is that document's claim, written by the same hands
> that wrote the reader.

What they can prove: the reader is self-consistent with the documented format. Both schemas
converge on one IR, the `due` and `ivl` encodings are normalised, media is deduplicated and
NFC-normalised, and the zstd/protobuf container unwraps.

What they cannot prove: that the documented format is right. **The protobuf field numbers in
`src/lib/server/apkg/anki-schema.ts` are the sharpest edge** — a synthetic fixture writes them
with the same constants it reads them with, so a wrong number passes every test here and
produces an empty or garbled note type against a real export.

Both builders encode the *same* logical collection, which is what makes the schema-convergence
test meaningful.

| Builder | Schema | Container | Exercises |
|---|---|---|---|
| `buildSchema11Package()` | 11 | plain zip, JSON media index | note types/decks as `col` JSON blobs, `::` deck names |
| `buildSchema18Package()` | 18 | zstd collection + media index, uncompressed media, `meta`, protobuf media index | `notetypes`/`fields`/`templates`/`decks` tables, protobuf configs, `\x1f` deck names |
| `buildSchema18ClaimWithoutTablesPackage()` | claims 18, has none | plain zip | `col.ver` lying about the layout — the reader must decide from table presence |
| `buildDowngradeStubPackage()` | 11 | plain zip, two collections | `collection.anki21` (full) beside a one-note `collection.anki2` downgrade stub — **modelled on a real export**, not invented |

Shared content: two note types (one standard with three fields and three templates, one
cloze), three decks including a filtered one and a subdeck, two notes (one with non-ASCII
content, empty trailing field and surrounding-space tags; one cloze), eight cards covering
every `cards.due` and `cards.ivl` case, two `revlog` rows with positive and negative
intervals, FSRS state in `cards.data`, and three media files — one duplicated by content under
two names, one with an NFD non-ASCII filename.

Two details are deliberately adversarial and should stay that way:

- The filtered-deck card's `due` (`-12345`) and `odue` (`7`) are far apart, so a reader that
  takes the wrong column cannot pass by coincidence.
- One note type's `flds` and one note type's `tmpls` are listed **out of `ord` order**. Without
  that, the schema-convergence test cannot notice a reader that trusts array order.

**Replace these with real exports as soon as one is available**, and correct
`docs/apkg-format.md` in place wherever the two disagree.
