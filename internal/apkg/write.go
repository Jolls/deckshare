package apkg

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WriteFile serialises col as a .apkg package and writes it to path.
func WriteFile(col *IrCollection, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("apkg: creating %q: %w", path, err)
	}
	defer closeQuietly(f)
	return Write(col, f)
}

// Write serialises col (db -> IR already done by Export) as a .apkg package: a legacy zip
// container -- no meta member, uncompressed collection.anki21 -- carrying a schema-11 collection
// (col.models/col.decks JSON blobs). Schema 18 is never written, verified reader support (#61)
// notwithstanding: every Anki version can read schema 11, so there is no compatibility reason to
// ever emit the newer, more complex format.
func Write(col *IrCollection, w io.Writer) error {
	collBytes, err := buildCollection(col)
	if err != nil {
		return err
	}

	mediaJSON, err := encodeMediaIndex(col.Media)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	if err := writeZipMember(zw, "collection.anki21", collBytes); err != nil {
		return err
	}
	if err := writeZipMember(zw, "media", mediaJSON); err != nil {
		return err
	}
	for _, m := range col.Media {
		if err := writeZipMember(zw, m.Index, m.Data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("apkg: closing zip writer: %w", err)
	}
	return nil
}

func writeZipMember(zw *zip.Writer, name string, data []byte) error {
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return fmt.Errorf("apkg: creating zip member %q: %w", name, err)
	}
	if _, err := fw.Write(data); err != nil {
		return fmt.Errorf("apkg: writing zip member %q: %w", name, err)
	}
	return nil
}

// collectionDDL is read.go's sqlSelectCol11/sqlSelectNotes/sqlSelectCards shapes, in reverse.
const collectionDDL = `
CREATE TABLE col (id integer primary key, crt integer, ver integer, models text, decks text);
CREATE TABLE notes (id integer primary key, guid text, mid integer, mod integer, tags text, flds text, csum integer);
CREATE TABLE cards (id integer primary key, nid integer, did integer, ord integer, type integer, queue integer, due integer, ivl integer, factor integer, reps integer, lapses integer, odue integer, odid integer, flags integer, data text);
`

const revlogDDL = `CREATE TABLE revlog (id integer primary key, cid integer, ease integer, ivl integer, lastIvl integer, factor integer, time integer, type integer);`

// buildCollection writes col as a schema-11 SQLite file and returns its bytes. modernc.org/sqlite
// opens files, not byte slices (read.go's openCollection has the same constraint in reverse).
func buildCollection(col *IrCollection) ([]byte, error) {
	dir, err := os.MkdirTemp("", "apkg-write-*")
	if err != nil {
		return nil, fmt.Errorf("apkg: creating temp dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			return
		}
	}()

	p := filepath.Join(dir, "collection.anki2")
	dbh, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p))
	if err != nil {
		return nil, fmt.Errorf("apkg: opening temp collection file: %w", err)
	}

	if err := writeCollection(dbh, col); err != nil {
		closeQuietly(dbh)
		return nil, err
	}
	if err := dbh.Close(); err != nil {
		return nil, fmt.Errorf("apkg: closing temp collection file: %w", err)
	}

	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("apkg: reading temp collection file: %w", err)
	}
	return b, nil
}

