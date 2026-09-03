# Issue #124 — Audit: `internal/apkg/`

Audit zone: `internal/apkg/` in full — `read.go` 812, `dbwrite.go` 561, `write.go` 261, `ir.go` 241,
`dbexport.go` 275, `ankischema.go` 154, `protobuf.go` 147, `media.go` 143, `errors.go` 17,
`doc.go` 9, plus `synthetic_test.go` 818, `dbwrite_test.go` 562, `read_test.go` 354,
`container_test.go` 313, `write_test.go` 264, `dbtest_test.go` 70, `media_render_test.go` 69.
17 files, 5070 lines, verified against the current tree. There is no `container.go`; the container
logic lives in `read.go` (`openArchive`/`memberBytes`/`decompressZstd`/`pickCollectionMember`) and
`container_test.go` is its test file.

Default intent is simplification/cleanup with **no behavior change**. Section 2 resolves the
architecture.md §20 deviation the issue flags. Section 3 is the cleanup, every item a literal
find/replace. Anything needing a decision is in **Open questions**, not decided here.

Clean-room constraint honoured: nothing below was derived from `ankitects/anki` source
(CLAUDE.md §2.8, §17). Every format fact cited traces to `docs/apkg-format.md`,
`docs/anki-schema.md`, or this repo's own parser and fixtures.

---

## 1. What was checked and found correct — no edits proposed

Stated explicitly so the reader knows these were covered, not skipped.

**The IR boundary holds (architecture.md §4, apkg-format.md "The intermediate representation").**
Verified by import graph, not by eye: `read.go`, `write.go`, `media.go`, `protobuf.go`,
`ankischema.go`, `ir.go` and `errors.go` import no database package at all. Only `dbwrite.go`
(IR → db) and `dbexport.go` (db → IR) name `internal/db` types, which is exactly what those two
files are for. No `sqlc`-generated row reaches the format layer, and no `IrX` type carries a
`pgtype` field. `deriveCardScheduling(state db.UserCardState, …)` (`dbexport.go:191`) taking a
generated row is **not** a violation — it is a private helper of the db-facing translator, on the
db side of the seam. Edit 27 records this boundary in `doc.go` so it does not drift.

**`resolveDue`'s queue/odid/odue disambiguation** (`ir.go:179-202`) matches apkg-format.md's table
line for line, including the two traps architecture.md §7 calls out: `due` is a position for
`queue == 0` and days-since-`crt` for `queue == 2/3`, and the discriminator is `queue`, not `type`.
The held-card (`queue < 0`) fallback correctly re-checks `type == 0` first and only then applies
`epochSecondsThreshold`. Covered by `read_test.go:177-215` (eight cases including both held
branches) and round-tripped by `write_test.go:17-51`.

**`intervalSeconds`/`unintervalSeconds`** (`ir.go:143-148`, `write.go:256-261`) implement
apkg-format.md's dual-unit `ivl` (days positive, seconds negative) and are a genuine inverse pair,
pinned by `write_test.go:53-61` over both branches. The IR carrying seconds throughout is the right
normalisation and is applied consistently to `cards.ivl`, `revlog.ivl` and `revlog.lastIvl`.

**Read determinism.** `readSchema11` sorts note types and decks by `AnkiID` after decoding
`col.models`/`col.decks` (`read.go:463`, `474`) precisely because Go randomises map iteration;
`collectMedia` visits indices in ascending numeric order (`media.go:78-82`) so the first-seen-wins
collision policy is stable across re-imports. Pinned by `TestRead_TwiceIsIdentical`
(`read_test.go:289`). Do not remove either sort.

**The zstd size gate.** `decompressZstd` (`read.go:303-332`) parses `Frame_Content_Size` out of the
header *before* calling `DecodeAll`, because `DecodeAll` is one uninterruptible allocation. It then
still bounds the actual output with `io.LimitReader`, so a frame declaring no size is covered too.
`zstdDeclaredSize` is unit-tested across all four FCS codes plus a dictionary id and a truncated
header (`container_test.go:86-120`). Correct as written.

**The media zstd fallback policy** (`media.go:98-119`). The three-way split —
decode succeeded → use it; `ErrBadZstdFrame` → keep raw bytes (a legitimate file coincidentally
starting with the magic number); `ErrMemberTooLarge` → drop with a warning (never store a
still-compressed frame as the blob) — is subtle and right, and each branch has its own test
(`container_test.go:213`, `234`, `269`). Leave it exactly as is.

**`pickCollectionMember`** (`read.go:336-343`) prefers `anki21b` → `anki21` → `anki2`, which is the
downgrade-stub trap from apkg-format.md, pinned by `TestRead_PrefersNewestCollectionMember`.

**The `unicase` collation registration** (`read.go:27-38`). The comment records exactly why it is
needed (the driver validates the whole schema when any schema-18 table is opened, and the collation
is referenced by a `UNIQUE INDEX`) and why the comparison semantics don't matter (nothing orders by
a collated column — `ankischema.go`'s queries sort by integer `id`/`ord` only). Correct.

**`validateSchema18Decode`** (`read.go:659-671`) fails loudly rather than importing blank cards
when a protobuf field number is wrong. This is the right posture for a package whose field numbers
are partly unverified, and `ankischema.go:126-154`'s ✅/❓ ledger is a model of how to record that.
Nothing decoded on a guessed field number. No edits.

**`ankiIntOrString`** (`ankischema.go:45-61`) exists for a real observed export shape (quoted
numeric `id`) and is pinned by `TestRead_QuotedModelID`. Marshals back as a bare number, matching
what Anki writes, so `Write`'s output round-trips.

**Import idempotency (CLAUDE.md §2.2).** `importDecks`/`importNoteTypes` dedup on
`(owner, name)`, `importNotes` on `(owner, guid)`, `importCards` upserts on `(note_id, ordinal)`
so card ids and their `user_card_state`/`review_log` survive a re-import, and
`InsertImportedReviewLog` dedups on `(user, card, anki_id)`. Four tests cover it
(`dbwrite_test.go:122`, `151`, `196`, `233`). No `anki_id` is ever used as a dedup key, per
invariant §2.2. Correct.

**`importNoteTypes`'s field-count guard** (`dbwrite.go:267-269`). Refusing to reuse a same-named
note type of a different width is right — `notes.fields` is positional, so importing into a
different-width type renders every field into the wrong slot. Pinned by
`TestImport_RejectsNoteTypeFieldCountMismatch`.

**SM-2 `factor` never reaches FSRS difficulty.** `seedCardStates` (`dbwrite.go:510-561`) writes
`Stability`/`Difficulty` only from `IrFSRSState`, never from `IrCard.Factor`, and
`TestImport_NeverWritesFactor` pins it with a deliberately absurd `factor = 9999`. This is the
single most expensive silent-wrongness path in the package (invariant §2.5) and it is guarded.

**Lock ordering.** `Import` hands `lockIDs` to `review.LockCards`, which sorts internally
(`internal/review/lock.go:69-70` → `sortedKeys`), so the map-iteration order at `dbwrite.go:86-89`
cannot produce the deadlock architecture.md §6 warns about. No edit needed there.

**Warm-start preference.** `importReviews` replays `review_log` through the scheduler rather than
trusting a snapshot, and `seedCardStates` skips any card that got reviews
(`cardHasReviews`) — exactly apkg-format.md's Import section. `TestImport_RevlogBecomesReviewLogAndReplays`
also asserts the imported rows carry `NULL stability_before`/`fsrs_version`, which is what marks
them as history the server did not itself compute.

**Media handling.** NFC normalisation before hashing (`media.go:120`), content-addressed blobs,
extension-first MIME detection with sniffing as fallback (`dbwrite.go:121-126`, with the SVG
rationale spelled out and pinned by `TestImport_MediaMimeFromExtension`). All correct.

---

## 2. architecture.md §20 — the `primaryDeckAnkiId` deviation is already resolved in code; only the register entry is stale

### 2.1 Finding

