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

**Nothing is committed yet — zero fixtures of any kind, real or synthetic.** Even the schema-11
findings folded into `docs/apkg-format.md` came from the geography deck above, which can never be
committed. The repo currently has no fixture it's actually allowed to test against.

### What to export, and why this specific set

One small hand-built collection, four exports, covers every dimension `docs/apkg-format.md`'s
Fixtures section asks for (schema 11 and 18+; with and without FSRS data; media; cloze; non-ASCII
filenames; `.colpkg` as well as `.apkg`) without a combinatorial number of files:

1. Build a tiny test collection in Anki: a **Basic** note type with 2–3 notes (attach an image to
   one, and rename another media file to something non-ASCII, e.g. `café.png` or `画像.png`,
   before attaching it), a **Cloze** note type with at least one note carrying `{{c1::}}` and
   `{{c2::}}` in the same field (exercises the "other cloze numbers show as plain text" rule in
   architecture.md §8), and a handful of real reviews on some of them (press Again/Good a few
   times) so `revlog` and `cards.data` carry real FSRS state — Anki defaults to FSRS since 23.10,
   so this falls out naturally.
2. Export four ways from that one collection:
   - `.apkg`, **"Support older Anki versions" checked** (schema 11, legacy container), **scheduling
     checked** → schema 11 + FSRS + cloze + media + non-ASCII, one file.
   - `.apkg`, **"Support older Anki versions" unchecked** (schema 18+, modern/zstd container),
     scheduling checked → the fixture that closes [#61](https://github.com/Jolls/enshu/issues/61)
     (verifying the protobuf field numbers `apkg-format.md` currently marks ❓).
   - Same as above, **scheduling unchecked** → covers "without FSRS data" for schema 18.
   - Full collection **`.colpkg`** export → always carries `revlog`; exercises the `.colpkg`
     container path specifically, which nothing else here does.
3. Drop all four in this directory, fill in the table above, and correct `docs/apkg-format.md` in
   place wherever a fixture disagrees with what it currently guesses.

---

## Synthetic fixtures

The superseded TypeScript reader had a `synthetic.ts` that built packages to the description in
[`docs/apkg-format.md`](../../../docs/apkg-format.md) — schema 11 and schema 18 variants of the
same logical collection, plus adversarial cases (out-of-`ord`-order field/template arrays, a
filtered-deck card with `due`/`odue` far enough apart that reading the wrong column can't pass by
coincidence). It was removed with the rest of that implementation (architecture.md §1).

**Rebuild its equivalent in Go, as test-helper code inside [#58](https://github.com/Jolls/enshu/issues/58)
(`.apkg` import), not as a standalone script producing committed binaries.** A synthetic fixture
is only as trustworthy as the spec it's built from, and `apkg-format.md` marks most of schema 18
❓ — unverified against a real export. Hand-building a synthetic schema-18 fixture ahead of a real
one risks baking today's guess into a "fixture" that looks authoritative but just tests itself.
The real exports above are what actually close those ❓ marks; synthetic fixtures are for the
adversarial cases no real export will conveniently contain (malformed `ord` arrays, `due`/`odue`
far enough apart to catch a column mix-up), which is exactly what makes them worth building as
parameterizable Go code rather than a handful of static files. See `docs/apkg-format.md`'s
Fixtures section for what made the TS-era fixtures worth having and the two properties worth
keeping adversarial.

**Replace synthetic fixtures with real exports as soon as one is available**, and correct
`docs/apkg-format.md` in place wherever the two disagree.
