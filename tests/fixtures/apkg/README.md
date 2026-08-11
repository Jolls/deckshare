# `.apkg` fixtures

Real exports from multiple Anki versions, used by the round-trip tests in CLAUDE.md §10.3.
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
architecture.md §12. Findings are recorded in words in
[`docs/apkg-format.md`](../../../docs/apkg-format.md) under "What the one real export
confirmed", and the two real-world traps it exposed are now covered by synthetic regression
tests (`buildDowngradeStubPackage()`).

**Still wanted: any schema-18 export** — a `.colpkg`, or an `.apkg` exported with "support
older Anki versions" turned *off*. That is the one fixture that would close issue #25. A
self-made deck of two notes is enough; it does not need to be anyone's real collection, which
also sidesteps the licensing problem entirely.

---

## Synthetic fixtures

The superseded TypeScript reader had a `synthetic.ts` that built packages to the description in
[`docs/apkg-format.md`](../../../docs/apkg-format.md) — schema 11 and schema 18 variants of the
same logical collection, plus adversarial cases (out-of-`ord`-order field/template arrays, a
filtered-deck card with `due`/`odue` far enough apart that reading the wrong column can't pass by
coincidence). It was removed with the rest of that implementation (architecture.md §1); rebuild
its equivalent in Go rather than skipping straight to "wait for a real export" — see
`docs/apkg-format.md`'s Fixtures section for what made those fixtures worth having and the two
properties worth keeping adversarial.

**Replace synthetic fixtures with real exports as soon as one is available**, and correct
`docs/apkg-format.md` in place wherever the two disagree.