The issue asks whether the flagged deviation should be removed or re-justified. Neither: **it was
already resolved when `internal/apkg/` was built (#58), and the §20 entry simply was not updated.**

§20's own prescribed resolution (architecture.md:847-851) is:

> file `cards.deck_id` from `IrCard.deckAnkiId`, and keep `notes.deck_id` as the note's home deck
> — where it was first filed, where the notes list shows it, and the default for cards generated
> later. No migration, no reader change, one decision in the writer. `primaryDeckAnkiId` then
> either becomes that home deck or goes away.

Every clause of that is shipped:

| §20's prescription | Where it landed |
|---|---|
| `IrCard` carries each card's own home deck | `ir.go:96-97` — `IrCard.DeckAnkiID`, resolved from `odid` when `odid != 0`, else `did` (`read.go:720-725`) |
| `primaryDeckAnkiId` becomes the note's home deck | `ir.go:68-72` — renamed `IrNote.HomeDeckAnkiID`, with a comment that says in so many words "It is NOT what any card's `deck_id` comes from" |
| the writer files `cards.deck_id` from the card's own deck | `dbwrite.go:390` — `deckID, ok := deckByAnkiID[c.DeckAnkiID]`, and the same rule is restated in `internal/db/queries/import.sql`'s `UpsertImportedCard` comment (`import.sql.go:454-456`) |
| `notes.deck_id` stays the note's home deck | `dbwrite.go:296` — `deckByAnkiID[n.HomeDeckAnkiID]`, and a re-import never moves it (`UpdateImportedNote` has no `DeckID` param) |
| no migration, no schema change | none was made |

It is regression-tested, not merely implemented: `TestImport_FilesCardDeckFromCardsOwnDeck`
(`dbwrite_test.go:18-69`) imports note 101 whose two cards sit in *different* decks and asserts
card 202 lands in `Default::Sub` while the note's own `deck_id` stays `Default` — the exact
fidelity loss §20 exists to prevent. `TestImport_ReimportDoesNotMoveNotes`
(`dbwrite_test.go:196`) pins the second half. `TestImport_FilteredDeckNotCreated`
(`dbwrite_test.go:534`) pins the `odid` case.

So there is **no deviation left to remove and nothing to re-justify.** Enshu matches Anki here: a
card belongs to exactly one deck, its own. The only outstanding work is documentation.

### 2.2 Edit — rewrite architecture.md §20's third subsection

The current subsection (architecture.md:813-862, ~50 lines) is titled
`### Unforced — obsolete, and due for removal`, names TypeScript identifiers that no longer exist,
and twice states that the fix "remains #33's job". All three are now wrong.

**Do not delete the subsection.** It carries a design rule — *file `cards.deck_id` from the card's
own deck, never flattened to the note's* — that is not recoverable from git history in any useful
form and that a future importer change could silently undo. Retitle it as a resolved record and
trim it to the rule plus where it is enforced.

Replace architecture.md lines 813-862 in full with:

```markdown
### Unforced — resolved

**One note, one deck.** First, the cardinality, because it is easy to misread: **a card belongs
to exactly one deck** — `cards.did` is a single column, and so is our `cards.deck_id`. We match
Anki there and always have. Filtered decks are not an exception: they set `did` to the filtered
deck and keep the real home in `odid`, which is a temporary move with a forwarding address, not
membership in two decks.

What Anki does *not* do is require a note's cards to share a deck. Each card carries its own
`did`, which is why "Deck Override" on a card template works and why a note's reverse card can
live in its own deck. People use this.

**The deviation this row used to record is gone.** The superseded TypeScript importer's
`IrNote.primaryDeckAnkiId` (`src/lib/server/apkg/ir.ts`, §1) collapsed a note's cards to one
deck — the home deck of its lowest-numbered card — and justified it by
`UNIQUE (deck_id, guid)`, which required a note to have exactly one deck or its identity was
undefined. [#32](https://github.com/Jolls/deckshare/issues/32) replaced that key with
`UNIQUE (owner_id, guid)` (§2.2), so the constraint that forced the flattening stopped existing;
the multiuser argument is what *removed* it, not what created it.

**The Go importer never inherited it** ([#58](https://github.com/Jolls/deckshare/issues/58)). The
rule, and where it is enforced:

- `IrCard.DeckAnkiID` (`internal/apkg/ir.go`) is each card's own home deck — `odid` when
  `odid != 0`, else `did`. `internal/apkg/dbwrite.go`'s `importCards` files `cards.deck_id`
  from it, never from the note. `internal/db/queries/import.sql`'s `UpsertImportedCard` carries
  the same note.
- `IrNote.HomeDeckAnkiID` is the note's home deck — the deck of its lowest-numbered card. It
  fills `notes.deck_id` only: where the note was first filed, where the notes list shows it, and
  the default for cards generated later. A re-import never moves it.
- Guarded by `TestImport_FilesCardDeckFromCardsOwnDeck` and `TestImport_ReimportDoesNotMoveNotes`
  (`internal/apkg/dbwrite_test.go`). A change that reintroduces the flattening fails the first.

One consequence, settled in [#51](https://github.com/Jolls/deckshare/issues/51): deleting a deck
deletes the cards filed in it, and a note goes only when it has **no cards left anywhere** — so a
note whose cards span decks survives its home deck's deletion and is re-homed to the deck of its
lowest-ordinal surviving card. That is not expressible as a static FK cascade, so `cards.deck_id`
cascades while `notes.deck_id` restricts, and deck deletion runs as an ordered transaction in
`internal/db/deletion.go`. `review_log` keeps every row: its `card_id` is not a foreign key, the
same shape Anki's `revlog.cid` has, which is what lets a studied deck be deletable without any
`DELETE` path over training data (CLAUDE.md §2.5). Full policy: docs/schema.md, Deletion policy;
reasoning: `docs/plans/51-deletion-policy.md`.
```

### 2.3 No code change is required by section 2

`ir.go:68-72` and `dbwrite.go:351-353` already cite architecture.md §20 and already describe the
resolved design. Leave both comments as they are.

---

## 3. Cleanup — no behavior change

Twenty-nine edits. None changes what the reader produces, what the writer emits, or which
sentinel error any path returns. The two that touch a user-visible string are called out inline
(E1 adds detail to a malformed-package message; nothing else does).

### 3.1 `read.go`

#### E1. `corrupt()` helper — 29 sites currently discard the underlying cause

`read.go` wraps every SQL, scan, iterate and JSON-decode failure as
`fmt.Errorf("apkg: <what>: %w", ErrCorruptCollection)` — 29 occurrences, listed at
`read.go:382, 388, 393, 408, 416, 421, 425, 486, 498, 504, 531, 540, 564, 570, 579, 603, 618, 627,
650, 676, 685, 699, 707, 717, 762, 772, 780, 790, 809`. Every one of them **throws away `err`**.
A genuinely malformed package produces `apkg: reading notes: apkg: collection is missing a
required table or column` and nothing about what SQLite actually said. That is the one class of
error-handling defect CLAUDE.md §9 is aimed at: the error is checked, but the cause is gone.

Add, immediately after `closeQuietly` (i.e. after `read.go:49`):

```go
// corrupt wraps a driver, scan or decode failure as ErrCorruptCollection while keeping the cause
// in the message. errors.Is(err, ErrCorruptCollection) still matches -- the sentinel is what
// callers and tests switch on -- but a genuinely malformed package now says what actually failed
// instead of only that something did. The %w-then-%v shape matches openArchive below.
func corrupt(what string, cause error) error {
	return fmt.Errorf("apkg: %s: %w: %v", what, ErrCorruptCollection, cause)
}
```

Then rewrite all 29 sites mechanically. The `what` string is the existing message with the
`apkg: ` prefix and the trailing `: %w` removed:

| Line | Before (message text) | After |
|---|---|---|
| 382 | `apkg: listing collection tables` | `corrupt("listing collection tables", err)` |
| 388 | `apkg: reading table name` | `corrupt("reading table name", err)` |
| 393 | `apkg: iterating collection tables` | `corrupt("iterating collection tables", err)` |
| 408 | `apkg: reading col.crt` | `corrupt("reading col.crt", err)` |
| 416 | `apkg: reading col.models/decks` | `corrupt("reading col.models/decks", err)` |
| 421 | `apkg: decoding col.models` | `corrupt("decoding col.models", err)` |
| 425 | `apkg: decoding col.decks` | `corrupt("decoding col.decks", err)` |
| 486 | `apkg: reading notetypes` | `corrupt("reading notetypes", err)` |
| 498 | `apkg: scanning notetypes row` | `corrupt("scanning notetypes row", err)` |
| 504 | `apkg: iterating notetypes` | `corrupt("iterating notetypes", err)` |
| 531 | `apkg: reading fields` | `corrupt("reading fields", err)` |
| 540 | `apkg: scanning fields row` | `corrupt("scanning fields row", err)` |
| 564 | `apkg: iterating fields` | `corrupt("iterating fields", err)` |
| 570 | `apkg: reading templates` | `corrupt("reading templates", err)` |
| 579 | `apkg: scanning templates row` | `corrupt("scanning templates row", err)` |
| 603 | `apkg: iterating templates` | `corrupt("iterating templates", err)` |
| 618 | `apkg: reading decks` | `corrupt("reading decks", err)` |
| 627 | `apkg: scanning decks row` | `corrupt("scanning decks row", err)` |
| 650 | `apkg: iterating decks` | `corrupt("iterating decks", err)` |
| 676 | `apkg: reading notes` | `corrupt("reading notes", err)` |
| 685 | `apkg: scanning note row` | `corrupt("scanning note row", err)` |
| 699 | `apkg: iterating notes` | `corrupt("iterating notes", err)` |
| 707 | `apkg: reading cards` | `corrupt("reading cards", err)` |
| 717 | `apkg: scanning card row` | `corrupt("scanning card row", err)` |
| 762 | `apkg: iterating cards` | `corrupt("iterating cards", err)` |
| 772 | `apkg: probing for revlog table` | `corrupt("probing for revlog table", err)` |
| 780 | `apkg: reading revlog` | `corrupt("reading revlog", err)` |
| 790 | `apkg: scanning revlog row` | `corrupt("scanning revlog row", err)` |
| 809 | `apkg: iterating revlog` | `corrupt("iterating revlog", err)` |

`err` is in scope at every one (each is inside `x, err := …` or `if err := …; err != nil`).

Do **not** touch the five `decodeProto` wraps at `read.go:513, 549, 588, 632, 636` — those already
propagate the real cause through `%w` and carry `ErrSchema18Config`.

Sentinel identity is unchanged, so `container_test.go`'s and any caller's `errors.Is` checks are
unaffected. The only visible difference is that `/import`'s error banner
(`internal/http/import.go:47-50`) now names the underlying failure on a corrupt package.

#### E2. `memberPlain` — collapse the duplicated read-then-maybe-decompress

`Read` performs the identical five-line dance twice: once for the collection (`read.go:98-107`)
and once for the media index (`read.go:155-164`).

Add after `memberBytes` (i.e. after `read.go:224`):

```go
// memberPlain reads one member and transparently decompresses it when it is a zstd frame -- the
// modern container spells both the collection and the media index that way, the legacy one stores
// them plain (apkg-format.md's Container section). Sniffed on the magic number, never on the
// package's declared version, so a version number's meaning cannot be got wrong.
func memberPlain(f *zip.File, limits ArchiveLimits, budget *int64) ([]byte, error) {
	b, err := memberBytes(f, limits, budget)
	if err != nil {
		return nil, err
	}
	if !sniffZstd(b) {
		return b, nil
	}
	return decompressZstd(b, limits, budget)
}
```

Replace `read.go:98-107` with:

```go
	collBytes, err := memberPlain(collMember, limits, &budget)
	if err != nil {
		return nil, err
	}
```

Replace `read.go:155-164` with:

```go
		mediaBytes, err := memberPlain(mediaMember, limits, &budget)
		if err != nil {
			return nil, err
		}
```

`collectMedia` keeps its own `memberBytes` + explicit `sniffZstd` block — it must, because its
error policy differs per branch (media.go:98-119). Do not route it through `memberPlain`.

#### E3. Split `readSchema18` into four table readers

`readSchema18` (`read.go:483-654`) is 172 lines holding three copy-pasted scan loops. Because none
of them can `defer`, each repeats `closeQuietly(rows)` on all three exits — nine explicit close
calls (`read.go:497, 503, 506, 539, 548, 563, 566, 578, 587, 602, 605`). The note-type loop also
buffers every row into an intermediate `ntRow`/`ntRowsList` for no reason other than that same
close problem.

Replace `read.go:483-654` with:

```go
// readSchema18 decodes the notetypes/fields/templates/decks tables, including their protobuf
// config columns, using the field numbers in ankischema.go -- confirmed against a real export as
// of #61 for kind/css/qfmt/afmt/font/size/media/deck-kind. See ankischema.go for which
// properties are still unverified and therefore left at their zero value.
func readSchema18(dbh *sql.DB) ([]IrNoteType, []IrDeck, error) {
	noteTypes, ntByID, err := readNotetypes18(dbh)
	if err != nil {
		return nil, nil, err
	}
	if err := readFields18(dbh, noteTypes, ntByID); err != nil {
		return nil, nil, err
	}
	if err := readTemplates18(dbh, noteTypes, ntByID); err != nil {
		return nil, nil, err
	}
	for i := range noteTypes {
		sortFieldsByOrdinal(noteTypes[i].Fields)
		sortTemplatesByOrdinal(noteTypes[i].Templates)
	}
	if err := validateSchema18Decode(noteTypes); err != nil {
		return nil, nil, err
	}
	decks, err := readDecks18(dbh)
	if err != nil {
		return nil, nil, err
	}
	return noteTypes, decks, nil
}

// readNotetypes18 returns the note types in id order plus an index from notetypes.id into that
// slice, which readFields18 and readTemplates18 use to attach their rows.
func readNotetypes18(dbh *sql.DB) ([]IrNoteType, map[int64]int, error) {
	rows, err := dbh.Query(sqlSelectNotetypes18)
	if err != nil {
		return nil, nil, corrupt("reading notetypes", err)
	}
	defer closeQuietly(rows)

	var noteTypes []IrNoteType
	ntByID := map[int64]int{}
	for rows.Next() {
		var id int64
		var name string
		var config []byte
		if err := rows.Scan(&id, &name, &config); err != nil {
			return nil, nil, corrupt("scanning notetypes row", err)
		}
		fields, err := decodeProto(config)
		if err != nil {
			return nil, nil, fmt.Errorf("apkg: decoding notetypes.config for %q: %w", name, err)
		}
		kind, _ := protoUint(fields, ntConfigKindField)
		sortField, _ := protoUint(fields, ntConfigSortFieldField)
		css, _ := protoString(fields, ntConfigCSSField)
		ntByID[id] = len(noteTypes)
		noteTypes = append(noteTypes, IrNoteType{
			AnkiID:       id,
			Name:         name,
			CSS:          css,
			IsCloze:      kind == 1,
			SortFieldIdx: int32(sortField),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, corrupt("iterating notetypes", err)
	}
	return noteTypes, ntByID, nil
}

func readFields18(dbh *sql.DB, noteTypes []IrNoteType, ntByID map[int64]int) error {
	rows, err := dbh.Query(sqlSelectFields18)
	if err != nil {
		return corrupt("reading fields", err)
	}
	defer closeQuietly(rows)

	for rows.Next() {
		var ntid int64
		var ord int32
		var name string
		var config []byte
		if err := rows.Scan(&ntid, &ord, &name, &config); err != nil {
			return corrupt("scanning fields row", err)
		}
		i, ok := ntByID[ntid]
		if !ok {
			continue
		}
		fields, err := decodeProto(config)
		if err != nil {
			return fmt.Errorf("apkg: decoding fields.config for %q: %w", name, err)
		}
		font, _ := protoString(fields, fieldConfigFontField)
		size, _ := protoUint(fields, fieldConfigSizeField)
		// IsRTL/Sticky are left at their zero value: no field in a real export has either set,
		// so their protobuf field numbers are still unverified (ankischema.go, #61).
		noteTypes[i].Fields = append(noteTypes[i].Fields, IrField{
			Ordinal: ord,
			Name:    name,
			Font:    font,
			Size:    int32(size),
		})
	}
	return rowsErr(rows, "iterating fields")
}

func readTemplates18(dbh *sql.DB, noteTypes []IrNoteType, ntByID map[int64]int) error {
	rows, err := dbh.Query(sqlSelectTemplates18)
	if err != nil {
		return corrupt("reading templates", err)
	}
	defer closeQuietly(rows)

	for rows.Next() {
		var ntid int64
		var ord int32
		var name string
		var config []byte
		if err := rows.Scan(&ntid, &ord, &name, &config); err != nil {
			return corrupt("scanning templates row", err)
		}
		i, ok := ntByID[ntid]
		if !ok {
			continue
		}
		fields, err := decodeProto(config)
		if err != nil {
			return fmt.Errorf("apkg: decoding templates.config for %q: %w", name, err)
		}
		qfmt, _ := protoString(fields, tmplConfigQFmtField)
		afmt, _ := protoString(fields, tmplConfigAFmtField)
		// BrowserQfmt/BrowserAfmt are left at their zero value: no template in a real export
		// overrides them, so their protobuf field numbers are still unverified (#61).
		noteTypes[i].Templates = append(noteTypes[i].Templates, IrTemplate{
			Ordinal: ord,
			Name:    name,
			Qfmt:    qfmt,
			Afmt:    afmt,
		})
	}
	return rowsErr(rows, "iterating templates")
}

func readDecks18(dbh *sql.DB) ([]IrDeck, error) {
	rows, err := dbh.Query(sqlSelectDecks18)
	if err != nil {
		return nil, corrupt("reading decks", err)
	}
	defer closeQuietly(rows)

	var decks []IrDeck
	for rows.Next() {
		var id int64
		var name string
		var common, kind []byte
		if err := rows.Scan(&id, &name, &common, &kind); err != nil {
			return nil, corrupt("scanning decks row", err)
		}
		// decks.common's field numbers (e.g. description) are still unverified (#61): no deck in
		// a real export sets one. Decoded only to reject a corrupt blob; Description stays "".
		if _, err := decodeProto(common); err != nil {
			return nil, fmt.Errorf("apkg: decoding decks.common for %q: %w", name, err)
		}
		kindFields, err := decodeProto(kind)
		if err != nil {
			return nil, fmt.Errorf("apkg: decoding decks.kind for %q: %w", name, err)
		}
		// decks.kind is a oneof: field 1 (deckKindNormalField) wraps a non-filtered deck's
		// config, confirmed against a real export (#61). The filtered variant's own field number
		// is still unverified, but Anki decks are one or the other, so "the Normal variant is
		// absent" correctly identifies a filtered deck without needing that number.
		_, isNormal := protoMessage(kindFields, deckKindNormalField)
		decks = append(decks, IrDeck{
			AnkiID:     id,
			Name:       normaliseDeckName(name),
			IsFiltered: !isNormal,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, corrupt("iterating decks", err)
	}
	return decks, nil
}
```

and add next to `corrupt`:

```go
// rowsErr is the tail every scan loop ends with: report an iteration failure as a corrupt
// collection, or nil.
func rowsErr(rows *sql.Rows, what string) error {
	if err := rows.Err(); err != nil {
		return corrupt(what, err)
	}
	return nil
}
```

Notes for the implementer:

- Decoding the protobuf blob inside the loop instead of buffering rows first is safe:
  `database/sql`'s `Rows.Scan` into a `*[]byte` copies (only `sql.RawBytes` aliases), and
  `protoString`/`protoUint` copy out of the decoded fields, so nothing survives the iteration by
  reference.
- Behaviour is identical, including error precedence: fields are still attached before templates,
  sorting and `validateSchema18Decode` still run before the decks query, and a note type with no
  matching `ntid` is still silently skipped.
- Net effect: `readSchema18` 172 → ~25 lines, four focused readers, nine explicit `closeQuietly`
  calls become four `defer`s, and the `ntRow` struct and `ntRowsList` slice disappear.
- `readDecks18` uses `rows.Err()` inline rather than `rowsErr` only because it returns two values;
  keep it that way rather than adding a second helper.

#### E4. `closeQuietly` takes `io.Closer`

`read.go:45`:

```go
func closeQuietly(c interface{ Close() error }) {
```

becomes

```go
func closeQuietly(c io.Closer) {
```

`io` is already imported by `read.go`. Every current call site satisfies `io.Closer`
(`*os.File`, `io.ReadCloser`, `*sql.DB`, `*sql.Rows`, `*zstd.Encoder`). Leave the body
(`if err := c.Close(); err != nil { return }`) alone — see §4.4.

#### E5. Fix `ArchiveLimits`'s doc comment

`read.go:51-52` claims something that is not true:

```go
// ArchiveLimits bounds an untrusted package (architecture.md §8: a shared deck is other users'
// bytes). Zero fields are rejected -- use DefaultArchiveLimits and adjust.
```

Nothing validates the struct. A zero-valued `ArchiveLimits` fails with `ErrTooManyMembers` on any
archive with at least one member, which is neither a rejection of the limits nor an informative
error. Replace the second sentence:

```go
// ArchiveLimits bounds an untrusted package (architecture.md §8: a shared deck is other users'
// bytes). The values are not validated: a zero-valued ArchiveLimits rejects every package, since
// MaxMembers 0 fails any archive with a member. Start from DefaultArchiveLimits and adjust.
```

#### E6. `zstdDeclaredSize` uses `encoding/binary` for its little-endian reads

`read.go:291-297`:

```go
	var v uint64
	for i := 0; i < fcsSize; i++ {
		v |= uint64(b[off+i]) << (8 * i)
	}
	if fcsSize == 2 {
		v += 256
	}
	return int64(v), true, nil
```

becomes

```go
	var v uint64
	switch fcsSize {
	case 1:
		v = uint64(b[off])
	case 2:
		v = uint64(binary.LittleEndian.Uint16(b[off:])) + 256 // the 2-byte field is offset by 256
	case 4:
		v = uint64(binary.LittleEndian.Uint32(b[off:]))
	case 8:
		v = binary.LittleEndian.Uint64(b[off:])
	}
	return int64(v), true, nil
```

Add `"encoding/binary"` to `read.go`'s imports. The `len(b) < off+fcsSize` guard at `read.go:288`
already makes every slice above in-bounds. `container_test.go:86-120` covers all four widths.

### 3.2 `protobuf.go`

#### E7. `protoLast` dedups three identical last-occurrence loops

`protoString` (`protobuf.go:104-114`), `protoUint` (`117-127`) and `protoMessage` (`130-147`) each
open-code the same scan. Add above them:

```go
// protoLast returns the last occurrence of field number n with wire type wt. Last, not first:
// protobuf's own rule for a repeated scalar is that the final value wins.
func protoLast(fields []protoField, n uint32, wt protoWireType) (protoField, bool) {
	var out protoField
	var found bool
	for _, f := range fields {
		if f.Number == n && f.Type == wt {
			out, found = f, true
		}
	}
	return out, found
}
```

Then replace the three bodies:

```go
// protoString returns the last occurrence of field number n as a string, and whether it was
// present with wire type 2.
func protoString(fields []protoField, n uint32) (string, bool) {
	f, ok := protoLast(fields, n, protoBytes)
	return string(f.Bytes), ok
}

// protoUint returns the last varint occurrence of field number n.
func protoUint(fields []protoField, n uint32) (uint64, bool) {
	f, ok := protoLast(fields, n, protoVarint)
	return f.Varint, ok
}

// protoMessage returns the last length-delimited occurrence of n, decoded as a nested message.
func protoMessage(fields []protoField, n uint32) ([]protoField, bool) {
	f, ok := protoLast(fields, n, protoBytes)
	if !ok {
		return nil, false
	}
	nested, err := decodeProto(f.Bytes)
	if err != nil {
		return nil, false
	}
	return nested, true
}
```

`string(nil)` is `""`, so `protoString`'s not-found return is byte-for-byte what it is today.
44 lines → 30, and one loop instead of three.

#### E8. `decodeProto` uses `encoding/binary` for fixed64/fixed32

`protobuf.go:55-58`:

```go
			var v uint64
			for i := 0; i < 8; i++ {
				v |= uint64(b[i]) << (8 * i)
			}
```

becomes `v := binary.LittleEndian.Uint64(b[:8])`, and `protobuf.go:76-79`:

```go
			var v uint64
			for i := 0; i < 4; i++ {
				v |= uint64(b[i]) << (8 * i)
			}
```

becomes `v := uint64(binary.LittleEndian.Uint32(b[:4]))`. The `len(b) < 8` / `len(b) < 4` guards
immediately above already make both in-bounds. Add `"encoding/binary"` to the imports.

Leave `decodeVarint` alone — see Open question 2.

### 3.3 `ir.go`

#### E9. `splitTags` is one line of `strings.Fields`

`ir.go:155-161`:

```go
func splitTags(tags string) []string {
	fields := strings.Fields(tags)
	out := make([]string, 0, len(fields))
	out = append(out, fields...)
	return out
}
```

becomes:

```go
// splitTags splits notes.tags, which is space-separated AND space-surrounded, dropping empties.
// strings.Fields never returns nil -- it allocates with make even for an empty result -- which
// matters: an IrNote with no tags must compare equal to a spec built the same way
// (synthetic_test.go's expectedIR).
func splitTags(tags string) []string {
	return strings.Fields(tags)
}
```

Verified against the Go distribution's `strings.Fields`: the ASCII path returns
`make([]string, n)` and the non-ASCII path delegates to `FieldsFunc`, which returns
`make([]string, len(spans))` — both non-nil at length zero, and both length-equals-capacity,
exactly matching what the current copy produces. Covered by `TestRead_TagsAndFieldSplitting`,
`TestEncodeTags_InverseOfSplitTags`, and the `reflect.DeepEqual` comparison in
`TestRead_Schema11_MatchesSpec` (note 102 has no tags).

#### E10. `secondsPerDay` const replaces eight literal `86400`s

Add above `intervalSeconds` (`ir.go:140`):

```go
// secondsPerDay is Anki's day length wherever a column encodes an interval as a whole number of
// days -- cards.ivl, revlog.ivl, revlog.lastIvl, and our own scheduled_days on the way in and out.
const secondsPerDay = 86400
```

Replace the literal at: `ir.go:147`, `write.go:257`, `write.go:258`, `dbwrite.go:467`,
`dbwrite.go:549`, `dbexport.go:158`, `dbexport.go:221`. Leave the `86400` inside `ir.go:103`'s
comment (it is prose about the wire format). Leave `write_test.go:54`'s test data alone.

The const is untyped, so every existing conversion (`int64(x) * secondsPerDay`,
`int32(s / secondsPerDay)`, `s % secondsPerDay`) compiles unchanged.

#### E11. `resolveHomeDecks` indexes instead of copying every card

`ir.go:206-213` builds `map[int64]IrCard`, copying a whole `IrCard` (fifteen fields, an `IrDue`
and a pointer) per card. Replace `ir.go:207-213` with:

```go
	lowestCard := make(map[int64]int, len(notes))
	for i, c := range cards {
		j, ok := lowestCard[c.NoteAnkiID]
		if !ok || c.AnkiID < cards[j].AnkiID {
			lowestCard[c.NoteAnkiID] = i
		}
	}
```

and `ir.go:216-222` with:

```go
	for i := range notes {
		j, ok := lowestCard[notes[i].AnkiID]
		if !ok {
			warnings = append(warnings, "note "+guidOrAnki(notes[i])+" has no cards; skipped")
			continue
		}
		notes[i].HomeDeckAnkiID = cards[j].DeckAnkiID
	}
```

Same results, same warning text, same order. (The warning's wording is Open question 1 — do not
change it here.)

### 3.4 `write.go`

#### E12. One SQLite transaction for the whole collection

`writeCollection` (`write.go:119-166`) issues one `dbh.Exec` per note, per card and per revlog row.
Under `database/sql` each of those is its own implicit SQLite transaction, so a real collection
costs one commit — and one fsync — per row.

In `writeCollection`, after `encodeModelsAndDecks` succeeds (i.e. after `write.go:131`), insert:

```go
	// One transaction for every row below: SQLite otherwise commits -- and fsyncs -- once per
	// Exec, which on a real collection is one commit per note, card and revlog row.
	tx, err := dbh.Begin()
	if err != nil {
		return fmt.Errorf("apkg: beginning collection transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
```

Change every `dbh.Exec(` at `write.go:132`, `138`, `150`, `158` to `tx.Exec(`. Leave the DDL
`dbh.Exec(ddl)` at `write.go:124` outside the transaction. Replace `write.go:165` (`return nil`)
with:

```go
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apkg: committing collection: %w", err)
	}
	return nil
```

`_ = tx.Rollback()` after a successful commit returns `sql.ErrTxDone` and is the repo's existing
idiom for this (`internal/http/import.go:60`).

Behaviour is identical — the file bytes `buildCollection` reads back are the same, because
`dbh.Close()` (`write.go:108`) already happens after all writes.

#### E13. Media-index encoding moves next to its decoder

For read/write symmetry (the issue's second review-focus bullet), the media index should be
encoded where it is decoded. Add to `media.go`, immediately after `readMediaIndex`:

```go
// encodeMediaIndex is readMediaIndex's inverse for the legacy container's JSON spelling, which is
// the only one Write emits (write.go: schema 11, legacy zip, no meta member). The protobuf
// spelling is read-only.
func encodeMediaIndex(files []IrMedia) ([]byte, error) {
	idx := make(map[string]string, len(files))
	for _, m := range files {
		idx[m.Index] = m.Filename
	}
	b, err := json.Marshal(idx)
	if err != nil {
		return nil, fmt.Errorf("apkg: marshalling media index: %w", err)
	}
	return b, nil
}
```

Replace `write.go:38-45` with:

```go
	mediaJSON, err := encodeMediaIndex(col.Media)
	if err != nil {
		return err
	}
```

`write.go` then no longer needs `"encoding/json"` for this, but still does for `encodeCardData`
(`write.go:225`) — keep the import.

### 3.5 `dbwrite.go`

#### E14. `maxInt32` → the `max` builtin

Delete `maxInt32` (`dbwrite.go:501-506`) and replace both call sites:

- `dbwrite.go:467`: `ScheduledDaysAfter: maxInt32(0, int32(r.IntervalSeconds/secondsPerDay)),`
  → `ScheduledDaysAfter: max(0, int32(r.IntervalSeconds/secondsPerDay)),`
- `dbwrite.go:549`: `ScheduledDays: maxInt32(0, int32(c.IntervalSeconds/secondsPerDay)),`
  → `ScheduledDays: max(0, int32(c.IntervalSeconds/secondsPerDay)),`

Go 1.26 (`go.mod:3`), and the repo already uses the builtin at `internal/fsrs/params.go:79`.

#### E15. `reviewState*` constants are typed and moved above their user

`dbwrite.go:433-437` declares the constants *after* the only function that uses them, forcing
`int16(...)` conversions at `dbwrite.go:425` and `427`. Move the block above `reviewKindToState`
(i.e. before `dbwrite.go:419`) and type it:

```go
// review_log.state_before values, matching go-fsrs's State enum (docs/schema.md).
const (
	reviewStateLearning   int16 = 1
	reviewStateReview     int16 = 2
	reviewStateRelearning int16 = 3
)
```

and drop the conversions in the switch body:

```go
	case 0:
		return reviewStateLearning
	case 2:
		return reviewStateRelearning
	default: // 1 (review), 3 (cram)
		return reviewStateReview
```

#### E16. `importedNoteType` replaces three parallel maps

`importNoteTypes` (`dbwrite.go:203`) returns three maps keyed identically plus an error — a
four-value return that four `return nil, nil, nil, err` statements have to spell out (lines 222,
234, 249, 260, 265, 268, 272).

Add above `importNoteTypes`:

```go
// importedNoteType is what importNoteTypes resolved for one of the package's note types: the row
// it created or reused, that row's templates keyed by ordinal, and whether it is a cloze type --
// which files every card under template ordinal 0 whatever the card's own ordinal says.
type importedNoteType struct {
	ID        pgtype.UUID
	Templates map[int32]pgtype.UUID
	IsCloze   bool
}
```

Change the signature to:

```go
func importNoteTypes(ctx context.Context, q *db.Queries, ownerID pgtype.UUID, noteTypes []IrNoteType, result *ImportResult) (map[int64]importedNoteType, error) {
```

Body changes:
- `dbwrite.go:204-206` → `noteTypeByAnkiID := make(map[int64]importedNoteType, len(noteTypes))`
  (delete `templatesByNoteType` and `isClozeByNoteType`).
- all seven `return nil, nil, nil, fmt.Errorf(…)` → `return nil, fmt.Errorf(…)`.
- `dbwrite.go:253-255` →
  `noteTypeByAnkiID[nt.AnkiID] = importedNoteType{ID: created.ID, Templates: templates, IsCloze: nt.IsCloze}`
- `dbwrite.go:278-280` →
  `noteTypeByAnkiID[nt.AnkiID] = importedNoteType{ID: existing.ID, Templates: templates, IsCloze: existing.IsCloze}`
- `dbwrite.go:283` → `return noteTypeByAnkiID, nil`

Caller `importNotes` (`dbwrite.go:288`): change the parameter type to
`noteTypeByAnkiID map[int64]importedNoteType` and `dbwrite.go:291-295` to:

```go
		nt, ok := noteTypeByAnkiID[n.NoteTypeAnkiID]
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("note %q: note type did not resolve; skipped", n.Guid))
			continue
		}
```

then `NoteTypeID: nt.ID` at `dbwrite.go:313` and `337`.

Caller `Import` (`dbwrite.go:62`) becomes:

```go
	noteTypeByAnkiID, err := importNoteTypes(ctx, q, ownerID, col.NoteTypes, &result)
	if err != nil {
		return ImportResult{}, err
	}
```

#### E17. `importCards` — drop a dead parameter, take `col`, return a struct

`importCards` (`dbwrite.go:354`) takes **ten** parameters, one of which — `noteTypeByAnkiID` — is
never read in the body. Verify before editing: the body uses `noteByAnkiID`,
`noteTypeAnkiIDByNoteAnkiID`, `templatesByNoteType`, `isClozeByNoteType`, `deckByAnkiID`,
`homeDeckByNoteAnkiID`, `cards`, `notes`, `result` — and not `noteTypeByAnkiID`. It is an unused
parameter Go does not flag.

Add above `importCards`:

```go
// importedCards is what importCards resolved: the database id and the deck of every card it
// wrote, keyed by the card's Anki id, plus the per-deck tally ImportResult.Decks reports.
type importedCards struct {
	IDByAnkiID   map[int64]pgtype.UUID
	DeckByAnkiID map[int64]pgtype.UUID
	CountByDeck  map[pgtype.UUID]int
}
```

New signature (ten parameters → seven, two return values → two):

```go
func importCards(ctx context.Context, q *db.Queries, col *IrCollection, noteByAnkiID map[int64]pgtype.UUID, noteTypeByAnkiID map[int64]importedNoteType, deckByAnkiID map[int64]pgtype.UUID, result *ImportResult) (importedCards, error) {
```

Replace `dbwrite.go:355-363` with:

```go
	noteTypeOf := make(map[int64]importedNoteType, len(col.Notes))
	homeDeckOf := make(map[int64]int64, len(col.Notes))
	for _, n := range col.Notes {
		noteTypeOf[n.AnkiID] = noteTypeByAnkiID[n.NoteTypeAnkiID]
		homeDeckOf[n.AnkiID] = n.HomeDeckAnkiID
	}

	out := importedCards{
		IDByAnkiID:   make(map[int64]pgtype.UUID, len(col.Cards)),
		DeckByAnkiID: make(map[int64]pgtype.UUID, len(col.Cards)),
		CountByDeck:  make(map[pgtype.UUID]int, len(deckByAnkiID)),
	}
	for _, c := range col.Cards {
```

Inside the loop:
- `dbwrite.go:370-371` (`noteTypeAnkiID := …; templates := templatesByNoteType[…]`) →
  `nt := noteTypeOf[c.NoteAnkiID]`
- `dbwrite.go:374` → `if nt.IsCloze {`
- `dbwrite.go:375` → `id, ok := nt.Templates[0]`
- `dbwrite.go:382` → `id, ok := nt.Templates[c.Ordinal]`
- `dbwrite.go:394` → `deckID, ok = deckByAnkiID[homeDeckOf[c.NoteAnkiID]]`
- `dbwrite.go:410` → `return importedCards{}, fmt.Errorf("apkg: upserting card (anki_id %d): %w", c.AnkiID, err)`
- `dbwrite.go:412-414` →
  ```go
		out.IDByAnkiID[c.AnkiID] = created.ID
		out.DeckByAnkiID[c.AnkiID] = deckID
		out.CountByDeck[deckID]++
		result.CardsUpserted++
  ```
- every other `return nil, nil, …` in the function → `return importedCards{}, …`
- `dbwrite.go:416` → `return out, nil`

Equivalence: a zero `importedNoteType` from `noteTypeOf` has a nil `Templates` map, and a lookup in
a nil map returns `(zero, false)` — the same not-found path today's nil `templatesByNoteType[…]`
takes, producing the identical "no template at ordinal N; skipped" warning. `out.DeckByAnkiID` is
written in the same statement as `out.IDByAnkiID`, so the two key sets are always equal.

Caller `Import` (`dbwrite.go:72`):

```go
	cards, err := importCards(ctx, q, col, noteByAnkiID, noteTypeByAnkiID, deckByAnkiID, &result)
	if err != nil {
		return ImportResult{}, err
	}
```

#### E18. `Import` builds `result.Decks` and `deckIDs` in one deterministic pass

Two problems in `dbwrite.go:77-84` and `105-108`. First, both loops range the same
`deckByAnkiID` map, and the second does not dedup, so a package where two Anki deck ids resolve to
one Enshu deck issues duplicate `CreateMediaRef` calls. Second, map iteration is randomised
per process, so `result.Decks`'s order — and with it `/import`'s redirect target on a card-count
tie (`internal/http/import.go:79-91`) — varies run to run for the same file.

Replace `dbwrite.go:77-84` with:

```go
	// Ordered by the package's own deck list, not by map iteration: ImportResult.Decks decides
	// which deck /import redirects to (internal/http/import.go), and randomised order makes that
	// redirect vary run to run whenever two decks tie on card count. Deduped because two Anki
	// deck ids can resolve to one Enshu deck, and importMedia refs each deck once.
	seenDeck := make(map[pgtype.UUID]bool, len(deckByAnkiID))
	deckIDs := make([]pgtype.UUID, 0, len(deckByAnkiID))
	for _, d := range col.Decks {
		id, ok := deckByAnkiID[d.AnkiID]
		if !ok || seenDeck[id] {
			continue
		}
		seenDeck[id] = true
		deckIDs = append(deckIDs, id)
		result.Decks = append(result.Decks, ImportedDeck{ID: id, CardCount: cards.CountByDeck[id]})
	}
```

Delete `dbwrite.go:105-108` (the second `deckIDs` loop) entirely; `importMedia`'s call at
`dbwrite.go:109` now uses the `deckIDs` built above.

Update `dbwrite.go:86-89` to read from the new struct:

```go
	lockIDs := make([]pgtype.UUID, 0, len(cards.IDByAnkiID))
	for _, id := range cards.IDByAnkiID {
		lockIDs = append(lockIDs, id)
	}
```

(Order here is genuinely irrelevant — `review.LockCards` sorts. Leave it as a map range.)

`dbwrite.go:96` and `101` become `importReviews(ctx, tx, q, ownerID, col.Reviews, cards, &result)`
and `seedCardStates(ctx, q, ownerID, col.Cards, cards.IDByAnkiID, cardHasReviews, now, &result)`.

#### E19. `importReviews` — deterministic order, no per-card `GetCard`, params resolved once per deck

Three problems in `importReviews` (`dbwrite.go:442-492`):

1. It ranges `byCard`, a map, so its warnings land in `ImportResult.Warnings` in a different order
   on every run of the same package.
2. It calls `q.GetCard(ctx, cardID)` once per reviewed card purely to learn the card's deck — a
   deck `importCards` already knows. `UpsertImportedCard` is
   `ON CONFLICT … DO UPDATE SET deck_id = EXCLUDED.deck_id … RETURNING … deck_id`
   (`internal/db/import.sql.go:438-444`), so the value `GetCard` returns is by construction the
   `deckID` `importCards` passed in. The query is redundant.
3. It calls `review.EffectiveParams` once per reviewed card, though the answer only varies by
   `(user, deck)`.

Replace `dbwrite.go:442-492` with:

```go
// importReviews inserts each review_log row and replays the affected card's history through the
// scheduler -- apkg-format.md's preferred warm-start over seeding from a snapshot. Returns which
// cards received at least one review, so seedCardStates knows which ones to skip.
func importReviews(ctx context.Context, tx pgx.Tx, q *db.Queries, ownerID pgtype.UUID, reviews []IrReview, cards importedCards, result *ImportResult) (map[int64]bool, error) {
	// Grouped in first-appearance order rather than iterated as a map: ImportResult.Warnings is
	// shown to the importing user, and map iteration is randomised per process, so the same
	// package would otherwise report its warnings in a different order every run.
	order := make([]int64, 0, len(reviews))
	byCard := make(map[int64][]IrReview, len(reviews))
	for _, r := range reviews {
		if _, seen := byCard[r.CardAnkiID]; !seen {
			order = append(order, r.CardAnkiID)
		}
		byCard[r.CardAnkiID] = append(byCard[r.CardAnkiID], r)
	}

	cardHasReviews := make(map[int64]bool, len(byCard))
	// EffectiveParams is a per-(user, deck) lookup, so resolve it once per deck instead of once
	// per reviewed card -- a real collection has thousands of the latter and a handful of decks.
	paramsByDeck := make(map[pgtype.UUID]fsrs.Params)

	for _, cardAnkiID := range order {
		cardID, ok := cards.IDByAnkiID[cardAnkiID]
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("review log for card (anki_id %d): card did not resolve; skipped", cardAnkiID))
			continue
		}

		for _, r := range byCard[cardAnkiID] {
			n, err := q.InsertImportedReviewLog(ctx, db.InsertImportedReviewLogParams{
				UserID:              ownerID,
				CardID:              cardID,
				Rating:              r.Rating,
				ReviewedAt:          pgtype.Timestamptz{Time: r.ReviewedAt, Valid: true},
				DurationMs:          durationMsParam(r.DurationMs),
				StateBefore:         reviewKindToState(r.Kind),
				LearningStepsBefore: 0,
				ElapsedDaysBefore:   0,
				ScheduledDaysAfter:  max(0, int32(r.IntervalSeconds/secondsPerDay)),
				ReviewKind:          r.Kind,
				AnkiID:              pgtype.Int8{Int64: r.AnkiID, Valid: true},
			})
			if err != nil {
				return nil, fmt.Errorf("apkg: inserting review log row (anki_id %d): %w", r.AnkiID, err)
			}
			result.ReviewsInserted += int(n)
		}

		// The card's deck is what importCards just filed it under: UpsertImportedCard's
		// ON CONFLICT sets deck_id = EXCLUDED.deck_id, so re-reading the row cannot disagree.
		deckID := cards.DeckByAnkiID[cardAnkiID]
		params, cached := paramsByDeck[deckID]
		if !cached {
			p, err := review.EffectiveParams(ctx, q, ownerID, deckID)
			if err != nil {
				return nil, fmt.Errorf("apkg: resolving fsrs params for card (anki_id %d): %w", cardAnkiID, err)
			}
			paramsByDeck[deckID] = p
			params = p
		}
		if _, err := review.ReplayCard(ctx, tx, params, ownerID, cardID); err != nil {
			return nil, fmt.Errorf("apkg: replaying review history for card (anki_id %d): %w", cardAnkiID, err)
		}
		result.CardStatesReplayed++
		cardHasReviews[cardAnkiID] = true
	}
	return cardHasReviews, nil
}
```

Add `"github.com/Jolls/enshu/internal/fsrs"` to `dbwrite.go`'s imports (grouped with the other
`internal/` imports).

`pgtype.UUID` is comparable (a `[16]byte` plus a `bool`), so it is a valid map key.

The removed `"apkg: reading deck of card (anki_id %d)"` error path goes with the query; no caller
matches on it.

#### E20. Delete the now-orphaned unscoped `GetCard` query

E19 removes the only caller of `db.Queries.GetCard` anywhere in the repo (verified: the sole
reference was `internal/apkg/dbwrite.go:477`).

`GetCard` is `SELECT * FROM cards WHERE id = $1` — the same unscoped
`SELECT * WHERE id = $1` shape as the five getters #122 deleted, and for the same reason
(CLAUDE.md §9: authorisation is explicit at the query layer; these read deck-owned content with no
`deck_access` join). It survived that cleanup only because it had a caller. It no longer does, and
CLAUDE.md working rule 3 requires removing what our change orphaned.

1. Delete the stanza at `internal/db/queries/cards.sql:1-2`:
   ```sql
   -- name: GetCard :one
   SELECT * FROM cards WHERE id = $1;
   ```
   Keep the comment block that follows (`cards.sql:4-5` onward) — it documents the four
   card-regeneration statements, not `GetCard`.
2. Run `go generate ./...` and commit the regenerated `internal/db/cards.sql.go` (CLAUDE.md §16 —
   never hand-edit generated files).
3. Confirm `grep -rn "GetCard(" --include=*.go .` returns only `GetCardsForNote`-style names
   afterwards.

If the `sqlc` toolchain is not available in the implementing session, drop E20 and keep E19; E19
does not depend on it.

### 3.6 `dbexport.go`

#### E21. Name the hardcoded `2500`

`dbexport.go:140` writes `Factor: 2500` as a bare literal. Add to `ankischema.go`, next to
`ankiFlagMask` (after `ankischema.go:111`):

```go
// ankiDefaultFactor is the SM-2 ease Anki assigns a fresh card (2.5 x 1000). Export writes it on
// every card unconditionally: Enshu stores no SM-2 ease, and cards.factor is a column any reader
// of our output will parse. It is never derived from, and never maps back to, FSRS difficulty
// (apkg-format.md, and dbwrite_test.go's TestImport_NeverWritesFactor in the other direction).
const ankiDefaultFactor = 2500
```

and change `dbexport.go:140` to `IntervalSeconds: sched.IntervalSeconds, Factor: ankiDefaultFactor,`.

### 3.7 `doc.go`

#### E22. Record the IR boundary the audit verified

Append to `doc.go`, before the closing `package apkg` line:

```go
// Boundary: only dbwrite.go (IR -> db) and dbexport.go (db -> IR) may name internal/db types.
// read.go, write.go, media.go, protobuf.go, ankischema.go, ir.go and errors.go import no database
// package at all, which is what keeps a sqlc-generated row from leaking into the format layer.
```

### 3.8 Tests

None of these changes what any test asserts.

#### E23. `container_test.go` — hand-rolled integer formatting

Delete `itoaTest` (`container_test.go:301-313`) and change `container_test.go:34`:

```go
		members[string(rune('a'))+itoaTest(i)] = []byte("x")
```

to

```go
		members["a"+strconv.Itoa(i)] = []byte("x")
```

Add `"strconv"` to the imports. (`string(rune('a'))` was a roundabout spelling of `"a"`.)

#### E24. `synthetic_test.go` — `normaliseDeckNameToSchema18` is one `strings.ReplaceAll`

Replace `synthetic_test.go:484-495` with:

```go
// normaliseDeckNameToSchema18 is normaliseDeckName run in reverse: schema 18 separates the deck
// hierarchy with \x1f, schema 11 with "::".
func normaliseDeckNameToSchema18(name string) string {
	return strings.ReplaceAll(name, "::", "\x1f")
}
```

Add `"strings"` to the imports.

#### E25. `synthetic_test.go` — `appendVarint` is `binary.AppendUvarint`

Delete `appendVarint` (`synthetic_test.go:812-818`) and replace its three call sites
(`synthetic_test.go:798, 799, 806, 807` and `container_test.go:152, 153`) with
`binary.AppendUvarint`. `synthetic_test.go` already imports `"encoding/binary"`;
`container_test.go` needs it added.

#### E26. `synthetic_test.go` — `insertNotesCardsRevlog` reuses the production encoders

`synthetic_test.go:304-318` hand-builds the `\x1f`-joined `flds` string and the space-surrounded
`tags` string. Replace both with the functions that already exist and are already inverse-tested
(`TestEncodeTags_InverseOfSplitTags`):

```go
	for _, n := range spec.Notes {
		if _, err := dbh.Exec("INSERT INTO notes (id, guid, mid, mod, tags, flds, csum) VALUES (?,?,?,?,?,?,?)",
			n.AnkiID, n.Guid, n.NoteTypeAnkiID, n.Mod, encodeTags(n.Tags), strings.Join(n.Fields, "\x1f"), n.Csum); err != nil {
			return err
		}
	}
```

Equivalence check: for `n.Tags = nil` the old loop produces `""`; `encodeTags(nil)` also returns
`""` (`write.go:212-214`). For a non-empty list the old loop produces `" a b "`; `encodeTags`
produces `" " + "a b" + " "`. Identical.

#### E27. `synthetic_test.go` — `unzipMembers` collapses four copies of the unpack loop

The "open the zip, read every member into a map" loop appears four times:
`buildZstdPackage` (512-527), `buildDowngradeStubPackage` twice (543-556, 571-584), and
`buildOversizePackage` (708-720).

Add next to `zipMembers`:

```go
// unzipMembers is zipMembers in reverse: every member of pkg, read into a name -> bytes map.
func unzipMembers(t *testing.T, pkg []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatalf("opening package: %v", err)
	}
	members := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening member %q: %v", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("reading member %q: %v", f.Name, err)
		}
		closeQuietly(rc)
		members[f.Name] = buf.Bytes()
	}
	return members
}
```

Then:

`buildZstdPackage` body (`synthetic_test.go:500-528`) becomes:

```go
	enc, err := zstd.NewWriter(nil, zstd.WithSingleSegment(true))
	if err != nil {
		t.Fatalf("creating zstd encoder: %v", err)
	}
	defer closeQuietly(enc)

	members := unzipMembers(t, pkg)
	for name, data := range members {
		if name == "collection.anki21" || name == "media" {
			members[name] = enc.EncodeAll(data, nil)
		}
	}
	return zipMembers(t, members)
```

(Assigning to existing keys while ranging a map is defined behaviour in Go; no key is added or
deleted.)

`buildDowngradeStubPackage` body (`synthetic_test.go:536-589`) becomes:

```go
	realColl := unzipMembers(t, buildSchema11Package(t, defaultSynthSpec(t)))["collection.anki21"]

	stubSpec := synthSpec{
		Crt:       defaultSynthSpec(t).Crt,
		NoteTypes: []synthNoteType{{AnkiID: 9001, Name: "Stub", Fields: []IrField{{Ordinal: 0, Name: "Front"}}, Templates: []IrTemplate{{Ordinal: 0, Name: "Card 1", Qfmt: "{{Front}}", Afmt: "{{Front}}"}}}},
		Decks:     []synthDeck{{AnkiID: 1, Name: "Default"}},
		Notes:     []synthNote{{AnkiID: 901, Guid: "stub-guid", NoteTypeAnkiID: 9001, Fields: []string{"please upgrade"}}},
		Cards:     []synthCard{{AnkiID: 902, NoteAnkiID: 901, Did: 1, Ord: 0, Type: ankiTypeNew, Queue: ankiQueueNew, Due: 1}},
	}
	stubColl := unzipMembers(t, buildSchema11Package(t, stubSpec))["collection.anki21"]

	return zipMembers(t, map[string][]byte{
		"collection.anki21": realColl,
		"collection.anki2":  stubColl,
	})
```

`buildOversizePackage` — see E28.

#### E28. `synthetic_test.go` — `buildOversizePackage`'s `declaredOnly` parameter is always `true`

Its only caller is `container_test.go:78`, `buildOversizePackage(t, true)`. The `!declaredOnly`
branch is unreachable. Drop the parameter, and with it half the doc comment:

```go
// buildOversizePackage returns a package whose "media" INDEX member (distinct from the numbered
// media file members, which are also zstd-sniffed but tolerate a non-zstd payload -- media.go) is
// a zstd frame declaring a Frame_Content_Size far past any reasonable limit while the actual
// payload is tiny, exercising decompressZstd's before-decompression gate specifically.
func buildOversizePackage(t *testing.T) []byte {
	t.Helper()
	members := unzipMembers(t, buildSchema11Package(t, defaultSynthSpec(t)))

	enc, err := zstd.NewWriter(nil, zstd.WithSingleSegment(true))
	if err != nil {
		t.Fatalf("creating zstd encoder: %v", err)
	}
	defer closeQuietly(enc)

	members["media"] = patchZstdDeclaredSize(t, enc.EncodeAll([]byte(`{}`), nil), 10<<30) // 10 GiB, dwarfs any test limit

	return zipMembers(t, members)
}
```

Update `container_test.go:78` to `buildOversizePackage(t)`.

#### E29. `read_test.go` — two identical hand-written insertion sorts

Replace `read_test.go:336-354` with:

```go
func sortedNoteTypes(nts []IrNoteType) []IrNoteType {
	out := append([]IrNoteType(nil), nts...)
	sort.Slice(out, func(i, j int) bool { return out[i].AnkiID < out[j].AnkiID })
	return out
}

func sortedDecks(ds []IrDeck) []IrDeck {
	out := append([]IrDeck(nil), ds...)
	sort.Slice(out, func(i, j int) bool { return out[i].AnkiID < out[j].AnkiID })
	return out
}
```

Add `"sort"` to `read_test.go`'s imports. `sort.Slice` rather than `slices.SortFunc` because
`sort.Slice` is what the rest of the repo uses (`internal/render/cloze.go:118`,
`internal/review/grade.go:225`, `internal/review/lock.go:51`, `internal/apkg/ir.go:235`).

---

## 4. Considered and rejected

**4.1 Deleting `WriteFile` (`write.go:17-25`).** It has no caller anywhere — not even a test.
Keep it: eight lines, it is `ReadFile`'s exact mirror, and it is the entry point the
`.apkg` export route (Milestone 2, docs/routes.md "Import / export") will call. Removing half of a
symmetric pair to satisfy a dead-code count is the wrong trade in the package whose review focus is
import/export symmetry.

**4.2 Deleting the IR fields with no database consumer.** `IrCard.FilteredDeckAnkiID`,
`IrCard.Buried`, `IrFSRSState.Position`, `IrFSRSState.DesiredRetention`, `IrReview.Factor` and
`IrReview.LastIntervalSeconds` are all populated by the reader and never written to Postgres. Keep
every one. They are facts the reader has already paid to decode; the IR's job is to be a faithful
normalisation of what the file said (apkg-format.md, "The intermediate representation"), not a
projection of what today's schema happens to store. Dropping them would make the reader lossy in a
way no test would catch and a future export would silently inherit.
`FilteredDeckAnkiID` is in any case asserted by `read_test.go:155`, and `IrReview.Factor` /
`LastIntervalSeconds` are written by `write.go:159`.

**4.3 Deleting `ankiTypeRelearning` (`ankischema.go:108`), which has no reference at all.**
Keep. `ankiType*` and `ankiQueue*` are a documented external-format value table, and CLAUDE.md §9
explicitly says the `apkg` package earns this kind of comment because it encodes format facts.
A table with a hole in it is worse than an unused constant.

**4.4 Simplifying `closeQuietly`'s body to `_ = c.Close()`.** The current
`if err := c.Close(); err != nil { return }` looks like noise, but CLAUDE.md §9 forbids `_ = err`
outright, and the comment at `read.go:40-44` records exactly why the error is droppable here
(post-success teardown, after every check that could reveal a misread). Change only the parameter
type (E4); leave the body.

**4.5 `slices.SortFunc` / `slices.Sort` in place of `sort.Slice`.** More modern, but the repo uses
`sort.Slice` in five places across four packages. Consistency wins; this is not the issue to flip
it in.

**4.6 `deriveCardScheduling` taking `db.UserCardState` (`dbexport.go:191`).** Not an IR-boundary
violation. `dbexport.go` *is* the db → IR translator; the rule (architecture.md §4) is that
generated rows never travel past it, which holds — see §1. Introducing an intermediate struct here
would add a type whose only purpose is to be copied field-for-field from the one above it.

**4.7 A guard on `zstdDeclaredSize` returning a negative `int64` for an 8-byte
`Frame_Content_Size` above `MaxInt64` (`read.go:298`).** The declared-size gate would then be
skipped, but `decompressZstd`'s `io.ReadAll(io.LimitReader(dec, limit+1))` still bounds the actual
output, so there is no hole — only a gate that fires later than it could on input no real Anki
export produces. Adding a new error for it would be new behaviour on hostile input for no
correctness gain.

**4.8 Tightening `ankiIntOrString.UnmarshalJSON`'s `strings.Trim(string(b), "\"")`
(`ankischema.go:54`).** It would also accept `"""1001"""`, which `encoding/json` will never hand
it. Leaving it.

**4.9 Deleting architecture.md §20's third subsection outright** now that the deviation is
resolved. Rejected in favour of the trimmed "resolved" record (§2.2): the subsection carries a
design rule that a future importer change could silently undo, and the regression test it names is
the thing that would catch it. A deleted section teaches nobody.

**4.10 Replacing `Export`'s two parallel maps `noteTypeAnkiID` and `noteTypeIdx`
(`dbexport.go:69-70`).** They are keyed identically, but collapsing them requires an
ok-checked index lookup at `dbexport.go:110` where the current code relies on a missing key
yielding `0`. Not worth the risk of changing what a dangling `note_type_id` exports to.

---

## 5. Verification steps (for the implementing session)

1. `go build ./...`, `go vet ./...`, `golangci-lint run` — all clean.
2. `go test ./internal/apkg/` **without** `DATABASE_URL` set. Every non-DB test must pass; the
   DB-backed ones skip (`dbtest_test.go:28`). This covers E1–E13, E21–E29.
3. `bash .claude/skills/run-app/reset-db.sh`, then `go test ./...` with `DATABASE_URL` set. This is
   the only run that exercises E14–E20. Watch specifically:
   - `TestImport_FilesCardDeckFromCardsOwnDeck` — the §20 rule (§2).
   - `TestImport_ResultReportsDeckCardCounts` — E18's rewritten `result.Decks` loop.
   - `TestImport_RevlogBecomesReviewLogAndReplays` — E19's params cache and dropped `GetCard`.
   - `TestExport_RoundTripsThroughReimport` — E12, E17, E19, E21 together, plus `Write`'s output
     re-parsing (CLAUDE.md §10.3).
   - `TestImport_MediaWrittenToStoreAndDB` — E18's deduped `deckIDs`, which must still ref **both**
     of `defaultSynthSpec`'s decks.
   - `internal/http/import_test.go` — the real fixture through the real route.
4. `TestRead_TwiceIsIdentical` and `TestRead_RealSchema18Fixture` are the two that would catch an
   E3 regression. Neither needs a database.
5. After E20, `grep -rn "GetCard(" --include=*.go .` must return no `q.GetCard` /
   `Queries.GetCard` hit, and `git status` must show `internal/db/cards.sql.go` regenerated, not
   hand-edited.
6. CLAUDE.md working rule 5 / §10: this touches the `.apkg` reader/writer, so it always ships a
   test. No *new* test is required — every edit above is covered by an existing one, listed per
   edit — but confirm that claim by running step 3 before and after and diffing nothing but timing.
7. Manual verification for the user (CLAUDE.md §14 step 4): import
   `tests/fixtures/apkg/mathematics-schema18.apkg` through `/import` and confirm the redirect
   lands on the same deck as before the change, and that images still render.

## 6. Changelog

Per CLAUDE.md §14, one entry for the PR, next patch version after 0.1.32:

```
## [0.1.33] - YYYY-MM-DD

### Changed
- Audited `internal/apkg/` for simplification: split the 172-line schema-18 reader into four
  table readers, deduplicated the protobuf field accessors and the container read-then-decompress
  path, and collapsed three parallel note-type maps in the importer into one
  ([#124](https://github.com/Jolls/deckshare/issues/124)).
- A malformed `.apkg` now reports the underlying SQLite or decode failure instead of only
  "collection is missing a required table or column"; `errors.Is` behaviour is unchanged
  ([#124](https://github.com/Jolls/deckshare/issues/124)).
- Import warnings and the `/import` redirect are now deterministic for a given package — both
  previously depended on Go's randomised map iteration
  ([#124](https://github.com/Jolls/deckshare/issues/124)).
- Recorded the `IrNote.primaryDeckAnkiId` flattening in architecture.md §20 as resolved: the Go
  importer files `cards.deck_id` from each card's own home deck and has since #58
  ([#124](https://github.com/Jolls/deckshare/issues/124)).

### Fixed
- Importing a collection with review history no longer issues two redundant queries per reviewed
  card, and writing a package no longer commits once per row
  ([#124](https://github.com/Jolls/deckshare/issues/124)).

### Removed
- Deleted `GetCard`, the last unscoped `SELECT * WHERE id = $1` getter — the importer no longer
  needs it, continuing #122's §9 cleanup ([#124](https://github.com/Jolls/deckshare/issues/124)).
```

---

## 7. Resolved decisions

All open questions below were resolved with the user before implementation.

1. **Note-with-no-cards warning text.** **Apply the fix.** New edit **E30** below.
2. **`decodeVarint` overflow truncation.** **Tighten via `binary.Uvarint`.** New edit **E31** below.
3. **`/import`'s redirect tie-break.** Accept deterministic-by-package-order (lowest Anki deck id
   wins a tie) and document the rule at the redirect site. New edit **E32** below.
