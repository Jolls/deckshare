package apkg

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Synthetic package builders for the .apkg reader tests, per tests/fixtures/apkg/README.md's
// "Synthetic fixtures" section: no real fixture exists for schema 18 yet (#61), so these build
// packages entirely in memory rather than committing binary files or a standalone script.
//
// Deliberate simplification from docs/plans/58-apkg-import.md §7: rather than a bidirectional
// IR<->raw mapper, the spec types here hold Anki's OWN raw column shapes (due/odue/odid/ivl as
// SQLite would store them). expectedIR derives the IR a correct reader must produce by calling
// this package's own resolveDue/intervalSeconds -- the same functions read.go calls -- so the
// convergence tests verify the SQL/JSON/protobuf encoding and container plumbing are correct,
// while the dedicated due/interval tests assert resolveDue/intervalSeconds's formulas directly
// against hand-computed values.

type synthNoteType struct {
	AnkiID    int64
	Name      string
	CSS       string
	IsCloze   bool
	SortIdx   int32
	Fields    []IrField
	Templates []IrTemplate
}

type synthDeck struct {
	AnkiID int64
	Name   string
	Desc   string
	Dyn    int
}

type synthNote struct {
	AnkiID         int64
	Guid           string
	NoteTypeAnkiID int64
	Fields         []string
	Tags           []string
	Csum           int64
	Mod            int64 // epoch seconds
}

type synthCard struct {
	AnkiID     int64
	NoteAnkiID int64
	Did        int64
	Odid       int64
	Ord        int32
	Type       int32
	Queue      int32
	Due        int64
	Odue       int64
	Ivl        int64
	Factor     int32
	Reps       int32
	Lapses     int32
	Flags      int32
	Data       string // raw cards.data JSON, "" for none
}

type synthRevlog struct {
	AnkiID     int64
	CardAnkiID int64
	Ease       int32
	Ivl        int64
	LastIvl    int64
	Factor     int32
	Time       int32
	Type       int32
}

// synthSpec describes one logical collection independently of the schema it is written in.
type synthSpec struct {
	Crt       time.Time
	NoteTypes []synthNoteType
	Decks     []synthDeck
	Notes     []synthNote
	Cards     []synthCard
	Revlog    []synthRevlog
	// FieldOrderShuffled writes schema 11's flds/tmpls JSON arrays in an order that DISAGREES
	// with their ord values (reversed), the adversarial case in tests/fixtures/apkg/README.md.
	FieldOrderShuffled bool
}

