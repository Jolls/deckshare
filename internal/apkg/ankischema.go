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

// Schema-18 protobuf field numbers. ALL ❓ UNVERIFIED against a real export -- #61 exists to
// close this, and tests/fixtures/apkg/README.md explains why a synthetic schema-18 fixture
// cannot. Per the resolved decision in docs/plans/58-apkg-import.md §10.1, no third-party
// provenance is claimed for these placeholder values: readSchema18 always fails with
// ErrSchema18Config until #61 verifies the real numbers against a real export.
const (
	ntConfigKindField      uint32 = 1 // 0 standard, 1 cloze
	ntConfigSortFieldField uint32 = 2
	ntConfigCSSField       uint32 = 3
	fieldConfigFontField   uint32 = 1
	fieldConfigSizeField   uint32 = 2
	fieldConfigRTLField    uint32 = 3
	fieldConfigStickyField uint32 = 4
	tmplConfigQFmtField    uint32 = 1
	tmplConfigAFmtField    uint32 = 2
	tmplConfigBQFmtField   uint32 = 3
	tmplConfigBAFmtField   uint32 = 4
	deckCommonDescField    uint32 = 1
	deckKindFilteredField  uint32 = 1
	mediaEntryField        uint32 = 1
	mediaEntryNameField    uint32 = 1
)
