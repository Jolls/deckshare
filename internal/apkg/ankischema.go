package apkg

// Anki's SQLite shapes and constants. No behaviour beyond decoding the JSON/protobuf blobs.

// Schema-11 queries. col.models / col.decks are JSON objects keyed by id-as-string.
const (
	sqlSelectCol11 = "SELECT crt, models, decks FROM col LIMIT 1"
)

// Schema-18 queries. Ordered by integer id only -- never ORDER BY name (schema-18 name columns
// declare COLLATE unicase and the driver may not be able to register it).
const (
	sqlSelectNotetypes18 = "SELECT id, name, config FROM notetypes ORDER BY id"
	sqlSelectFields18    = "SELECT ntid, ord, name, config FROM fields ORDER BY ntid, ord"
	sqlSelectTemplates18 = "SELECT ntid, ord, name, config FROM templates ORDER BY ntid, ord"
	sqlSelectDecks18     = "SELECT id, name, common, kind FROM decks ORDER BY id"
)

// Shared by both schemas.
const (
	sqlSelectNotes  = "SELECT id, guid, mid, mod, tags, flds, csum FROM notes ORDER BY id"
	sqlSelectCards  = "SELECT id, nid, did, ord, type, queue, due, ivl, factor, reps, lapses, odue, odid, flags, data FROM cards ORDER BY id"
	sqlSelectRevlog = "SELECT id, cid, ease, ivl, lastIvl, factor, time, type FROM revlog ORDER BY id"
)

// ankiModel11 is one entry of col.models (schema 11), keyed by id-as-string in the JSON object.
type ankiModel11 struct {
	ID    int64            `json:"id"`
	Name  string           `json:"name"`
	Type  int              `json:"type"` // 0 standard, 1 cloze
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

// ankiDeck11 is one entry of col.decks (schema 11), keyed by id-as-string in the JSON object.
type ankiDeck11 struct {
	ID   int64  `json:"id"`
	Name string `json:"name"` // "::"-separated
	Desc string `json:"desc"`
	Dyn  int    `json:"dyn"` // 1 = filtered
}

// Anki cards.queue values.
const (
	ankiQueueNew         = 0
	ankiQueueLearning    = 1
	ankiQueueReview      = 2
	ankiQueueDayLearning = 3
	ankiQueuePreview     = 4
	ankiQueueSuspended   = -1
	ankiQueueSchedBuried = -2
	ankiQueueUserBuried  = -3
)

// Anki cards.type values.
const (
	ankiTypeNew        = 0
	ankiTypeLearning   = 1
	ankiTypeReview     = 2
	ankiTypeRelearning = 3
)

const ankiFlagMask = 0x7

// epochSecondsThreshold is apkg-format.md's heuristic for disambiguating a held card's `due`:
// a value at or above this magnitude is an epoch-seconds instant, not a day count since crt.
const epochSecondsThreshold = 1_000_000_000

// ankiCardData is cards.data, a JSON object carrying FSRS state. Pointers so "absent" and "zero"
// are distinguishable. An unparseable or unrecognised data is treated as absent, never an error.
type ankiCardData struct {
	Pos *int32   `json:"pos"`
	S   *float64 `json:"s"`
	D   *float64 `json:"d"`
	DR  *float64 `json:"dr"`
}

// Schema-18 protobuf field numbers. Verified 2026-08-15 (#61) against two real schema-18
// exports (tests/fixtures/apkg/mathematics-schema18.apkg): a Basic-family note type and a real
// Cloze note type with content, decoded byte-for-byte against the raw config blobs. Each
// constant below is either ✅ confirmed against those bytes or ❓ still unverified -- see
// docs/apkg-format.md's schema-18 section for the decode walkthrough. Two of the original
// placeholder guesses (fieldConfigFontField, fieldConfigSizeField) turned out wrong: the real
// wire bytes have font/size at field numbers 3/4, not 1/2.
const (
	ntConfigKindField      uint32 = 1 // ✅ 0 standard, 1 cloze -- confirmed against a real Cloze notetypes.config
	ntConfigSortFieldField uint32 = 2 // ❓ unverified -- no note type in the real export has a non-default sort field
	ntConfigCSSField       uint32 = 3 // ✅ confirmed against real CSS bytes
	fieldConfigFontField   uint32 = 3 // ✅ confirmed ("Arial") -- corrected from an earlier guess of 1
	fieldConfigSizeField   uint32 = 4 // ✅ confirmed (20) -- corrected from an earlier guess of 2
	tmplConfigQFmtField    uint32 = 1 // ✅ confirmed against real template HTML
	tmplConfigAFmtField    uint32 = 2 // ✅ confirmed against real template HTML
	deckKindNormalField    uint32 = 1 // ✅ confirmed: wraps a nested message ({1: deck_config id}) for a non-filtered deck.
	mediaEntryField        uint32 = 1 // ✅ confirmed against real media filenames
	mediaEntryNameField    uint32 = 1 // ✅ confirmed against real media filenames

	// Deliberately NOT decoded in read.go, and no constants declared for them: RTL/sticky field
	// flags, browser-specific template overrides (bqfmt/bafmt), a deck's description, and a
	// filtered deck's own `kind` oneof variant are all still ❓ -- no note/field/deck in either
	// real export exercises a non-default value for them. Guessing a field number here risks
	// silently mis-decoding a real collection, which is the exact failure mode #61 exists to
	// rule out; these properties default to their zero value (false / "") until a fixture
	// exercises them. IsFiltered is still derived correctly without knowing the Filtered variant's
	// field number: it's the negation of deckKindNormalField's presence (deck kind is a oneof, so
	// "not Normal" means "Filtered" is the only other variant Anki has).
)
