# 58 — `.apkg` import: reader + IR → database writer

Plan for [#58](https://github.com/Jolls/enshu/issues/58). Phase 1, build-order step 8
(architecture.md §11). Read [docs/apkg-format.md](../apkg-format.md) and
[docs/anki-schema.md](../anki-schema.md) before implementing — this plan does not restate the
format, it names which parts of it each function is responsible for.

**Implement this plan verbatim.** Every decision below is fixed. The only things left open are
in §10; if one of them blocks you, stop and ask rather than picking.

---

## 1. Scope summary

Build the read half of `internal/apkg/`: a package reader that turns an `.apkg`/`.colpkg` file
(legacy zip container and modern zstd container; collection schema 11 and schema 18+) into an
in-memory **IR**, and an IR → database writer that files that IR into our schema under one
owning user, idempotently on `(owner_id, guid)` for notes and `(owner_id, name)` for decks and
note types, with each card's `deck_id` taken from **that card's own home deck**
(architecture.md §20's resolution), never flattened to the note's deck. Untrusted-input
ceilings (member count, per-member and total decompressed bytes, zstd declared frame size
checked before decompressing) are enforced in the container layer. Out of scope and explicitly
**not** in this issue: the `.apkg` *file writer* (`internal/apkg/write.go` stays a stub —
[#59](https://github.com/Jolls/enshu/issues/59)), the media blob store and any `media_blobs` /
`media_refs` writes ([#60](https://github.com/Jolls/enshu/issues/60)), the `/import` HTTP route
and real-fixture verification ([#62](https://github.com/Jolls/enshu/issues/62)), and verifying
the schema-18 protobuf field numbers against a real export
([#61](https://github.com/Jolls/enshu/issues/61)).

**Scope clarification on the issue's wording** (this is the §10-flagged ambiguity, resolved
here): "reader + write IR into database" means `apkg -> IR -> db` only. `db -> IR -> apkg` is
#59. `write.go` is left exactly as it is (`package apkg`, nothing else).

---

## 2. New / changed files

All new files are in `internal/apkg/` unless stated. Every file starts `package apkg`.

Error-wrapping convention throughout this package, matching `internal/review` and
`internal/db`: sentinel `errors.New("apkg: …")` values in `errors.go`, wrapped at the call site
with `fmt.Errorf("apkg: <what failed>: %w", err)`. Never `_ = err` (CLAUDE.md §9). No `any` /
`interface{}` in this package — it is one of the two correctness-critical packages named in
CLAUDE.md §9. (The protobuf decoder in §2.6 returns concrete typed values, not `any`, for
exactly this reason.)

### 2.1 `internal/apkg/doc.go` (new)

Package doc comment, in the style of `internal/review/doc.go`:

```go
// Package apkg reads and writes Anki .apkg/.colpkg packages through an intermediate
// representation (architecture.md §4, §7): import is apkg -> IR -> db, export is db -> IR ->
// apkg. The IR is where format quirks are normalised, so the schema-11 and schema-18 readers
// converge before any database code runs, and it is what unit tests assert against.
//
// No Anki-derived code (CLAUDE.md §2.8): this reader is written from docs/apkg-format.md and
// docs/anki-schema.md, both clean-room reconstructions. Never read ankitects/anki source into
// this package.
package apkg
```

### 2.2 `internal/apkg/errors.go` (new)

```go
var (
	ErrNotAPackage       = errors.New("apkg: not a zip archive")
	ErrNoCollection      = errors.New("apkg: no collection member in package")
	ErrTooManyMembers    = errors.New("apkg: archive member count exceeds limit")
	ErrMemberTooLarge    = errors.New("apkg: archive member exceeds per-member size limit")
	ErrArchiveTooLarge   = errors.New("apkg: archive exceeds total decompressed size limit")
	ErrBadZstdFrame      = errors.New("apkg: malformed zstd frame header")
	ErrUnknownSchema     = errors.New("apkg: collection matches neither schema 11 nor schema 18")
	ErrCorruptCollection = errors.New("apkg: collection is missing a required table or column")
	ErrMediaIndex        = errors.New("apkg: malformed media index")
	ErrSchema18Config    = errors.New("apkg: schema-18 config blob did not decode to a usable value")
	ErrNoteTypeMismatch  = errors.New("apkg: existing note type has a different field count")
)
```

### 2.3 `internal/apkg/ir.go` (new)

Holds only type declarations and the small pure helpers on them (§3). No I/O, no DB. This file
is what both `read.go` and (later, #59) `write.go` sit either side of.

### 2.4 `internal/apkg/read.go` (currently an empty stub — fill in)

Container handling, schema detection, and the two schema readers' orchestration. Exported
surface, and nothing else exported from this file:

```go
// ArchiveLimits bounds an untrusted package (architecture.md §8: a shared deck is other users'
// bytes). Zero fields are rejected -- use DefaultArchiveLimits and adjust.
type ArchiveLimits struct {
	MaxMembers     int   // zip entries in the archive
	MaxMemberBytes int64 // decompressed bytes of any one member
	MaxTotalBytes  int64 // decompressed bytes across all members read
}

func DefaultArchiveLimits() ArchiveLimits

// ReadFile reads the package at path. It is the entry point the import handler (#62) calls.
func ReadFile(path string, limits ArchiveLimits) (*IrCollection, error)

// Read reads a package from r, which must be size bytes long.
func Read(r io.ReaderAt, size int64, limits ArchiveLimits) (*IrCollection, error)
```

Unexported functions in this file, in call order:

| Function | Signature | Responsibility |
|---|---|---|
| `openArchive` | `(io.ReaderAt, int64, ArchiveLimits) (*zip.Reader, error)` | `zip.NewReader`; reject `> MaxMembers` with `ErrTooManyMembers`; wrap a zip parse failure as `ErrNotAPackage`. |
| `memberBytes` | `(*zip.File, ArchiveLimits, *int64) ([]byte, error)` | Reads one member with both ceilings enforced (§4). The `*int64` is the running total budget, decremented in place. |
| `sniffZstd` | `([]byte) bool` | First four bytes are `28 B5 2F FD`. |
| `zstdDeclaredSize` | `([]byte) (int64, bool, error)` | Parses `Frame_Content_Size` out of the frame header **without decompressing** (§4). `false` means the frame declares no size. |
| `decompressZstd` | `([]byte, ArchiveLimits) ([]byte, error)` | Declared-size gate, then streaming decode under an `io.LimitReader` (§4). |
| `pickCollectionMember` | `(*zip.Reader) (*zip.File, error)` | `collection.anki21b`, then `collection.anki21`, then `collection.anki2` — newest first, load-bearing (apkg-format.md's downgrade-stub trap). `ErrNoCollection` if none. |
| `openCollection` | `([]byte) (*sql.DB, func(), error)` | Writes bytes to a file in `os.MkdirTemp`, opens `sql.Open("sqlite", "file:"+p+"?mode=ro")`, returns a cleanup that closes the DB and removes the temp dir. (`modernc.org/sqlite` opens files, not byte slices.) |
| `detectSchema` | `(*sql.DB) (int, error)` | `SELECT name FROM sqlite_master WHERE type='table'`. All four of `notetypes`, `fields`, `templates`, `decks` present → `18`; `col` present → `11`; else `ErrUnknownSchema`. **Never reads `col.ver`** (apkg-format.md: a repacked package can claim 18 while carrying only JSON blobs). |
| `readCrt` | `(*sql.DB) (time.Time, error)` | `SELECT crt FROM col LIMIT 1`; `time.Unix(crt,0).UTC()`. Opaque anchor, used verbatim, nothing added (apkg-format.md). |
| `readNotes` | `(*sql.DB) ([]IrNote, error)` | §5, shared by both schemas. |
| `readCards` | `(*sql.DB, time.Time) ([]IrCard, error)` | §5, shared. `crt` is needed for due conversion. |
| `readRevlog` | `(*sql.DB) ([]IrReview, []string, error)` | §5, shared. Second return is warnings. |

### 2.5 `internal/apkg/ankischema.go` (currently an empty stub — fill in)

Anki's SQLite shapes and constants. No behaviour beyond decoding the JSON blobs. Contents:

- The SQL statement constants, one `const` per query, e.g.
  `const sqlSelectCol11 = "SELECT crt, models, decks FROM col LIMIT 1"`,
  `sqlSelectNotes`, `sqlSelectCards`, `sqlSelectRevlog`, `sqlSelectNotetypes18`,
  `sqlSelectFields18`, `sqlSelectTemplates18`, `sqlSelectDecks18`.
  **Order by integer id only — never `ORDER BY name`** (apkg-format.md: schema-18 name columns
  declare `COLLATE unicase` and the driver may not be able to register it).
- The schema-11 JSON blob structs, decoded with `encoding/json` (field names exactly as in
  anki-schema.md's "Legacy JSON blob shapes"):

```go
type ankiModel11 struct {
	ID    int64            `json:"id"`
	Name  string           `json:"name"`
	Type  int              `json:"type"`  // 0 standard, 1 cloze
	Sortf int32            `json:"sortf"`
	CSS   string           `json:"css"`
	Flds  []ankiField11    `json:"flds"`
	Tmpls []ankiTemplate11 `json:"tmpls"`
}
type ankiField11 struct {
	Name   string `json:"name"`
	Ord    int32  `json:"ord"`
	Sticky bool   `json:"sticky"`
	RTL    bool   `json:"rtl"`
	Font   string `json:"font"`
	Size   int32  `json:"size"`
}
type ankiTemplate11 struct {
	Name  string `json:"name"`
	Ord   int32  `json:"ord"`
	Qfmt  string `json:"qfmt"`
	Afmt  string `json:"afmt"`
	Bqfmt string `json:"bqfmt"`
	Bafmt string `json:"bafmt"`
}
type ankiDeck11 struct {
	ID   int64  `json:"id"`
	Name string `json:"name"` // "::"-separated
	Desc string `json:"desc"`
	Dyn  int    `json:"dyn"`  // 1 = filtered
}
```

  `col.models` and `col.decks` are JSON **objects keyed by id-as-string**, so decode into
  `map[string]ankiModel11` / `map[string]ankiDeck11` and take the id from the struct's own `id`
  field, not the map key.
- Anki enum constants, named not magic: `ankiQueueNew = 0`, `ankiQueueLearning = 1`,
  `ankiQueueReview = 2`, `ankiQueueDayLearning = 3`, `ankiQueuePreview = 4`,
  `ankiQueueSuspended = -1`, `ankiQueueSchedBuried = -2`, `ankiQueueUserBuried = -3`;
  `ankiTypeNew = 0` … `ankiTypeRelearning = 3`; `ankiFlagMask = 0x7`;
  `epochSecondsThreshold = 1_000_000_000` (apkg-format.md's hold-card magnitude heuristic).
- The schema-18 protobuf field-number table (§5.2), each entry carrying a `// ❓ unverified —
  #61` comment and a provenance comment naming where the number came from.
- The `cards.data` JSON struct:

```go
type ankiCardData struct {
	Pos *int32   `json:"pos"`
	S   *float64 `json:"s"`
	D   *float64 `json:"d"`
	DR  *float64 `json:"dr"`
}
```

  Pointers so "absent" and "zero" are distinguishable. An unparseable or unrecognised `data`
  is treated as absent, never as an error (apkg-format.md).

### 2.6 `internal/apkg/protobuf.go` (new)

A minimal, hand-written protobuf **wire-format** decoder — no `google.golang.org/protobuf`
dependency, no `.proto` files, and nothing transcribed from Anki (CLAUDE.md §2.8). It decodes
the wire format only; which field number means what lives in `ankischema.go`.

```go
// protoWireType is the low three bits of a protobuf tag.
type protoWireType uint8

const (
	protoVarint protoWireType = 0
	protoI64    protoWireType = 1
	protoBytes  protoWireType = 2
	protoI32    protoWireType = 5
)

// protoField is one decoded field occurrence, in encounter order.
type protoField struct {
	Number uint32
	Type   protoWireType
	Varint uint64 // valid for protoVarint / protoI64 / protoI32
	Bytes  []byte // valid for protoBytes; aliases the input, never copied
}

// decodeProto walks b and returns every top-level field occurrence. Groups (wire types 3 and 4)
// are rejected: no message this reader touches uses them, and skipping them correctly needs a
// nesting stack that would only ever run on malformed input.
func decodeProto(b []byte) ([]protoField, error)

// protoString returns the last occurrence of field number n as a string, and whether it was
// present with wire type 2.
func protoString(fields []protoField, n uint32) (string, bool)

// protoUint returns the last varint occurrence of field number n.
func protoUint(fields []protoField, n uint32) (uint64, bool)

// protoMessage returns the last length-delimited occurrence of n, decoded as a nested message.
func protoMessage(fields []protoField, n uint32) ([]protoField, bool)
```

Hard bound: `decodeProto` returns `ErrSchema18Config`-wrapped errors on a truncated varint, a
length prefix that runs past the end of `b`, or more than 4096 field occurrences in one message
(these blobs are small config records; anything larger is malformed or hostile input).

### 2.7 `internal/apkg/media.go` (currently an empty stub — fill in)

Media **index** parsing only. No filesystem store, no `media_blobs` / `media_refs` writes —
that is #60.

```go
// readMediaIndex parses the package's "media" member into index -> filename. The legacy
// container spells it as a JSON object ({"0":"cat.jpg"}); the modern container spells it as a
// protobuf list where an entry's POSITION is the zip member name the JSON spelled as a key
// (apkg-format.md). Sniffed on the first byte -- '{' is JSON, anything else is protobuf -- not
// branched on the package version, so a version number's meaning cannot be got wrong.
func readMediaIndex(b []byte) (map[string]string, error)

// collectMedia reads every media member named by idx, NFC-normalises its filename, hashes it,
// and applies the first-seen-wins collision policy (docs/schema.md, Media). Entries are visited
// in ascending numeric index order so the policy is deterministic across re-imports. Returns the
// media entries and one warning per dropped file.
func collectMedia(z *zip.Reader, idx map[string]string, limits ArchiveLimits, budget *int64) ([]IrMedia, []string, error)
```

Details:

- NFC normalisation uses `golang.org/x/text/unicode/norm` (`norm.NFC.String(name)`) — a package
  in the module graph already (indirect), promoted to a direct dependency (§9). A macOS-produced
  package can carry NFD in the index and the note field will reference NFC (apkg-format.md).
- Collision policy: two entries whose normalised filenames are equal — if the SHA-256 matches,
  drop silently; if it differs, keep the first and append a warning
  `fmt.Sprintf("media: dropped %q (a different file of the same name was already imported)", name)`.
- Media members are **stored uncompressed and are never zstd-sniffed** (apkg-format.md: media
  bytes are arbitrary, and a legitimate file starting with the zstd magic would be mangled).
  Only the collection member and the `media` member are sniffed.
- The protobuf media list: entries are repeated field number `mediaEntryField` (see §5.2's
  table) of the top-level message, each a nested message whose field 1 is the filename string.
  The entry's zero-based position in the list is the zip member name.

### 2.8 `internal/apkg/dbwrite.go` (new) — the IR → DB writer

See §6 for the algorithm. Exported surface:

```go
// ImportResult is the per-import tally the import UI (#62) reports.
type ImportResult struct {
	DecksCreated      int
	DecksReused       int
	NoteTypesCreated  int
	NoteTypesReused   int
	NotesInserted     int
	NotesUpdated      int
	CardsUpserted     int
	ReviewsInserted   int
	CardStatesSeeded  int
	CardStatesReplayed int
	MediaDeferred     int // len(col.Media); #60 wires these up, this issue counts them
	Warnings          []string
}

// Import files col into the database under ownerID. Must be called inside a transaction it does
// not own; the caller commits. Idempotent: re-importing the same package updates rather than
// duplicates (CLAUDE.md §2.2).
func Import(ctx context.Context, tx pgx.Tx, ownerID pgtype.UUID, col *IrCollection, now time.Time) (ImportResult, error)
```

Existing DB-layer functions it calls: `db.New`, `db.CreateDeckWithAccess`,
`q.GetDeckForContentEdit`, `q.ListFieldsForNoteType`, `q.ListTemplatesForNoteType`,
`review.EffectiveParams`, `review.LockCards` (§2.10), `review.ReplayCard`. New generated
queries it calls are in §2.9.

### 2.9 `internal/db/queries/import.sql` (new) + regenerated `internal/db/import.sql.go`

New `sqlc` queries. Run `go generate ./...` and commit the generated file (CLAUDE.md §16); do
not hand-edit it. Every query that reaches an existing deck joins `deck_access` — authorisation
is explicit at the query layer (CLAUDE.md §9).

```sql
-- Import (#58). Every statement here is called only from internal/apkg/dbwrite.go, inside the
-- one transaction an import runs in.

-- name: GetDeckByOwnerAndName :one
SELECT * FROM decks WHERE owner_id = sqlc.arg(owner_id) AND name = sqlc.arg(name);

-- Re-import reuses the owner's deck of that name (docs/schema.md). anki_id is export fidelity
-- only and is never a key -- deck id 1 is "Default" in every collection ever made.
-- name: SetDeckAnkiID :execrows
UPDATE decks SET anki_id = sqlc.narg(anki_id), modified_at = now()
WHERE id = sqlc.arg(deck_id) AND anki_id IS NULL;

-- name: CreateImportedDeck :one
INSERT INTO decks (owner_id, name, description, anki_id)
VALUES (sqlc.arg(owner_id), sqlc.arg(name), sqlc.arg(description), sqlc.narg(anki_id))
RETURNING *;

-- name: GetNoteTypeByOwnerAndName :one
SELECT * FROM note_types WHERE owner_id = sqlc.arg(owner_id) AND name = sqlc.arg(name);

-- name: CreateImportedNoteType :one
INSERT INTO note_types (owner_id, name, css, is_cloze, sort_field_idx, anki_id)
VALUES (sqlc.arg(owner_id), sqlc.arg(name), sqlc.arg(css), sqlc.arg(is_cloze),
        sqlc.arg(sort_field_idx), sqlc.narg(anki_id))
RETURNING *;

-- The full field row: internal/db/queries/fields.sql's CreateField carries name only, which
-- would silently drop an imported field's font/size/rtl/sticky.
-- name: CreateImportedField :one
INSERT INTO fields (note_type_id, ordinal, name, font, size, is_rtl, sticky)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- Same reason as CreateImportedField: templates.sql's CreateTemplate has no browser formats.
-- name: CreateImportedTemplate :one
INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt, browser_qfmt, browser_afmt)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: GetNoteByOwnerAndGuid :one
SELECT * FROM notes WHERE owner_id = sqlc.arg(owner_id) AND guid = sqlc.arg(guid);

-- Same deck_access authorisation as notes.sql's CreateNote, plus the imported columns. owner_id
-- comes from the DECK, not the caller (migration 00015's composite FK rejects any other value).
-- name: CreateImportedNote :one
INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, tags, checksum,
                   created_at, modified_at, anki_id)
SELECT sqlc.arg(guid), d.owner_id, nt.id, d.id, sqlc.arg(fields), sqlc.arg(tags),
       sqlc.arg(checksum), sqlc.arg(created_at), sqlc.arg(modified_at), sqlc.narg(anki_id)
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
JOIN note_types nt ON nt.id = sqlc.arg(note_type_id) AND nt.owner_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
RETURNING *;

-- Re-import updates rather than inserts (CLAUDE.md §2.2, apkg-format.md). deck_id is NOT
-- touched: a re-import must not silently move a note the user has since filed elsewhere.
-- name: UpdateImportedNote :execrows
UPDATE notes n
SET fields = sqlc.arg(fields), tags = sqlc.arg(tags), checksum = sqlc.arg(checksum),
    note_type_id = sqlc.arg(note_type_id), modified_at = sqlc.arg(modified_at),
    anki_id = sqlc.narg(anki_id)
FROM deck_access da
WHERE n.id = sqlc.arg(note_id) AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;

-- cards.deck_id comes from the CARD's own home deck, never flattened to the note's deck
-- (architecture.md §20). ON CONFLICT keeps the existing card id, and with it its
-- user_card_state and review_log history (docs/schema.md's card-regeneration trap).
-- name: UpsertImportedCard :one
INSERT INTO cards (note_id, template_id, ordinal, deck_id, anki_id)
VALUES ($1, $2, $3, $4, sqlc.narg(anki_id))
ON CONFLICT (note_id, ordinal) DO UPDATE
SET deck_id = EXCLUDED.deck_id, template_id = EXCLUDED.template_id, anki_id = EXCLUDED.anki_id
RETURNING *;

-- Imported history. id is omitted so the column DEFAULT uuidv7() supplies it -- an imported row
-- has no client-generated id and this package must not grow a UUID dependency to invent one.
-- stability_before / difficulty_before / fsrs_version stay NULL: Anki's revlog carries SM-2
-- values, and writing a fabricated FSRS prior would be permanently wrong training data
-- (CLAUDE.md §2.5). ON CONFLICT DO NOTHING makes a re-import a no-op on the dedup key.
-- name: InsertImportedReviewLog :execrows
INSERT INTO review_log (
    user_id, card_id, rating, reviewed_at, duration_ms,
    state_before, learning_steps_before, elapsed_days_before, scheduled_days_after,
    review_kind, anki_id
) VALUES (
    sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(rating), sqlc.arg(reviewed_at),
    sqlc.narg(duration_ms), sqlc.arg(state_before), sqlc.arg(learning_steps_before),
    sqlc.arg(elapsed_days_before), sqlc.arg(scheduled_days_after),
    sqlc.arg(review_kind), sqlc.narg(anki_id)
)
ON CONFLICT (user_id, card_id, anki_id) DO NOTHING;

-- The seed path for a card that arrives with scheduling state but no review history. DO NOTHING,
-- not DO UPDATE: an existing row is this user's own live progress and an imported snapshot never
-- outranks it.
-- name: SeedImportedUserCardState :execrows
INSERT INTO user_card_state (user_id, card_id, due, stability, difficulty, state, reps, lapses,
                             elapsed_days, scheduled_days, learning_steps, last_review,
                             suspended, flag)
VALUES (sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(due), sqlc.arg(stability),
        sqlc.arg(difficulty), sqlc.arg(state), sqlc.arg(reps), sqlc.arg(lapses),
        sqlc.arg(elapsed_days), sqlc.arg(scheduled_days), sqlc.arg(learning_steps),
        sqlc.narg(last_review), sqlc.arg(suspended), sqlc.arg(flag))
ON CONFLICT (user_id, card_id) DO NOTHING;
```

### 2.10 `internal/review/lock.go` (changed — one addition)

`ReplayCard`'s doc comment requires the `(user, card)` advisory lock, and `lockKey`/`lockKeys`
are unexported. Add one exported wrapper; change nothing else in the file:

```go
// LockCards takes the per-(user, card) advisory lock for every card in cardIDs, in ascending
// key order -- the same deadlock-avoidance rule GradeBatch follows (architecture.md §6). The
// .apkg importer needs it because ReplayCard must run under the lock and an import can race a
// live grade of the same card on a re-import.
func LockCards(ctx context.Context, q *db.Queries, userID pgtype.UUID, cardIDs []pgtype.UUID) error
```

Implementation: build the deduped ascending key slice exactly as `lockKeys` does (refactor
`lockKeys` to delegate to a shared `sortedKeys(userID pgtype.UUID, cardIDs []pgtype.UUID) []int64`
so there is one ordering rule, not two), then call `acquireLocks`.

### 2.11 `internal/apkg/write.go` — unchanged

Stays the one-line stub. #59 owns it.

---

## 3. The IR type definitions (`internal/apkg/ir.go`)

Exact declarations. Anki ids are `int64` throughout and are carried only for `anki_id` export
fidelity — never used as a key (docs/schema.md).

```go
// IrCollection is one package, fully normalised. Both schema readers converge on this before any
// database code runs (architecture.md §4).
type IrCollection struct {
	Crt           time.Time // col.crt, UTC. An opaque anchor: used verbatim, nothing added.
	SchemaVersion int       // 11 or 18, from table presence -- never from col.ver
	NoteTypes     []IrNoteType
	Decks         []IrDeck
	Notes         []IrNote
	Cards         []IrCard
	Reviews       []IrReview
	Media         []IrMedia
	Warnings      []string // non-fatal findings, surfaced to the importing user
}

type IrNoteType struct {
	AnkiID       int64
	Name         string
	CSS          string
	IsCloze      bool
	SortFieldIdx int32
	Fields       []IrField    // sorted by Ordinal; ord is authoritative, array order is not
	Templates    []IrTemplate // sorted by Ordinal, same rule
}

type IrField struct {
	Ordinal int32
	Name    string
	Font    string
	Size    int32
	IsRTL   bool
	Sticky  bool
}

type IrTemplate struct {
	Ordinal     int32
	Name        string
	Qfmt        string
	Afmt        string
	BrowserQfmt string
	BrowserAfmt string
}

type IrDeck struct {
	AnkiID      int64
	Name        string // full path, "::"-separated in BOTH schemas -- schema 18's \x1f is normalised
	Description string
	IsFiltered  bool // dyn != 0. We have no filtered decks; cards are filed under their home deck
}

type IrNote struct {
	AnkiID         int64
	Guid           string
	NoteTypeAnkiID int64
	Fields         []string // split on \x1f, indexed by IrField.Ordinal
	Tags           []string // space-surrounded source string, empties dropped
	Checksum       int64    // notes.csum
	Created        time.Time // notes.id, epoch MILLISECONDS
	Modified       time.Time // notes.mod, epoch SECONDS
	// HomeDeckAnkiID is the resolved home deck of this note's LOWEST-numbered card
	// (architecture.md §20). It fills notes.deck_id: where the note was first filed, where the
	// notes list shows it, and the default for cards generated later. It is NOT what any card's
	// deck_id comes from -- that is IrCard.DeckAnkiID, per card.
	HomeDeckAnkiID int64
}

type IrDueKind uint8

const (
	DueNone     IrDueKind = iota // no meaningful due value
	DuePosition                  // new-card queue position; no calendar meaning
	DueAt                        // an absolute instant
)

// IrDue is cards.due after the three-way disambiguation in apkg-format.md, resolved from odue
// when the card sits in a filtered deck. The discriminator is `queue`, not `type`.
type IrDue struct {
	Kind     IrDueKind
	Position int32
	At       time.Time // UTC; valid only when Kind == DueAt
}

type IrCard struct {
	AnkiID     int64
	NoteAnkiID int64
	// DeckAnkiID is the card's real HOME deck: odid when odid != 0, else did. This is what
	// cards.deck_id is filed from (architecture.md §20's resolution -- the whole point of #58's
	// second bullet).
	DeckAnkiID         int64
	FilteredDeckAnkiID int64 // did when odid != 0, else 0. Carried for export fidelity only
	Ordinal            int32 // template ordinal, or cloze ordinal on a cloze note type
	Type               int32 // Anki cards.type, verbatim
	Queue              int32 // Anki cards.queue, verbatim
	Due                IrDue
	IntervalSeconds    int64 // cards.ivl normalised: days*86400 when positive, |ivl| when negative
	Factor             int32 // SM-2 ease x1000. NEVER mapped to FSRS difficulty (apkg-format.md)
	Reps               int32
	Lapses             int32
	Flag               int16 // cards.flags & 0x7
	Suspended          bool  // queue == -1
	Buried             bool  // queue == -2 or -3
	FSRS               *IrFSRSState // nil when cards.data carries no usable FSRS state
}

type IrFSRSState struct {
	Stability        float64
	Difficulty       float64
	DesiredRetention float64 // 0 when absent
	Position         int32   // cards.data "pos", the preserved new-card position; 0 when absent
}

type IrReview struct {
	AnkiID              int64     // revlog.id, epoch MILLISECONDS -- also the review instant
	CardAnkiID          int64
	ReviewedAt          time.Time // UTC, from AnkiID
	Rating              int16     // revlog.ease, 1..4. ease == 0 is a manual reschedule, dropped
	IntervalSeconds     int64     // revlog.ivl, same sign convention as cards.ivl
	LastIntervalSeconds int64     // revlog.lastIvl, same convention
	Factor              int32
	DurationMs          int32 // revlog.time
	Kind                int16 // revlog.type: 0 learning, 1 review, 2 relearning, 3 cram, 4 manual
}

type IrMedia struct {
	Index     string // the zip member name -- the numeric index the media index keyed it by
	Filename  string // NFC-normalised
	SHA256    string // lowercase hex
	SizeBytes int64
	Data      []byte
}
```

Pure helpers in the same file:

```go
// intervalSeconds normalises Anki's dual-unit interval: days when positive, seconds when
// negative (apkg-format.md). Applies to cards.ivl, revlog.ivl and revlog.lastIvl alike -- the IR
// carries seconds throughout so the distinction cannot reappear downstream.
func intervalSeconds(ivl int64) int64

// splitFields splits notes.flds on \x1f (unit separator).
func splitFields(flds string) []string

// splitTags splits notes.tags, which is space-separated AND space-surrounded, dropping empties.
func splitTags(tags string) []string

// normaliseDeckName converts schema 18's \x1f hierarchy separator to schema 11's "::".
// Idempotent, so it is safe to call on both readers' output.
func normaliseDeckName(name string) string

// resolveDue applies apkg-format.md's queue/odid/odue table.
func resolveDue(queue, typ int32, due, odue, odid int64, crt time.Time) IrDue

// resolveHomeDecks fills every IrNote.HomeDeckAnkiID from the note's lowest-AnkiID card's
// DeckAnkiID (architecture.md §20). Notes with no cards keep 0 and are reported as a warning.
func resolveHomeDecks(notes []IrNote, cards []IrCard) []string
```

`resolveDue`'s exact rule (this is the trap that produces plausible-looking wrong data, so it is
spelled out rather than left to the implementer):

1. If `odid != 0`, the value to interpret is `odue`, not `due`. Otherwise it is `due`.
2. Switch on **`queue`**, not `type`:
   - `0` (new) → `DuePosition{Position: int32(v)}`.
   - `1` (learning), `4` (preview) → `DueAt{time.Unix(v, 0).UTC()}` — epoch seconds.
   - `2` (review), `3` (day-learning) → `DueAt{crt.Add(time.Duration(v) * 24 * time.Hour)}` —
     days since `crt`. **`users.day_start_hour` is not applied** — doing so shifts every
     imported review card (apkg-format.md).
   - negative (`-1`, `-2`, `-3`, held) → the queue that would have discriminated is gone. If
     `type == 0`, `DuePosition`. Else if `v >= epochSecondsThreshold`, epoch seconds; else days
     since `crt`.
   - anything else → `DueNone`.

---

## 4. Container / decompression handling

**Detection is by bytes, never by version number** (apkg-format.md): the reader does not branch
on the `meta` member at all. `archive/zip` opens the container in both cases; the difference is
that a modern package's `collection.*` and `media` members are zstd frames *inside* the zip
member. So:

1. `zip.NewReader` over the whole file.
2. Reject `len(z.File) > limits.MaxMembers` → `ErrTooManyMembers`.
3. For the chosen collection member and the `media` member only: read the member's bytes, then
   `sniffZstd` the first four bytes for `28 B5 2F FD` and decompress if they match.
4. Media members are never sniffed (§2.7).

`memberBytes` enforces both ceilings, and enforces them against **actual** bytes, not the zip
header's claim (a header can lie):

```go
if int64(f.UncompressedSize64) > limits.MaxMemberBytes { return nil, ErrMemberTooLarge }
rc, err := f.Open(); defer rc.Close()
buf, err := io.ReadAll(io.LimitReader(rc, limits.MaxMemberBytes+1))
if int64(len(buf)) > limits.MaxMemberBytes { return nil, ErrMemberTooLarge }
if int64(len(buf)) > *budget { return nil, ErrArchiveTooLarge }
*budget -= int64(len(buf))
```

**zstd declared-size gate.** `decompressZstd` calls `zstdDeclaredSize` first and refuses a frame
whose declared `Frame_Content_Size` exceeds `MaxMemberBytes` (or the remaining budget) *before*
any decompression happens — `DecodeAll` is one uninterruptible allocation, so a post-hoc check
is too late (apkg-format.md). A frame that declares no size is not rejected; it is decoded
through `zstd.NewReader` + `io.ReadAll(io.LimitReader(dec, limit+1))` and rejected if it
overruns. Parse the header per the public zstd frame-format spec:

- bytes 0–3: magic `28 B5 2F FD` (already sniffed).
- byte 4: Frame_Header_Descriptor. Bits 7–6 = `FCS_Field_Size` code, bit 5 = Single_Segment_Flag,
  bits 1–0 = Dictionary_ID_Flag.
- Frame_Content_Size field size in bytes: code 0 → 1 if Single_Segment_Flag else 0; code 1 → 2;
  code 2 → 4; code 3 → 8.
- Window_Descriptor byte present only when Single_Segment_Flag is 0.
- Dictionary_ID size: flag 0/1/2/3 → 0/1/2/4 bytes.
- FCS is little-endian; the 2-byte form has 256 added to it.
- A header shorter than the offsets require → `ErrBadZstdFrame`.

Also pass `zstd.WithDecoderMaxMemory(uint64(limits.MaxMemberBytes))` when constructing the
decoder, as a second, library-level bound.

**Ceiling constants.** No precedent exists in the repo or the docs for specific numbers — the
docs require the ceilings, not particular values. These are proposed literals, flagged as an
assumption (§10):

```go
func DefaultArchiveLimits() ArchiveLimits {
	return ArchiveLimits{
		MaxMembers:     50_000,      // the one real export inspected had ~550 members
		MaxMemberBytes: 512 << 20,   // 512 MiB: a large collection.anki21 with room to spare
		MaxTotalBytes:  2 << 30,     // 2 GiB across everything read
	}
}
```

Rationale to record in the comment: the verified export (apkg-format.md) is 546 media files plus
two collections, so 50 000 members is ~90× real-world headroom while still bounding the number
of `f.Open()` calls; 512 MiB per member and 2 GiB total bound the worst case a single import can
allocate on a self-hosted box.

---

## 5. Schema 11 vs schema 18+ parsing

Detection is by **table presence** — all four of `notetypes`, `fields`, `templates`, `decks` →
schema 18; else `col` → schema 11; else `ErrUnknownSchema`. `col.ver` is never consulted
(apkg-format.md: a repacked or downgraded package can claim 18 while carrying only the JSON
blobs, and schema 18 keeps `col.models`/`col.decks` as *emptied* columns, so "does it parse"
sees an empty collection rather than an error).

### 5.1 Schema 11 (`readSchema11(dbh *sql.DB) ([]IrNoteType, []IrDeck, error)`)

`SELECT crt, models, decks FROM col LIMIT 1`.

- `models` → `map[string]ankiModel11`. Per model: `IsCloze = (Type == 1)`,
  `SortFieldIdx = Sortf`, `CSS = Css`, fields from `Flds`, templates from `Tmpls`.
- **Sort `Flds` and `Tmpls` by their own `ord`, and use `ord` as `IrField.Ordinal` /
  `IrTemplate.Ordinal` — never the array index.** `notes.flds` is indexed by `ord`, so a reader
  trusting array order maps every field value onto the wrong field name, and the two schema
  readers then silently disagree (apkg-format.md). This is one of the two adversarial synthetic
  fixtures (§7).
- `decks` → `map[string]ankiDeck11`. Name is already `::`-separated (run it through
  `normaliseDeckName` anyway — idempotent). `IsFiltered = (Dyn != 0)`.
- `dconf` / `conf` are **not read**. Deck presets are not imported by this issue (our
  `decks.preset` keeps its `'{}'` default); FSRS parameters are per-user and never taken from a
  package (CLAUDE.md §2.3/§2.4).

### 5.2 Schema 18+ (`readSchema18(dbh *sql.DB) ([]IrNoteType, []IrDeck, error)`)

Column reads (plain SQL, all ordered by integer id):

| Table | Columns read | Protobuf column | What only the protobuf has |
|---|---|---|---|
| `notetypes` | `id`, `name` | `config` | `is_cloze` (kind), `sort_field_idx`, `css` |
| `fields` | `ntid`, `ord`, `name` | `config` | font, size, rtl, sticky (all optional — defaults on absence) |
| `templates` | `ntid`, `ord`, `name` | `config` | `qfmt`, `afmt`, browser variants — **required** |
| `decks` | `id`, `name` | `common`, `kind` | description, filtered flag |

`fields.ord` / `templates.ord` are real columns here and are authoritative, same as schema 11.
Deck names use `\x1f` as the hierarchy separator and MUST go through `normaliseDeckName`;
missing this silently flattens or mangles a deck tree (apkg-format.md).

**The protobuf field numbers are the ❓ part of this issue.** They live in one table in
`ankischema.go`:

```go
// Schema-18 protobuf field numbers. ALL ❓ UNVERIFIED against a real export -- #61 exists to
// close this, and tests/fixtures/apkg/README.md explains why a synthetic schema-18 fixture
// cannot. Provenance for each number is recorded beside it; nothing here is transcribed from
// ankitects/anki (CLAUDE.md §2.8).
const (
	ntConfigKindField     uint32 = /* … */ // 0 standard, 1 cloze
	ntConfigSortFieldNum  uint32 = /* … */
	ntConfigCSSField      uint32 = /* … */
	fieldConfigFontField  uint32 = /* … */
	fieldConfigSizeField  uint32 = /* … */
	fieldConfigRTLField   uint32 = /* … */
	fieldConfigStickyField uint32 = /* … */
	tmplConfigQFmtField   uint32 = /* … */
	tmplConfigAFmtField   uint32 = /* … */
	tmplConfigBQFmtField  uint32 = /* … */
	tmplConfigBAFmtField  uint32 = /* … */
	deckCommonDescField   uint32 = /* … */
	deckKindFilteredField uint32 = /* … */
	mediaEntryField       uint32 = /* … */
	mediaEntryNameField   uint32 = /* … */
)
```

Because the numbers are unverified, the schema-18 reader **fails loudly rather than importing
plausible-looking wrong data**: after decoding, `validateSchema18Decode` asserts that every
template yields a non-empty `Qfmt` and that every note type yields at least one field; if not,
the whole read fails with `ErrSchema18Config` wrapped with the note-type/template name. A
half-decoded note type would render blank cards for every note of that type, which is exactly
the silent wrongness CLAUDE.md §10 ranks above everything else.

### 5.3 Shared by both schemas

- `notes`: `SELECT id, guid, mid, mod, tags, flds, csum FROM notes ORDER BY id`.
  `Created = time.UnixMilli(id).UTC()` (epoch **ms**), `Modified = time.Unix(mod, 0).UTC()`
  (epoch **s**). Fields split on `\x1f`; tags via `splitTags`.
- `cards`: `SELECT id, nid, did, ord, type, queue, due, ivl, factor, reps, lapses, odue, odid, flags, data FROM cards ORDER BY id`.
  `DeckAnkiID = odid` when `odid != 0` else `did`; `FilteredDeckAnkiID = did` when `odid != 0`
  else 0. `Due = resolveDue(...)`. `IntervalSeconds = intervalSeconds(ivl)`.
  `Flag = int16(flags & 0x7)`. `Suspended = queue == -1`, `Buried = queue == -2 || queue == -3`.
  `data` unmarshalled into `ankiCardData`; `FSRS` is non-nil only when both `s` and `d` are
  present and finite; anything else is treated as absent, never an error (apkg-format.md).
- `revlog`: `SELECT id, cid, ease, ivl, lastIvl, factor, time, type FROM revlog ORDER BY id`.
  `ReviewedAt = time.UnixMilli(id).UTC()`. **`ease == 0` rows are dropped** with a warning —
  they are manual reschedules, not answers, and `review_log.rating` has a `CHECK (1..4)`.
  Both `ivl` and `lastIvl` go through `intervalSeconds` (apkg-format.md's correction: the
  negative-seconds encoding is not unique to `cards.ivl`; read naively it is an 86 400× error).
  An absent `revlog` table is **normal**, not malformed (a deck export without scheduling has
  none): probe `sqlite_master` and return an empty slice.
- `graves`, `deck_config`, `config`, `tags`, `col.conf` are not read at all.

---

## 6. IR → DB writer

`Import` runs entirely inside the caller's transaction, in this order. Any error aborts and is
returned; the caller rolls back. The whole import is one transaction — partial imports are worse
than none, and Phase 1 has no import large enough for that to be a problem.

**Step 1 — decks.** For each `IrDeck`, in `Name` order:

1. `q.GetDeckByOwnerAndName(ownerID, name)`.
2. On `pgx.ErrNoRows`: `db.CreateDeckWithAccess(ctx, tx, ownerID, name, description)` (grants
   the creator all six flags), then `q.SetDeckAnkiID`. `DecksCreated++`.
3. On a hit: authorise with `q.GetDeckForContentEdit(userID, deckID)` — `pgx.ErrNoRows` there
   means the owner lacks `can_edit_content` on their own deck, which is a hard error, not a
   skip. `q.SetDeckAnkiID` (a no-op when already set). `DecksReused++`. The deck's description
   is **not** overwritten — a re-import must not clobber a description the user edited.

Build `deckByAnkiID map[int64]pgtype.UUID`. A filtered deck (`IsFiltered`) is **not created at
all**: no card is ever filed in one (§3's `DeckAnkiID` is the home deck), so creating it would
leave an empty deck the user never asked for. Record a warning naming it.

**Step 2 — note types.** For each `IrNoteType`:

1. `q.GetNoteTypeByOwnerAndName(ownerID, name)`.
2. On `pgx.ErrNoRows`: `q.CreateImportedNoteType`, then one `q.CreateImportedField` per
   `IrField` and one `q.CreateImportedTemplate` per `IrTemplate`, both in `Ordinal` order.
   `NoteTypesCreated++`.
3. On a hit: **reuse without restructuring.** Read `q.ListFieldsForNoteType`; if its length
   differs from `len(IrNoteType.Fields)`, abort with `ErrNoteTypeMismatch` wrapped with the
   name — `notes.fields` is a positional array indexed by `fields.ordinal`, so importing into a
   note type of a different width renders every field into the wrong slot (docs/schema.md). If
   the count matches, reuse as-is: fields and templates are not renamed, added, or reordered
   (the same rule `db.ErrNoteTypeStructureLocked` encodes for the CRUD path).
   `NoteTypesReused++`.

Build `noteTypeByAnkiID map[int64]pgtype.UUID` and, per note type,
`templateByOrdinal map[int32]pgtype.UUID` (from `q.ListTemplatesForNoteType`) plus its
`IsCloze` flag.

**Step 3 — notes.** For each `IrNote` (skip, with a warning, any whose `NoteTypeAnkiID` or
`HomeDeckAnkiID` does not resolve):

1. `q.GetNoteByOwnerAndGuid(ownerID, guid)` — **check-then-write, not a blind upsert.** This
   matches the existing `internal/db` pattern (`GetDeckForContentEdit` then act) and it is what
   lets the two branches differ: the insert path sets `deck_id`, the update path deliberately
   does not.
2. `pgx.ErrNoRows` → `q.CreateImportedNote` with `DeckID = deckByAnkiID[HomeDeckAnkiID]`,
   `Fields = json.Marshal(ir.Fields)`, `Tags`, `Checksum`, `CreatedAt`, `ModifiedAt`, `AnkiID`.
   `NotesInserted++`.
3. Hit → `q.UpdateImportedNote` (fields, tags, checksum, note_type_id, modified_at, anki_id).
   `deck_id` is untouched. Before it, re-check the field count against the note type's, same as
   step 2.3. `NotesUpdated++`.

Build `noteByAnkiID map[int64]pgtype.UUID`.

**Step 4 — cards.** For each `IrCard`, in ascending `AnkiID`:

1. Resolve `note_id` from `noteByAnkiID[NoteAnkiID]`; skip with a warning if absent.
2. Resolve `template_id`: for a **cloze** note type, always the template at ordinal 0 (a cloze
   note type has exactly one template — `db.ErrClozeNoteTypeSingleTemplate` — and the card's
   ordinal is the cloze number, not a template index). For a non-cloze note type, the template
   whose ordinal equals `IrCard.Ordinal`; skip with a warning if there is none.
3. Resolve `deck_id` from **`deckByAnkiID[IrCard.DeckAnkiID]`** — the card's own home deck.
   This line is architecture.md §20's resolution and the reason the issue exists; it must never
   read the note's deck. Fall back to the note's home deck with a warning only when the card's
   own deck id does not resolve (a package referencing a deck it does not define).
4. `q.UpsertImportedCard(note_id, template_id, ordinal, deck_id, anki_id)`. `CardsUpserted++`.

Build `cardByAnkiID map[int64]pgtype.UUID`. **Cards present in the database but absent from the
package are never deleted** — a delete here would cascade `user_card_state` away (docs/schema.md's
card-regeneration trap).

**Step 5 — locks.** Collect every `card_id` that step 6 or 7 will write scheduling state for,
and call `review.LockCards(ctx, q, ownerID, ids)` once, before any of those writes. Ascending
key order is what keeps it deadlock-free against a concurrent `GradeBatch` (architecture.md §6).

**Step 6 — review history.** Group `col.Reviews` by `CardAnkiID`. For each group whose card
resolves, in ascending `AnkiID` (= chronological, since `revlog.id` is the review instant):

- `q.InsertImportedReviewLog` with `Rating = ir.Rating`, `ReviewedAt`, `DurationMs` (NULL when
  `ir.DurationMs <= 0` — never 0 as a stand-in for unknown, per migration 00011),
  `StateBefore` from `reviewKindToState(ir.Kind)` (0 learning → 1 Learning, 1 review → 2 Review,
  2 relearning → 3 Relearning, 3 cram → 2 Review, 4 manual → the row was already dropped),
  `LearningStepsBefore = 0`, `ElapsedDaysBefore = 0`, `ScheduledDaysAfter = max(0, IntervalSeconds/86400)`,
  `ReviewKind = ir.Kind`, `AnkiID = ir.AnkiID`. `stability_before`, `difficulty_before` and
  `fsrs_version` stay NULL — the schema documents exactly this case as "imported history".
  Count rows returned; `ReviewsInserted += n`.

  `ElapsedDaysBefore = 0` is deliberate and must carry a comment: Anki's `lastIvl` is the
  *scheduled* interval before the review, not the elapsed one, and inventing an elapsed count
  from it would be fabricated training data (CLAUDE.md §2.5). The column is an annotation; the
  replay in the next line re-derives everything scheduling actually reads.

- Then `review.ReplayCard(ctx, tx, params, ownerID, cardID)` for that card, where `params` comes
  from `review.EffectiveParams(ctx, q, ownerID, deckID)` for the card's deck. This is
  apkg-format.md's preferred warm-start: replaying the log through the scheduler beats seeding
  from a snapshot. `CardStatesReplayed++`.

**Step 7 — cards with state but no history.** For each card with **zero** imported reviews that
carries scheduling state — defined as `ir.FSRS != nil` || `ir.Type != 0` || `ir.Suspended` ||
`ir.Flag != 0` — call `q.SeedImportedUserCardState`:

| Column | Value |
|---|---|
| `due` | `ir.Due.At` when `Kind == DueAt`; otherwise `now` (the column is `NOT NULL`; a new/positional card has no calendar due) |
| `stability` / `difficulty` | `ir.FSRS.Stability` / `.Difficulty` when present, else `0` |
| `state` | `int16(ir.Type)` — Anki's `type` enum is value-identical to `fsrs.State` (0 new, 1 learning, 2 review, 3 relearning) |
| `reps` / `lapses` | `ir.Reps` / `ir.Lapses` |
| `elapsed_days` | `0` |
| `scheduled_days` | `max(0, ir.IntervalSeconds/86400)` |
| `learning_steps` | `0` |
| `last_review` | `due - IntervalSeconds` when `Kind == DueAt` and `IntervalSeconds > 0`, else NULL |
| `suspended` | `ir.Suspended` |
| `flag` | `ir.Flag` |

`ir.Factor` is **not** written anywhere — SM-2 ease is meaningless under FSRS and must never be
mapped onto difficulty (apkg-format.md). A card with no state at all gets no row: never-seen
cards have no `user_card_state` row by design, and the queue query's `LEFT JOIN` handles them.
`CardStatesSeeded++`.

**Step 8 — media.** Not written. `MediaDeferred = len(col.Media)`, plus one warning when
non-zero: `"media: N files present in the package were not imported (#60)"`. `media_refs.sha256`
has a `RESTRICT` FK to `media_blobs`, and blobs cannot be written until the content-addressed
store exists.

---

## 7. Synthetic fixture test helpers

New file `internal/apkg/synthetic_test.go` (test-only, same package — the builders are test
helper code, not a committed binary and not a standalone script, per
`tests/fixtures/apkg/README.md`). It builds packages **in memory** by creating a SQLite file in
`t.TempDir()` with `modernc.org/sqlite`, reading it back into bytes, and zipping it.

Shared spec type, so schema-11 and schema-18 builders produce **the same logical collection**
and a test can assert both converge on an equal IR:

```go
// synthSpec describes one logical collection independently of the schema it is written in.
type synthSpec struct {
	Crt       time.Time
	NoteTypes []IrNoteType // reused deliberately: the IR is the spec
	Decks     []IrDeck
	Notes     []IrNote
	Cards     []IrCard
	Reviews   []IrReview
	Media     []IrMedia
	// FieldOrderShuffled writes schema 11's flds/tmpls JSON arrays in an order that DISAGREES
	// with their ord values (reversed), the adversarial case in tests/fixtures/apkg/README.md.
	FieldOrderShuffled bool
}

// defaultSynthSpec is the baseline collection every builder starts from: two decks
// ("Default" and "Default::Sub"), one 3-field / 2-template non-cloze note type plus one cloze
// note type, three notes, five cards spanning BOTH decks (so the per-card deck_id rule has
// something to get wrong), and two revlog rows on one card.
func defaultSynthSpec(t *testing.T) synthSpec

// buildSchema11Package writes spec as a schema-11 collection (col.models / col.decks JSON blobs)
// in a legacy zip container, member name "collection.anki21", uncompressed media.
func buildSchema11Package(t *testing.T, spec synthSpec) []byte

// buildSchema18Package writes spec as a schema-18 collection (notetypes/fields/templates/decks
// tables, protobuf config columns encoded with the field numbers in ankischema.go, deck names
// \x1f-separated) in a legacy zip container.
func buildSchema18Package(t *testing.T, spec synthSpec) []byte

// buildZstdPackage re-wraps pkg's collection and media members as zstd frames and adds a two-byte
// protobuf "meta" member, producing the modern container from any builder's output.
func buildZstdPackage(t *testing.T, pkg []byte) []byte

// buildDowngradeStubPackage produces the real-world trap from apkg-format.md: a package holding
// BOTH collection.anki21 (the real collection, defaultSynthSpec) and collection.anki2 (a
// one-note "please upgrade" placeholder). Reading the wrong member imports one note and reports
// success.
func buildDowngradeStubPackage(t *testing.T) []byte

// buildOutOfOrderOrdPackage is defaultSynthSpec with FieldOrderShuffled set: the note type's
// flds and tmpls arrays are written in reverse-ord order while notes.flds stays indexed by ord.
// A reader keying on array index maps every field value onto the wrong field name.
func buildOutOfOrderOrdPackage(t *testing.T) []byte

// buildFilteredDeckPackage is defaultSynthSpec plus a filtered deck (dyn = 1) holding one card
// whose did is the filtered deck, odid its real home deck, due = -12345 and odue = 7. The two
// values are far enough apart, and of opposite sign, that reading the wrong column cannot pass
// by coincidence (tests/fixtures/apkg/README.md).
func buildFilteredDeckPackage(t *testing.T) []byte

// buildOversizePackage returns a package whose media member decompresses to more than the given
// limit, for the ceiling tests. declaredOnly writes a zstd frame whose declared
// Frame_Content_Size exceeds the limit while the actual payload is small, exercising the
// before-decompression gate specifically.
func buildOversizePackage(t *testing.T, declaredOnly bool) []byte
```

Internal helpers in the same file: `writeSQLite(t, stmts []string) []byte`,
`zipMembers(t, map[string][]byte) []byte`, `encodeProtoString(num uint32, s string) []byte`,
`encodeProtoVarint(num uint32, v uint64) []byte` (the encoder mirrors `protobuf.go`'s decoder;
it lives in the test file so no production code exists only for tests).

---

## 8. Test plan

All tests are colocated with the source (architecture.md §4). DB-backed tests follow the
existing pattern exactly: `testPool(t)` skipping on an unset `DATABASE_URL`, `beginTx(t)` with a
rollback `t.Cleanup` (copy the shape from `internal/review/dbtest_test.go`).

CLAUDE.md §10's exception applies here without qualification: **this area always ships tests.**

### `internal/apkg/read_test.go`

| Test | Asserts |
|---|---|
| `TestRead_Schema11_MatchesSpec` | Reading `buildSchema11Package(defaultSynthSpec)` yields an `IrCollection` field-for-field equal to the spec (decks, note types with ordered fields/templates, notes with split fields/tags, cards, reviews). |
| `TestRead_Schema18_MatchesSpec` | Same for `buildSchema18Package`, including `\x1f` deck names normalised to `::`. |
| `TestRead_SchemasConverge` | The two readers produce **equal** IRs for the same spec. This is the whole point of the IR sitting in the middle, and it is the test that catches the two readers silently disagreeing. |
| `TestRead_OutOfOrderOrdArrays` | `buildOutOfOrderOrdPackage` still maps each field value to the field name its `ord` designates — i.e. the IR is identical to the in-order package's. Regression for apkg-format.md's "`ord` is authoritative, array order is not". |
| `TestRead_FilteredDeckUsesOdueAndOdid` | The filtered-deck card's `DeckAnkiID` is the **home** deck (`odid`), `FilteredDeckAnkiID` is the filtered deck, and its `Due` derives from `odue` (7 days after `crt`), not from `due` (-12345). |
| `TestRead_DueDiscriminatedByQueue` | Table-driven over apkg-format.md's `queue`/`due` table, including a `type = 2` card sitting in `queue = 3` and a `type = 3` card in `queue = 1`, plus the three negative-queue hold cases. Asserts `type` is never the discriminator. |
| `TestRead_NegativeIntervalIsSeconds` | `ivl = -600` → `IntervalSeconds == 600`, `ivl = 3` → `259200`; same for `revlog.ivl` and `revlog.lastIvl`. |
| `TestRead_TagsAndFieldSplitting` | Space-surrounded tags yield no empty entries; `\x1f`-joined fields including empty middle fields yield exactly the note type's field count. |
| `TestRead_CardDataFSRSAndGarbage` | Valid `{"s":..,"d":..}` populates `FSRS`; `{}`, `"not json"` and an absent column all leave it nil with no error. |
| `TestRead_NoRevlogTableIsNormal` | A package with no `revlog` table reads successfully with zero reviews (the common `.apkg` case). |
| `TestRead_HomeDeckIsLowestNumberedCard` | `IrNote.HomeDeckAnkiID` is the home deck of the note's lowest-id card when the note's cards span two decks. |
| `TestRead_TwiceIsIdentical` | Reading the same bytes twice produces an identical IR (`reflect.DeepEqual`) — the property the one real export confirmed. |

### `internal/apkg/container_test.go`

| Test | Asserts |
|---|---|
| `TestRead_PrefersNewestCollectionMember` | `buildDowngradeStubPackage` reads the `collection.anki21` deck (3 notes), not the 1-note `collection.anki2` stub. |
| `TestRead_ZstdContainer` | `buildZstdPackage(buildSchema11Package(spec))` yields the same IR as its uncompressed input — detection is by bytes, so the two paths converge. |
| `TestRead_MemberCountCeiling` | A package with `MaxMembers+1` members → `ErrTooManyMembers`, and no member was opened. |
| `TestRead_MemberSizeCeiling` | A member larger than `MaxMemberBytes` → `ErrMemberTooLarge`, including the case where the zip header **understates** the size. |
| `TestRead_TotalSizeCeiling` | Many individually-legal members exceeding `MaxTotalBytes` → `ErrArchiveTooLarge`. |
| `TestRead_ZstdDeclaredSizeRejectedBeforeDecompressing` | `buildOversizePackage(t, true)` → `ErrMemberTooLarge`, and `zstdDeclaredSize` is what rejected it (assert via a small payload that would have decompressed fine). |
| `TestZstdDeclaredSize_HeaderForms` | Table-driven over the four `FCS_Field_Size` codes, single-segment set and clear, with and without a dictionary id; a truncated header → `ErrBadZstdFrame`. |
| `TestRead_MediaIndexJSONAndProtobuf` | Both spellings yield the same index→filename map; the JSON/protobuf choice is sniffed from the first byte. |
| `TestRead_MediaFilenameNFCAndCollision` | An NFD filename normalises to NFC; two entries colliding after normalisation keep the first and produce exactly one warning; identical bytes under one name produce none. |
| `TestRead_MediaBytesNeverZstdSniffed` | A media file whose first four bytes are the zstd magic survives byte-identical. |

### `internal/apkg/dbwrite_test.go` (DB-backed)

| Test | Asserts |
|---|---|
| `TestImport_FilesCardDeckFromCardsOwnDeck` | **The headline test for this issue.** A note whose cards span two decks: every `cards.deck_id` matches its `IrCard.DeckAnkiID`, and `notes.deck_id` is the lowest-numbered card's deck. Fails if the writer ever flattens to the note's deck (architecture.md §20). |
| `TestImport_IdempotentOnOwnerGuid` | Importing the same IR twice leaves the same note count, the same note ids, the same card ids, and the same `review_log` row count. |
| `TestImport_ReimportPreservesCardIDsAndState` | Grade a card between two imports; after the second import the card's id is unchanged and its `user_card_state` row still exists (the trap docs/schema.md names). |
| `TestImport_ReimportDoesNotMoveNotes` | A note moved to another deck between imports stays there. |
| `TestImport_ReusesDeckAndNoteTypeByName` | A second import of a package whose deck/note-type names already exist creates no duplicates; `DecksReused`/`NoteTypesReused` reflect it. |
| `TestImport_RejectsNoteTypeFieldCountMismatch` | An existing note type of a different width → `ErrNoteTypeMismatch`, and nothing was written (assert via the rolled-back tx's counts before the error). |
| `TestImport_RevlogBecomesReviewLogAndReplays` | Each imported review lands one `review_log` row with `anki_id` set and `stability_before`/`fsrs_version` NULL; the card's `user_card_state` matches what `review.ReplayCard` produces for the same history. |
| `TestImport_SeedsStateOnlyWhenCardHasState` | A never-studied, unsuspended, unflagged card gets **no** `user_card_state` row; a suspended one and an FSRS-carrying one each get exactly one. |
| `TestImport_NeverWritesFactor` | No column anywhere receives `IrCard.Factor` (assert `difficulty` is 0 for an SM-2-only card with a high `factor`). |
| `TestImport_MediaDeferred` | `MediaDeferred == len(col.Media)`, no `media_blobs`/`media_refs` rows written, one warning. |
| `TestImport_FilteredDeckNotCreated` | The filtered deck produces no `decks` row; its card is filed in the home deck. |

### Round-trip (CLAUDE.md §10.3) — scoped down, deliberately

The full `import(export(import(f))) == import(f)` round trip **cannot be written in this issue**:
the export half is `write.go`, which is [#59](https://github.com/Jolls/enshu/issues/59). What
#58 ships instead, and what #59 must extend:

- `TestRead_TwiceIsIdentical` — the import determinism half of the property.
- `TestRead_SchemasConverge` — two independent readers agreeing on one IR, which is the
  strongest convergence check available without a writer.
- `TestImport_IdempotentOnOwnerGuid` — the database half: importing twice converges.

Add a `// Round-trip: the export half is #59; this file covers import determinism only.` comment
at the top of `read_test.go` so the gap is visible rather than looking like an oversight.

Real fixtures stay absent, per `tests/fixtures/apkg/README.md` — collecting them is a human task
and #62 is where they get exercised end to end. Do not add anything to
`tests/fixtures/apkg/` in this issue.

---

## 9. Dependencies to add

```
go get modernc.org/sqlite
go get github.com/klauspost/compress/zstd
go get golang.org/x/text/unicode/norm
```

All three are the choices architecture.md §3's stack table already names (`modernc.org/sqlite`
pure-Go driver, stdlib `archive/zip`, `klauspost/compress/zstd`); `golang.org/x/text` is already
in the module graph as an indirect dependency and is promoted to direct. **No protobuf library**
— `protobuf.go` decodes the wire format by hand (§2.6), which avoids both a `.proto` toolchain
and any question about where a schema came from (CLAUDE.md §2.8).

Run `go mod tidy` and commit `go.mod`/`go.sum`.

---

## 10. Open questions — resolved

All five resolved by the user before implementation. Each subsection below replaces the
corresponding open question with the exact change the resolution makes to the plan above.

### 10.1 Schema-18 protobuf field numbers → fail loudly (as planned in §5.2)

**Decision:** do not source the field numbers from any third party. Ship the constant table in
§5.2 with placeholder values (each marked `// ❓ unverified — #61`, no provenance claimed), and
`readSchema18` / `validateSchema18Decode` return `ErrSchema18Config` unconditionally until #61
verifies the real numbers against a real export. Schema-18 packages fail import cleanly rather
than risk silently-wrong decode. `TestRead_Schema18_MatchesSpec` and `TestRead_SchemasConverge`
(§8) are written against the synthetic builder using the same placeholder numbers `ankischema.go`
declares, so they exercise the *decode plumbing* (protobuf wire walking, `validateSchema18Decode`
gating) but do not — and cannot — assert real-world correctness; #61 closes that gap. No other
part of the plan changes: schema 11 remains fully functional import, which is the common case
("Support older Anki versions" checked) most users will hit anyway.

### 10.2 Archive ceiling literals → tighter limits

**Decision:** replace §4's `DefaultArchiveLimits()` literals with:

```go
func DefaultArchiveLimits() ArchiveLimits {
	return ArchiveLimits{
		MaxMembers:     5_000,      // the one real export inspected had ~550 members
		MaxMemberBytes: 100 << 20,  // 100 MiB per member
		MaxTotalBytes:  500 << 20,  // 500 MiB across everything read
	}
}
```

Rationale comment to use in place of §4's: generous headroom over the one real export (546 media
files, largest single member well under 100 MiB) while bounding worst-case memory more tightly on
a typical self-hosted box than the originally-proposed 50k/512MiB/2GiB. All ceiling tests in §8
(`TestRead_MemberCountCeiling`, `TestRead_MemberSizeCeiling`, `TestRead_TotalSizeCeiling`,
`TestRead_ZstdDeclaredSizeRejectedBeforeDecompressing`) use these tighter numbers.

### 10.3 Transaction scope → one transaction (as planned in §6)

**Decision:** no change. `Import` runs entirely inside the caller's single transaction, as
written in §6's opening paragraph.

### 10.4 `review_log.review_kind` for cram rows → import verbatim (as planned in §6, Step 6)

**Decision:** no change. `revlog.type == 3` (cram) is written to `review_kind` verbatim, as
already specified in Step 6's `reviewKindToState` mapping and the `ReviewKind = ir.Kind` line.

### 10.5 Deck presets → out of scope (as planned in §5.1)

**Decision:** no change. `col.dconf` / `deck_config` stay unread; `decks.preset` keeps its
`'{}'` default. §5.1's "`dconf` / `conf` are not read" stands as written.