// defaultSynthSpec is the baseline collection every builder starts from: two decks ("Default"
// and "Default::Sub"), one 3-field/2-template non-cloze note type plus one cloze note type,
// three notes, five cards spanning BOTH decks, and two revlog rows on one card.
func defaultSynthSpec(t *testing.T) synthSpec {
	t.Helper()
	crt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return synthSpec{
		Crt: crt,
		Decks: []synthDeck{
			{AnkiID: 1, Name: "Default"},
			{AnkiID: 2, Name: "Default::Sub"},
		},
		NoteTypes: []synthNoteType{
			{
				AnkiID:  1001,
				Name:    "Basic",
				CSS:     ".card { color: black; }",
				SortIdx: 0,
				Fields: []IrField{
					{Ordinal: 0, Name: "Front", Font: "Arial", Size: 20},
					{Ordinal: 1, Name: "Back", Font: "Arial", Size: 20},
					{Ordinal: 2, Name: "Extra", Font: "Arial", Size: 20},
				},
				Templates: []IrTemplate{
					{Ordinal: 0, Name: "Card 1", Qfmt: "{{Front}}", Afmt: "{{Back}}"},
					{Ordinal: 1, Name: "Card 2", Qfmt: "{{Back}}", Afmt: "{{Front}}"},
				},
			},
			{
				AnkiID:  1002,
				Name:    "Cloze",
				CSS:     ".cloze { color: blue; }",
				IsCloze: true,
				SortIdx: 0,
				Fields: []IrField{
					{Ordinal: 0, Name: "Text", Font: "Arial", Size: 20},
				},
				Templates: []IrTemplate{
					{Ordinal: 0, Name: "Cloze", Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"},
				},
			},
		},
		Notes: []synthNote{
			{AnkiID: 101, Guid: "guid-note-1", NoteTypeAnkiID: 1001, Fields: []string{"front1", "back1", ""}, Tags: []string{"tag1"}, Csum: 111, Mod: crt.Unix() + 10},
			{AnkiID: 102, Guid: "guid-note-2", NoteTypeAnkiID: 1001, Fields: []string{"front2", "back2", "extra2"}, Tags: nil, Csum: 222, Mod: crt.Unix() + 20},
			{AnkiID: 103, Guid: "guid-note-3", NoteTypeAnkiID: 1002, Fields: []string{"{{c1::cloze text}}"}, Tags: []string{"tag2", "tag3"}, Csum: 333, Mod: crt.Unix() + 30},
		},
		Cards: []synthCard{
			{AnkiID: 201, NoteAnkiID: 101, Did: 1, Ord: 0, Type: ankiTypeReview, Queue: ankiQueueReview, Due: 5, Ivl: 3, Factor: 2500, Reps: 2, Lapses: 0},
			{AnkiID: 202, NoteAnkiID: 101, Did: 2, Ord: 1, Type: ankiTypeNew, Queue: ankiQueueNew, Due: 7},
			{AnkiID: 203, NoteAnkiID: 102, Did: 2, Ord: 0, Type: ankiTypeNew, Queue: ankiQueueNew, Due: 1},
			{AnkiID: 204, NoteAnkiID: 102, Did: 1, Ord: 1, Type: ankiTypeNew, Queue: ankiQueueNew, Due: 2},
			{AnkiID: 205, NoteAnkiID: 103, Did: 1, Ord: 0, Type: ankiTypeNew, Queue: ankiQueueNew, Due: 3},
		},
		Revlog: []synthRevlog{
			{AnkiID: 301, CardAnkiID: 201, Ease: 3, Ivl: 3, LastIvl: 1, Factor: 2500, Time: 5000, Type: 1},
			{AnkiID: 302, CardAnkiID: 201, Ease: 4, Ivl: 6, LastIvl: 3, Factor: 2600, Time: 4000, Type: 1},
		},
	}
}

// expectedIR derives the IR a correct reader must produce from spec, using this package's own
// resolveDue/intervalSeconds/resolveHomeDecks.
func expectedIR(spec synthSpec, schemaVersion int) *IrCollection {
	noteTypes := make([]IrNoteType, 0, len(spec.NoteTypes))
	for _, nt := range spec.NoteTypes {
		fields := append([]IrField(nil), nt.Fields...)
		templates := append([]IrTemplate(nil), nt.Templates...)
		sortFieldsByOrdinal(fields)
		sortTemplatesByOrdinal(templates)
		noteTypes = append(noteTypes, IrNoteType{
			AnkiID: nt.AnkiID, Name: nt.Name, CSS: nt.CSS, IsCloze: nt.IsCloze,
			SortFieldIdx: nt.SortIdx, Fields: fields, Templates: templates,
		})
	}

	decks := make([]IrDeck, 0, len(spec.Decks))
	for _, d := range spec.Decks {
		decks = append(decks, IrDeck{AnkiID: d.AnkiID, Name: d.Name, Description: d.Desc, IsFiltered: d.Dyn != 0})
	}

	notes := make([]IrNote, 0, len(spec.Notes))
	for _, n := range spec.Notes {
		fields := make([]string, len(n.Fields))
		copy(fields, n.Fields)
		// splitTags never returns nil (even for zero tags), so mirror that here rather than
		// append([]string(nil), ...), which WOULD stay nil for an empty n.Tags and make this
		// spec disagree with the reader's actual (non-nil) output under reflect.DeepEqual.
		tags := make([]string, len(n.Tags))
		copy(tags, n.Tags)
		notes = append(notes, IrNote{
			AnkiID: n.AnkiID, Guid: n.Guid, NoteTypeAnkiID: n.NoteTypeAnkiID,
			Fields: fields, Tags: tags,
			Checksum: n.Csum, Created: time.UnixMilli(n.AnkiID).UTC(), Modified: time.Unix(n.Mod, 0).UTC(),
		})
	}

	cards := make([]IrCard, 0, len(spec.Cards))
	for _, c := range spec.Cards {
		deckAnkiID := c.Did
		filteredDeckAnkiID := int64(0)
		if c.Odid != 0 {
			deckAnkiID = c.Odid
			filteredDeckAnkiID = c.Did
		}
		var fsrs *IrFSRSState
		if c.Data != "" {
			var cd ankiCardData
			if json.Unmarshal([]byte(c.Data), &cd) == nil && cd.S != nil && cd.D != nil {
				fs := IrFSRSState{Stability: *cd.S, Difficulty: *cd.D}
				if cd.DR != nil {
					fs.DesiredRetention = *cd.DR
				}
				if cd.Pos != nil {
					fs.Position = *cd.Pos
				}
				fsrs = &fs
			}
		}
		cards = append(cards, IrCard{
			AnkiID: c.AnkiID, NoteAnkiID: c.NoteAnkiID, DeckAnkiID: deckAnkiID, FilteredDeckAnkiID: filteredDeckAnkiID,
			Ordinal: c.Ord, Type: c.Type, Queue: c.Queue,
			Due:             resolveDue(c.Queue, c.Type, c.Due, c.Odue, c.Odid, spec.Crt),
			IntervalSeconds: intervalSeconds(c.Ivl),
			Factor:          c.Factor, Reps: c.Reps, Lapses: c.Lapses,
			Flag:      int16(c.Flags & ankiFlagMask),
			Suspended: c.Queue == ankiQueueSuspended,
			Buried:    c.Queue == ankiQueueSchedBuried || c.Queue == ankiQueueUserBuried,
			FSRS:      fsrs,
		})
	}

	var reviews []IrReview
	var warnings []string
	for _, r := range spec.Revlog {
		if r.Ease == 0 {
			continue
		}
		reviews = append(reviews, IrReview{
			AnkiID: r.AnkiID, CardAnkiID: r.CardAnkiID, ReviewedAt: time.UnixMilli(r.AnkiID).UTC(),
			Rating: int16(r.Ease), IntervalSeconds: intervalSeconds(r.Ivl), LastIntervalSeconds: intervalSeconds(r.LastIvl),
			Factor: r.Factor, DurationMs: r.Time, Kind: int16(r.Type),
		})
	}

	homeDeckWarnings := resolveHomeDecks(notes, cards)
	warnings = append(warnings, homeDeckWarnings...)

	return &IrCollection{
		Crt: spec.Crt, SchemaVersion: schemaVersion, NoteTypes: noteTypes, Decks: decks,
		Notes: notes, Cards: cards, Reviews: reviews, Warnings: warnings,
	}
}

// writeSQLite creates a fresh SQLite file in t.TempDir(), executes ddl then insert, and returns
// the file's bytes.
func writeSQLite(t *testing.T, ddl []string, insert func(dbh *sql.DB) error) []byte {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "collection.anki2")
	dbh, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p))
	if err != nil {
		t.Fatalf("opening synthetic sqlite file: %v", err)
	}
	for _, stmt := range ddl {
		if _, err := dbh.Exec(stmt); err != nil {
			closeQuietly(dbh)
			t.Fatalf("executing DDL %q: %v", stmt, err)
		}
	}
	if err := insert(dbh); err != nil {
		closeQuietly(dbh)
		t.Fatalf("inserting synthetic data: %v", err)
	}
	if err := dbh.Close(); err != nil {
		t.Fatalf("closing synthetic sqlite file: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading synthetic sqlite file: %v", err)
	}
	return b
}