func writeCollection(dbh *sql.DB, col *IrCollection) error {
	ddl := collectionDDL
	if len(col.Reviews) > 0 {
		ddl += revlogDDL
	}
	if _, err := dbh.Exec(ddl); err != nil {
		return fmt.Errorf("apkg: creating collection schema: %w", err)
	}

	modelsJSON, decksJSON, err := encodeModelsAndDecks(col.NoteTypes, col.Decks)
	if err != nil {
		return err
	}

	// One transaction for every row below: SQLite otherwise commits -- and fsyncs -- once per
	// Exec, which on a real collection is one commit per note, card and revlog row.
	tx, err := dbh.Begin()
	if err != nil {
		return fmt.Errorf("apkg: beginning collection transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("INSERT INTO col (id, crt, ver, models, decks) VALUES (1,?,11,?,?)",
		col.Crt.Unix(), string(modelsJSON), string(decksJSON)); err != nil {
		return fmt.Errorf("apkg: inserting col row: %w", err)
	}

	for _, n := range col.Notes {
		if _, err := tx.Exec("INSERT INTO notes (id, guid, mid, mod, tags, flds, csum) VALUES (?,?,?,?,?,?,?)",
			n.AnkiID, n.Guid, n.NoteTypeAnkiID, n.Modified.Unix(), encodeTags(n.Tags), strings.Join(n.Fields, "\x1f"), n.Checksum); err != nil {
			return fmt.Errorf("apkg: inserting note %q: %w", n.Guid, err)
		}
	}

	for _, c := range col.Cards {
		data, err := encodeCardData(c.FSRS)
		if err != nil {
			return fmt.Errorf("apkg: encoding cards.data for card (anki_id %d): %w", c.AnkiID, err)
		}
		due := unresolveDue(c.Queue, c.Due, col.Crt)
		if _, err := tx.Exec("INSERT INTO cards (id, nid, did, ord, type, queue, due, ivl, factor, reps, lapses, odue, odid, flags, data) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			c.AnkiID, c.NoteAnkiID, c.DeckAnkiID, c.Ordinal, c.Type, c.Queue, due,
			unintervalSeconds(c.IntervalSeconds), c.Factor, c.Reps, c.Lapses, 0, 0, int32(c.Flag), data); err != nil {
			return fmt.Errorf("apkg: inserting card (anki_id %d): %w", c.AnkiID, err)
		}
	}

	for _, r := range col.Reviews {
		if _, err := tx.Exec("INSERT INTO revlog (id, cid, ease, ivl, lastIvl, factor, time, type) VALUES (?,?,?,?,?,?,?,?)",
			r.AnkiID, r.CardAnkiID, r.Rating, unintervalSeconds(r.IntervalSeconds), unintervalSeconds(r.LastIntervalSeconds),
			r.Factor, r.DurationMs, r.Kind); err != nil {
			return fmt.Errorf("apkg: inserting revlog row (anki_id %d): %w", r.AnkiID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apkg: committing collection: %w", err)
	}
	return nil
}

// encodeModelsAndDecks builds col.models / col.decks, the schema-11 JSON blobs readSchema11
// decodes (ankischema.go's ankiModel11/ankiDeck11 -- the same types, run in reverse).
func encodeModelsAndDecks(noteTypes []IrNoteType, decks []IrDeck) (modelsJSON, decksJSON []byte, err error) {
	models := make(map[string]ankiModel11, len(noteTypes))
	for _, nt := range noteTypes {
		flds := make([]ankiField11, len(nt.Fields))
		for i, f := range nt.Fields {
			flds[i] = ankiField11{Name: f.Name, Ord: f.Ordinal, Font: f.Font, Size: f.Size, RTL: f.IsRTL, Sticky: f.Sticky}
		}
		tmpls := make([]ankiTemplate11, len(nt.Templates))
		for i, t := range nt.Templates {
			tmpls[i] = ankiTemplate11{Name: t.Name, Ord: t.Ordinal, Qfmt: t.Qfmt, Afmt: t.Afmt, Bqfmt: t.BrowserQfmt, Bafmt: t.BrowserAfmt}
		}
		typ := 0
		if nt.IsCloze {
			typ = 1
		}
		models[strconv.FormatInt(nt.AnkiID, 10)] = ankiModel11{
			ID: ankiIntOrString(nt.AnkiID), Name: nt.Name, Type: typ, Sortf: nt.SortFieldIdx, CSS: nt.CSS, Flds: flds, Tmpls: tmpls,
		}
	}

	deckMap := make(map[string]ankiDeck11, len(decks))
	for _, d := range decks {
		dyn := 0
		if d.IsFiltered {
			dyn = 1
		}
		deckMap[strconv.FormatInt(d.AnkiID, 10)] = ankiDeck11{ID: ankiIntOrString(d.AnkiID), Name: d.Name, Desc: d.Description, Dyn: dyn}
	}

	modelsJSON, err = json.Marshal(models)
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: marshalling col.models: %w", err)
	}
	decksJSON, err = json.Marshal(deckMap)
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: marshalling col.decks: %w", err)
	}
	return modelsJSON, decksJSON, nil
}

// encodeTags is the inverse of splitTags: space-separated AND space-surrounded.
func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return " " + strings.Join(tags, " ") + " "
}

// encodeCardData is the inverse of readCards' ankiCardData decode. Position and desired
// retention are never populated: dbwrite.go's seedCardStates never reads IrFSRSState.Position or
// .DesiredRetention either, so there is nothing to round-trip there.
func encodeCardData(fsrs *IrFSRSState) (string, error) {
	if fsrs == nil {
		return "{}", nil
	}
	b, err := json.Marshal(ankiCardData{S: &fsrs.Stability, D: &fsrs.Difficulty})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unresolveDue is resolveDue run in reverse: given the queue this card is being written with and
// its already-decided IrDue, computes the raw `due` column value. odue/odid are always written 0
// -- Enshu tracks no filtered decks on export (architecture.md §20, apkg-format.md's Export
// section).
func unresolveDue(queue int32, d IrDue, crt time.Time) int64 {
	switch d.Kind {
	case DuePosition:
		return int64(d.Position)
	case DueAt:
		if queue == ankiQueueReview || queue == ankiQueueDayLearning {
			days := math.Round(d.At.Sub(crt).Hours() / 24)
			if days < 0 {
				days = 0
			}
			return int64(days)
		}
		return d.At.Unix() // learning/preview, or the ambiguous suspended/buried hold
	default:
		return 0
	}
}

// unintervalSeconds is intervalSeconds run in reverse: a whole number of days encodes as a
// positive day count (Anki's common case), anything else as negative seconds.
func unintervalSeconds(s int64) int64 {
	if s%secondsPerDay == 0 {
		return s / secondsPerDay
	}
	return -s
}