4. **`ErrSchema18Config`'s name.** Leave as-is — no rename. Cosmetic mismatch only, not worth the
   API churn in a cleanup-only audit.
5. **Export is unreachable.** Filed as a follow-up issue:
   [#140](https://github.com/Jolls/deckshare/issues/140) (wire `GET /decks/{id}/export`). No code
   change in this plan.
6. **Three export losses with no column.** Accept permanently and document in `docs/apkg-format.md`
   as a named list, rather than filing a schema-change issue. New edit **E33** below.
7. **IR boundary compile-time guard.** Comment only — E22 already covers this. No enforcing test
   added.

---

## 8. Additional edits from resolved decisions

### E30. `ir.go:218` — accurate warning for a note with no cards (resolved decision 1)

Old (inside `resolveHomeDecks`, the loop rewritten by E11):

```go
			warnings = append(warnings, "note "+guidOrAnki(notes[i])+" has no cards; skipped")
```

new:

```go
			warnings = append(warnings, "note "+guidOrAnki(notes[i])+" has no cards, so it has no home deck")
```

Apply this to the rewritten loop body E11 specifies (the `if !ok { warnings = append(...); continue }`
branch). `importNotes`'s own second warning (`dbwrite.go:298`, `"note %q: home deck did not
resolve; skipped"`) is unchanged — it is accurate as written and does the actual skipping.

### E31. `protobuf.go` — `decodeVarint` delegates to `encoding/binary.Uvarint` (resolved decision 2)

Replace `protobuf.go:89-100` in full, old:

```go
// decodeVarint reads a base-128 varint from the start of b. Returns the value and the number of
// bytes consumed, or (0, 0) on a truncated or overlong (>10 byte) varint.
func decodeVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}
```

new:

```go
// decodeVarint reads a base-128 varint from the start of b. Returns the value and the number of
// bytes consumed. binary.Uvarint's own error convention is n == 0 (truncated) or n < 0 (overflow,
// -n bytes read) -- both satisfy the `n <= 0` check every call site below already makes, so this
// is a drop-in replacement for the hand-rolled loop it used to be, and it additionally rejects an
// overlong (>10 byte / >64-bit) varint outright instead of silently truncating it.
func decodeVarint(b []byte) (uint64, int) {
	return binary.Uvarint(b)
}
```

`"encoding/binary"` is already added to `protobuf.go`'s imports by E8. All three call sites
(`protobuf.go:36`, `45`, `62`) are unchanged — each already checks `n <= 0` and returns
`ErrSchema18Config` wrapped with a specific message, which now also covers the overflow case.

### E32. `internal/http/import.go` — document the redirect tie-break (resolved decision 3)

Add a comment above `resultingDeckPath` (`import.go:79`):

```go
// resultingDeckPath picks the deck /import redirects to: the one with the most cards. On a tie,
// the first in decks wins, which after apkg.Import's deterministic ordering (dbwrite.go) means the
// deck with the lowest Anki deck id -- an accepted, documented tie-break rather than a chosen one.
```

No behavior change; `decks[i].CardCount > best.CardCount` (strict `>`) already implements
first-seen-wins on a tie, and E18 already made `ImportResult.Decks`'s order deterministic
(ascending Anki deck id).

### E33. `docs/apkg-format.md` — document the three permanent export losses (resolved decision 6)

Add a new subsection to `docs/apkg-format.md`'s Export section (or the nearest equivalent —
implementing session should locate the section describing what `internal/apkg.Export`/`Write`
produce and add this as its own subsection, e.g. "Known export losses"):

```markdown
### Known export losses

Three round-trip losses are permanent given the current schema — each would need a new column to
fix, and none is planned:

- **`revlog.factor` and `revlog.lastIvl`** (`IrReview.Factor`, `IrReview.LastIntervalSeconds`) are
  read from an imported `.apkg`'s `revlog` table but never persisted anywhere in Enshu's schema, so
  a re-exported collection always writes `0` for both on every review row.
- **FSRS `Position` and `DesiredRetention`** (`IrFSRSState.Position`, `.DesiredRetention`) are
  likewise read on import and not persisted, so export always writes their zero value.
- **`col.crt`** (the collection's creation timestamp) is not stored by Enshu at all — a deck has no
  single "collection" it belongs to the way an Anki collection does — so `Export` synthesises a
  value via `deriveCrt` rather than round-tripping an original one.

None of these affect scheduling correctness or `.apkg` readability: every field above is either
purely informational (SM-2 legacy data Enshu doesn't schedule from — see the `primaryDeckAnkiId`/
`factor` discussion elsewhere in this doc) or a value Anki's own importer recomputes/tolerates a
default for.
```

Adjust heading level and exact placement to match the surrounding document structure; the content
above is the substantive addition.