// zipMembers builds a zip archive (stored, uncompressed) from the given member name -> bytes map.
func zipMembers(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range members {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatalf("creating zip member %q: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("writing zip member %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}
	return buf.Bytes()
}

func insertNotesCardsRevlog(dbh *sql.DB, spec synthSpec) error {
	for _, n := range spec.Notes {
		flds := ""
		for i, f := range n.Fields {
			if i > 0 {
				flds += "\x1f"
			}
			flds += f
		}
		tags := ""
		for _, tg := range n.Tags {
			tags += " " + tg
		}
		if tags != "" {
			tags += " "
		}
		if _, err := dbh.Exec("INSERT INTO notes (id, guid, mid, mod, tags, flds, csum) VALUES (?,?,?,?,?,?,?)",
			n.AnkiID, n.Guid, n.NoteTypeAnkiID, n.Mod, tags, flds, n.Csum); err != nil {
			return err
		}
	}
	for _, c := range spec.Cards {
		data := c.Data
		if data == "" {
			data = "{}"
		}
		if _, err := dbh.Exec("INSERT INTO cards (id, nid, did, ord, type, queue, due, ivl, factor, reps, lapses, odue, odid, flags, data) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			c.AnkiID, c.NoteAnkiID, c.Did, c.Ord, c.Type, c.Queue, c.Due, c.Ivl, c.Factor, c.Reps, c.Lapses, c.Odue, c.Odid, c.Flags, data); err != nil {
			return err
		}
	}
	if len(spec.Revlog) > 0 {
		if _, err := dbh.Exec("CREATE TABLE revlog (id integer primary key, cid integer, ease integer, ivl integer, lastIvl integer, factor integer, time integer, type integer)"); err != nil {
			return err
		}
		for _, r := range spec.Revlog {
			if _, err := dbh.Exec("INSERT INTO revlog (id, cid, ease, ivl, lastIvl, factor, time, type) VALUES (?,?,?,?,?,?,?,?)",
				r.AnkiID, r.CardAnkiID, r.Ease, r.Ivl, r.LastIvl, r.Factor, r.Time, r.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

const sharedTableDDL = `
CREATE TABLE notes (id integer primary key, guid text, mid integer, mod integer, tags text, flds text, csum integer);
CREATE TABLE cards (id integer primary key, nid integer, did integer, ord integer, type integer, queue integer, due integer, ivl integer, factor integer, reps integer, lapses integer, odue integer, odid integer, flags integer, data text);
`

// buildSchema11Package writes spec as a schema-11 collection (col.models / col.decks JSON blobs)
// in a legacy zip container, member name "collection.anki21", uncompressed media.
func buildSchema11Package(t *testing.T, spec synthSpec) []byte {
	t.Helper()

	models := map[string]ankiModel11{}
	for _, nt := range spec.NoteTypes {
		flds := make([]ankiField11, len(nt.Fields))
		for i, f := range nt.Fields {
			flds[i] = ankiField11{Name: f.Name, Ord: f.Ordinal, Font: f.Font, Size: f.Size, RTL: f.IsRTL, Sticky: f.Sticky}
		}
		tmpls := make([]ankiTemplate11, len(nt.Templates))
		for i, tp := range nt.Templates {
			tmpls[i] = ankiTemplate11{Name: tp.Name, Ord: tp.Ordinal, Qfmt: tp.Qfmt, Afmt: tp.Afmt, Bqfmt: tp.BrowserQfmt, Bafmt: tp.BrowserAfmt}
		}
		if spec.FieldOrderShuffled {
			for i, j := 0, len(flds)-1; i < j; i, j = i+1, j-1 {
				flds[i], flds[j] = flds[j], flds[i]
			}
			for i, j := 0, len(tmpls)-1; i < j; i, j = i+1, j-1 {
				tmpls[i], tmpls[j] = tmpls[j], tmpls[i]
			}
		}
		typ := 0
		if nt.IsCloze {
			typ = 1
		}
		models[fmt.Sprintf("%d", nt.AnkiID)] = ankiModel11{
			ID: nt.AnkiID, Name: nt.Name, Type: typ, Sortf: nt.SortIdx, CSS: nt.CSS, Flds: flds, Tmpls: tmpls,
		}
	}
	decks := map[string]ankiDeck11{}
	for _, d := range spec.Decks {
		decks[fmt.Sprintf("%d", d.AnkiID)] = ankiDeck11{ID: d.AnkiID, Name: d.Name, Desc: d.Desc, Dyn: d.Dyn}
	}
	modelsJSON, err := json.Marshal(models)
	if err != nil {
		t.Fatalf("marshalling col.models: %v", err)
	}
	decksJSON, err := json.Marshal(decks)
	if err != nil {
		t.Fatalf("marshalling col.decks: %v", err)
	}

	ddl := []string{
		"CREATE TABLE col (id integer primary key, crt integer, ver integer, models text, decks text)",
		sharedTableDDL,
	}
	collBytes := writeSQLite(t, ddl, func(dbh *sql.DB) error {
		if _, err := dbh.Exec("INSERT INTO col (id, crt, ver, models, decks) VALUES (1,?,11,?,?)", spec.Crt.Unix(), string(modelsJSON), string(decksJSON)); err != nil {
			return err
		}
		return insertNotesCardsRevlog(dbh, spec)
	})

	return zipMembers(t, map[string][]byte{"collection.anki21": collBytes})
}

// buildSchema18Package writes spec as a schema-18 collection (notetypes/fields/templates/decks
// tables, protobuf config columns encoded with the field numbers in ankischema.go, deck names
// \x1f-separated) in a legacy zip container.
func buildSchema18Package(t *testing.T, spec synthSpec) []byte {
	t.Helper()

	ddl := []string{
		"CREATE TABLE col (id integer primary key, crt integer, ver integer)",
		"CREATE TABLE notetypes (id integer primary key, name text, config blob)",
		"CREATE TABLE fields (ntid integer, ord integer, name text, config blob)",
		"CREATE TABLE templates (ntid integer, ord integer, name text, config blob)",
		"CREATE TABLE decks (id integer primary key, name text, common blob, kind blob)",
		sharedTableDDL,
	}
	collBytes := writeSQLite(t, ddl, func(dbh *sql.DB) error {
		if _, err := dbh.Exec("INSERT INTO col (id, crt, ver) VALUES (1,?,18)", spec.Crt.Unix()); err != nil {
			return err
		}
		for _, nt := range spec.NoteTypes {
			kind := uint64(0)
			if nt.IsCloze {
				kind = 1
			}
			config := append(encodeProtoVarint(ntConfigKindField, kind), encodeProtoVarint(ntConfigSortFieldField, uint64(nt.SortIdx))...)
			config = append(config, encodeProtoString(ntConfigCSSField, nt.CSS)...)
			if _, err := dbh.Exec("INSERT INTO notetypes (id, name, config) VALUES (?,?,?)", nt.AnkiID, nt.Name, config); err != nil {
				return err
			}
			for _, f := range nt.Fields {
				// IsRTL/Sticky have no confirmed field number (#61) and read.go doesn't decode
				// them, so defaultSynthSpec never sets them and they're left unencoded here too.
				fc := encodeProtoString(fieldConfigFontField, f.Font)
				fc = append(fc, encodeProtoVarint(fieldConfigSizeField, uint64(f.Size))...)
				if _, err := dbh.Exec("INSERT INTO fields (ntid, ord, name, config) VALUES (?,?,?,?)", nt.AnkiID, f.Ordinal, f.Name, fc); err != nil {
					return err
				}
			}
			for _, tp := range nt.Templates {
				// BrowserQfmt/BrowserAfmt likewise unencoded -- see the field comment above.
				tc := encodeProtoString(tmplConfigQFmtField, tp.Qfmt)
				tc = append(tc, encodeProtoString(tmplConfigAFmtField, tp.Afmt)...)
				if _, err := dbh.Exec("INSERT INTO templates (ntid, ord, name, config) VALUES (?,?,?,?)", nt.AnkiID, tp.Ordinal, tp.Name, tc); err != nil {
					return err
				}
			}
		}
		for _, d := range spec.Decks {
			// decks.common's description field has no confirmed number (#61); defaultSynthSpec
			// never sets Desc, so common is left empty rather than encoding a guess.
			var kind []byte
			if d.Dyn != 0 {
				// The filtered variant's real field number is unverified (#61); field 2 here is
				// a synthetic-only stand-in whose only job is to NOT be deckKindNormalField, so
				// the decoder's "Normal variant absent means filtered" check has something to
				// exercise.
				kind = encodeProtoVarint(2, 1)
			} else {
				// Mirrors the real shape confirmed in #61: field 1 wraps a nested message
				// carrying the deck's config_id. The nested content doesn't matter here, only
				// that field 1 itself is present.
				kind = encodeProtoString(deckKindNormalField, string(encodeProtoVarint(1, 1)))
			}
			name := normaliseDeckNameToSchema18(d.Name)
			if _, err := dbh.Exec("INSERT INTO decks (id, name, common, kind) VALUES (?,?,?,?)", d.AnkiID, name, []byte{}, kind); err != nil {
				return err
			}
		}
		return insertNotesCardsRevlog(dbh, spec)
	})

	return zipMembers(t, map[string][]byte{"collection.anki21": collBytes})
}

func normaliseDeckNameToSchema18(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		if i+1 < len(name) && name[i] == ':' && name[i+1] == ':' {
			out = append(out, '\x1f')
			i++
			continue
		}
		out = append(out, name[i])
	}
	return string(out)
}

// buildZstdPackage re-wraps pkg's collection and media members as zstd frames, producing the
// modern container from any builder's output.
func buildZstdPackage(t *testing.T, pkg []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatalf("opening input package: %v", err)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithSingleSegment(true))
	if err != nil {
		t.Fatalf("creating zstd encoder: %v", err)
	}
	defer closeQuietly(enc)

	members := map[string][]byte{}
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
		data := buf.Bytes()
		if f.Name == "collection.anki21" || f.Name == "media" {
			data = enc.EncodeAll(data, nil)
		}
		members[f.Name] = data
	}
	return zipMembers(t, members)
}

// buildDowngradeStubPackage produces the real-world trap from apkg-format.md: a package holding
// BOTH collection.anki21 (the real collection, defaultSynthSpec) and collection.anki2 (a
// one-note "please upgrade" placeholder). Reading the wrong member imports one note and reports
// success.
func buildDowngradeStubPackage(t *testing.T) []byte {
	t.Helper()
	real := buildSchema11Package(t, defaultSynthSpec(t))
	zr, err := zip.NewReader(bytes.NewReader(real), int64(len(real)))
	if err != nil {
		t.Fatalf("opening real package: %v", err)
	}
	var realCollBytes []byte
	for _, f := range zr.File {
		if f.Name == "collection.anki21" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("opening collection member: %v", err)
			}
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(rc); err != nil {
				t.Fatalf("reading collection member: %v", err)
			}
			closeQuietly(rc)
			realCollBytes = buf.Bytes()
		}
	}

	stubSpec := synthSpec{
		Crt:       defaultSynthSpec(t).Crt,
		NoteTypes: []synthNoteType{{AnkiID: 9001, Name: "Stub", Fields: []IrField{{Ordinal: 0, Name: "Front"}}, Templates: []IrTemplate{{Ordinal: 0, Name: "Card 1", Qfmt: "{{Front}}", Afmt: "{{Front}}"}}}},
		Decks:     []synthDeck{{AnkiID: 1, Name: "Default"}},
		Notes:     []synthNote{{AnkiID: 901, Guid: "stub-guid", NoteTypeAnkiID: 9001, Fields: []string{"please upgrade"}}},
		Cards:     []synthCard{{AnkiID: 902, NoteAnkiID: 901, Did: 1, Ord: 0, Type: ankiTypeNew, Queue: ankiQueueNew, Due: 1}},
	}
	stub := buildSchema11Package(t, stubSpec)
	zrStub, err := zip.NewReader(bytes.NewReader(stub), int64(len(stub)))
	if err != nil {
		t.Fatalf("opening stub package: %v", err)
	}
	var stubCollBytes []byte
	for _, f := range zrStub.File {
		if f.Name == "collection.anki21" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("opening stub collection member: %v", err)
			}
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(rc); err != nil {
				t.Fatalf("reading stub collection member: %v", err)
			}
			closeQuietly(rc)
			stubCollBytes = buf.Bytes()
		}
	}

	return zipMembers(t, map[string][]byte{
		"collection.anki21": realCollBytes,
		"collection.anki2":  stubCollBytes,
	})
}

// buildOutOfOrderOrdPackage is defaultSynthSpec with FieldOrderShuffled set: the note type's
// flds and tmpls arrays are written in reverse-ord order while notes.flds stays indexed by ord.
// A reader keying on array index maps every field value onto the wrong field name.
func buildOutOfOrderOrdPackage(t *testing.T) []byte {
	t.Helper()
	spec := defaultSynthSpec(t)
	spec.FieldOrderShuffled = true
	return buildSchema11Package(t, spec)
}

// buildFilteredDeckPackage is defaultSynthSpec plus a filtered deck (dyn = 1) holding one card
// whose did is the filtered deck, odid its real home deck, due = -12345 and odue = 7. The two
// values are far enough apart, and of opposite sign, that reading the wrong column cannot pass
// by coincidence.
func buildFilteredDeckPackage(t *testing.T) []byte {
	t.Helper()
	spec := defaultSynthSpec(t)
	spec.Decks = append(spec.Decks, synthDeck{AnkiID: 3, Name: "Filtered", Dyn: 1})
	// A dedicated note/card pair, rather than reusing note 101's, since 101 already has cards at
	// ordinals 0 and 1 (Basic has two templates) and (note_id, ordinal) is unique.
	spec.Notes = append(spec.Notes, synthNote{AnkiID: 104, Guid: "guid-note-4", NoteTypeAnkiID: 1001, Fields: []string{"f4", "b4", ""}, Mod: spec.Crt.Unix() + 40})
	spec.Cards = append(spec.Cards, synthCard{
		AnkiID: 206, NoteAnkiID: 104, Did: 3, Odid: 1, Ord: 0,
		Type: ankiTypeReview, Queue: ankiQueueReview, Due: -12345, Odue: 7,
	})
	return buildSchema11Package(t, spec)
}

// buildOversizePackage returns a package whose "media" INDEX member (the only media-related
// member ever zstd-sniffed -- individual media files are stored uncompressed and never
// zstd-sniffed, per media.go) is a zstd frame. declaredOnly writes a frame whose declared
// Frame_Content_Size exceeds any reasonable limit while the actual payload is tiny, exercising
// the before-decompression gate specifically; !declaredOnly writes a frame whose declared size
// is accurate.
func buildOversizePackage(t *testing.T, declaredOnly bool) []byte {
	t.Helper()
	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatalf("opening package: %v", err)
	}
	members := map[string][]byte{}
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

	enc, err := zstd.NewWriter(nil, zstd.WithSingleSegment(true))
	if err != nil {
		t.Fatalf("creating zstd encoder: %v", err)
	}
	defer closeQuietly(enc)

	frame := enc.EncodeAll([]byte(`{}`), nil)
	if declaredOnly {
		frame = patchZstdDeclaredSize(t, frame, 10<<30) // 10 GiB, dwarfs any test limit
	}
	members["media"] = frame

	return zipMembers(t, members)
}

// patchZstdDeclaredSize rebuilds frame's header with an 8-byte Frame_Content_Size field
// (single-segment, FCS code 3) declaring newSize, keeping the compressed block data that follows
// the original header unchanged. A rebuild rather than an in-place overwrite because the
// original field (typically 1 byte for a tiny payload) may be too narrow to hold newSize.
func patchZstdDeclaredSize(t *testing.T, frame []byte, newSize uint64) []byte {
	t.Helper()
	if len(frame) < 5 {
		t.Fatalf("frame too short to have a header: %d bytes", len(frame))
	}
	fhd := frame[4]
	fcsCode := fhd >> 6
	singleSegment := fhd&0x20 != 0
	dictIDFlag := fhd & 0x3

	off := 5
	if !singleSegment {
		off++ // Window_Descriptor
	}
	var dictSize int
	switch dictIDFlag {
	case 1:
		dictSize = 1
	case 2:
		dictSize = 2
	case 3:
		dictSize = 4
	}
	dictBytes := frame[off : off+dictSize]
	off += dictSize

	var oldFcsSize int
	switch fcsCode {
	case 0:
		if singleSegment {
			oldFcsSize = 1
		}
	case 1:
		oldFcsSize = 2
	case 2:
		oldFcsSize = 4
	case 3:
		oldFcsSize = 8
	}
	rest := frame[off+oldFcsSize:]

	newDescriptor := byte(3<<6) | 0x20 | dictIDFlag // code 3 (8-byte FCS), single segment
	sizeBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(sizeBuf, newSize)

	out := append([]byte{}, frame[:4]...)
	out = append(out, newDescriptor)
	out = append(out, dictBytes...)
	out = append(out, sizeBuf...)
	out = append(out, rest...)
	return out
}

// encodeProtoVarint mirrors decodeProto's varint field encoding, test-only (protobuf.go decodes,
// it does not encode -- so no production code exists only for tests).
func encodeProtoVarint(num uint32, v uint64) []byte {
	var out []byte
	out = appendVarint(out, uint64(num)<<3|uint64(protoVarint))
	out = appendVarint(out, v)
	return out
}

// encodeProtoString mirrors decodeProto's length-delimited field encoding, test-only.
func encodeProtoString(num uint32, s string) []byte {
	var out []byte
	out = appendVarint(out, uint64(num)<<3|uint64(protoBytes))
	out = appendVarint(out, uint64(len(s)))
	out = append(out, s...)
	return out
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}
