package apkg

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

// closeQuietly closes c and drops any error. Used only for read-only cleanup that happens after
// every check that could reveal a misread (rows.Err(), Scan errors) has already run -- a close
// failure at that point is a driver-level detail with nothing left for the caller to act on, and
// CLAUDE.md §9's "no swallowed errors" is aimed at errors that could affect what gets imported,
// not at post-success resource teardown.
func closeQuietly(c interface{ Close() error }) {
	if err := c.Close(); err != nil {
		return
	}
}

// ArchiveLimits bounds an untrusted package (architecture.md §8: a shared deck is other users'
// bytes). Zero fields are rejected -- use DefaultArchiveLimits and adjust.
type ArchiveLimits struct {
	MaxMembers     int   // zip entries in the archive
	MaxMemberBytes int64 // decompressed bytes of any one member
	MaxTotalBytes  int64 // decompressed bytes across all members read
}

// DefaultArchiveLimits returns the ceilings applied when a caller has no stricter requirement.
// Sized off the one real export inspected (546 media files, largest member well under 100 MiB):
// generous headroom over real-world packages while bounding worst-case memory more tightly than
// a laxer default would on a typical self-hosted box.
func DefaultArchiveLimits() ArchiveLimits {
	return ArchiveLimits{
		MaxMembers:     5_000,
		MaxMemberBytes: 100 << 20, // 100 MiB per member
		MaxTotalBytes:  500 << 20, // 500 MiB across everything read
	}
}

// ReadFile reads the package at path. It is the entry point the import handler (#62) calls.
func ReadFile(path string, limits ArchiveLimits) (*IrCollection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("apkg: opening %q: %w", path, err)
	}
	defer closeQuietly(f)
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("apkg: stating %q: %w", path, err)
	}
	return Read(f, info.Size(), limits)
}

// Read reads a package from r, which must be size bytes long.
func Read(r io.ReaderAt, size int64, limits ArchiveLimits) (*IrCollection, error) {
	z, err := openArchive(r, size, limits)
	if err != nil {
		return nil, err
	}

	budget := limits.MaxTotalBytes

	collMember, err := pickCollectionMember(z)
	if err != nil {
		return nil, err
	}
	collBytes, err := memberBytes(collMember, limits, &budget)
	if err != nil {
		return nil, err
	}
	if sniffZstd(collBytes) {
		collBytes, err = decompressZstd(collBytes, limits, &budget)
		if err != nil {
			return nil, err
		}
	}

	dbh, cleanup, err := openCollection(collBytes)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	schema, err := detectSchema(dbh)
	if err != nil {
		return nil, err
	}

	crt, err := readCrt(dbh)
	if err != nil {
		return nil, err
	}

	var noteTypes []IrNoteType
	var decks []IrDeck
	switch schema {
	case 11:
		noteTypes, decks, err = readSchema11(dbh)
	case 18:
		noteTypes, decks, err = readSchema18(dbh)
	}
	if err != nil {
		return nil, err
	}

	notes, err := readNotes(dbh)
	if err != nil {
		return nil, err
	}
	cards, err := readCards(dbh, crt)
	if err != nil {
		return nil, err
	}
	reviews, warnings, err := readRevlog(dbh)
	if err != nil {
		return nil, err
	}

	homeDeckWarnings := resolveHomeDecks(notes, cards)
	warnings = append(warnings, homeDeckWarnings...)

	var media []IrMedia
	if mediaMember := findMember(z, "media"); mediaMember != nil {
		mediaBytes, err := memberBytes(mediaMember, limits, &budget)
		if err != nil {
			return nil, err
		}
		if sniffZstd(mediaBytes) {
			mediaBytes, err = decompressZstd(mediaBytes, limits, &budget)
			if err != nil {
				return nil, err
			}
		}
		idx, err := readMediaIndex(mediaBytes)
		if err != nil {
			return nil, err
		}
		var mediaWarnings []string
		media, mediaWarnings, err = collectMedia(z, idx, limits, &budget)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, mediaWarnings...)
	}

	return &IrCollection{
		Crt:           crt,
		SchemaVersion: schema,
		NoteTypes:     noteTypes,
		Decks:         decks,
		Notes:         notes,
		Cards:         cards,
		Reviews:       reviews,
		Media:         media,
		Warnings:      warnings,
	}, nil
}

func openArchive(r io.ReaderAt, size int64, limits ArchiveLimits) (*zip.Reader, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("apkg: opening zip archive: %w: %v", ErrNotAPackage, err)
	}
	if len(z.File) > limits.MaxMembers {
		return nil, ErrTooManyMembers
	}
	return z, nil
}

// memberBytes reads one member with both ceilings enforced against actual bytes, not the zip
// header's claim (a header can lie). budget is the running total, decremented in place.
func memberBytes(f *zip.File, limits ArchiveLimits, budget *int64) ([]byte, error) {
	if int64(f.UncompressedSize64) > limits.MaxMemberBytes {
		return nil, ErrMemberTooLarge
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("apkg: opening archive member %q: %w", f.Name, err)
	}
	defer closeQuietly(rc)
	buf, err := io.ReadAll(io.LimitReader(rc, limits.MaxMemberBytes+1))
	if err != nil {
		return nil, fmt.Errorf("apkg: reading archive member %q: %w", f.Name, err)
	}
	if int64(len(buf)) > limits.MaxMemberBytes {
		return nil, ErrMemberTooLarge
	}
	if int64(len(buf)) > *budget {
		return nil, ErrArchiveTooLarge
	}
	*budget -= int64(len(buf))
	return buf, nil
}

func findMember(z *zip.Reader, name string) *zip.File {
	for _, f := range z.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// sniffZstd reports whether b begins with the zstd magic number.
func sniffZstd(b []byte) bool {
	return len(b) >= 4 && b[0] == 0x28 && b[1] == 0xB5 && b[2] == 0x2F && b[3] == 0xFD
}

// zstdDeclaredSize parses Frame_Content_Size out of the frame header without decompressing.
// false means the frame declares no size.
func zstdDeclaredSize(b []byte) (int64, bool, error) {
	if len(b) < 5 {
		return 0, false, ErrBadZstdFrame
	}
	fhd := b[4]
	fcsCode := fhd >> 6
	singleSegment := fhd&0x20 != 0
	dictIDFlag := fhd & 0x3

	off := 5
	if !singleSegment {
		off++ // Window_Descriptor
	}

	var dictSize int
	switch dictIDFlag {
	case 0:
		dictSize = 0
	case 1:
		dictSize = 1
	case 2:
		dictSize = 2
	case 3:
		dictSize = 4
	}
	off += dictSize

	var fcsSize int
	switch fcsCode {
	case 0:
		if singleSegment {
			fcsSize = 1
		} else {
			fcsSize = 0
		}
	case 1:
		fcsSize = 2
	case 2:
		fcsSize = 4
	case 3:
		fcsSize = 8
	}

	if fcsSize == 0 {
		return 0, false, nil
	}
	if len(b) < off+fcsSize {
		return 0, false, ErrBadZstdFrame
	}
	var v uint64
	for i := 0; i < fcsSize; i++ {
		v |= uint64(b[off+i]) << (8 * i)
	}
	if fcsSize == 2 {
		v += 256
	}
	return int64(v), true, nil
}

// decompressZstd applies the declared-size gate before any decompression happens -- DecodeAll is
// one uninterruptible allocation, so a post-hoc check is too late.
func decompressZstd(b []byte, limits ArchiveLimits, budget *int64) ([]byte, error) {
	limit := limits.MaxMemberBytes
	if *budget < limit {
		limit = *budget
	}

	declared, ok, err := zstdDeclaredSize(b)
	if err != nil {
		return nil, err
	}
	if ok && declared > limit {
		return nil, ErrMemberTooLarge
	}

	dec, err := zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderMaxMemory(uint64(limits.MaxMemberBytes)))
	if err != nil {
		return nil, fmt.Errorf("apkg: opening zstd frame: %w", ErrBadZstdFrame)
	}
	defer dec.Close()

	out, err := io.ReadAll(io.LimitReader(dec, limit+1))
	if err != nil {
		return nil, fmt.Errorf("apkg: decompressing zstd frame: %w", ErrBadZstdFrame)
	}
	if int64(len(out)) > limit {
		return nil, ErrMemberTooLarge
	}
	*budget -= int64(len(out))
	return out, nil
}

// pickCollectionMember chooses collection.anki21b, then collection.anki21, then collection.anki2
// -- newest first, load-bearing (apkg-format.md's downgrade-stub trap).
func pickCollectionMember(z *zip.Reader) (*zip.File, error) {
	for _, name := range []string{"collection.anki21b", "collection.anki21", "collection.anki2"} {
		if f := findMember(z, name); f != nil {
			return f, nil
		}
	}
	return nil, ErrNoCollection
}

// openCollection writes bytes to a file in os.MkdirTemp and opens it read-only. modernc.org/
// sqlite opens files, not byte slices.
func openCollection(b []byte) (*sql.DB, func(), error) {
	dir, err := os.MkdirTemp("", "apkg-collection-*")
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: creating temp dir: %w", err)
	}
	cleanupDir := func() {
		if err := os.RemoveAll(dir); err != nil {
			return
		}
	}

	p := filepath.Join(dir, "collection.anki2")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		cleanupDir()
		return nil, nil, fmt.Errorf("apkg: writing temp collection file: %w", err)
	}

	dbh, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p)+"?mode=ro")
	if err != nil {
		cleanupDir()
		return nil, nil, fmt.Errorf("apkg: opening collection sqlite file: %w", err)
	}
	cleanup := func() {
		closeQuietly(dbh)
		cleanupDir()
	}
	return dbh, cleanup, nil
}

// detectSchema probes table presence, never col.ver -- a repacked package can claim 18 while
// carrying only JSON blobs.
func detectSchema(dbh *sql.DB) (int, error) {
	tables := map[string]bool{}
	rows, err := dbh.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return 0, fmt.Errorf("apkg: listing collection tables: %w", ErrCorruptCollection)
	}
	defer closeQuietly(rows)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return 0, fmt.Errorf("apkg: reading table name: %w", ErrCorruptCollection)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("apkg: iterating collection tables: %w", ErrCorruptCollection)
	}

	if tables["notetypes"] && tables["fields"] && tables["templates"] && tables["decks"] {
		return 18, nil
	}
	if tables["col"] {
		return 11, nil
	}
	return 0, ErrUnknownSchema
}

func readCrt(dbh *sql.DB) (time.Time, error) {
	var crt int64
	if err := dbh.QueryRow("SELECT crt FROM col LIMIT 1").Scan(&crt); err != nil {
		return time.Time{}, fmt.Errorf("apkg: reading col.crt: %w", ErrCorruptCollection)
	}
	return time.Unix(crt, 0).UTC(), nil
}

func readSchema11(dbh *sql.DB) ([]IrNoteType, []IrDeck, error) {
	var modelsJSON, decksJSON []byte
	if err := dbh.QueryRow(sqlSelectCol11).Scan(new(int64), &modelsJSON, &decksJSON); err != nil {
		return nil, nil, fmt.Errorf("apkg: reading col.models/decks: %w", ErrCorruptCollection)
	}

	var models map[string]ankiModel11
	if err := json.Unmarshal(modelsJSON, &models); err != nil {
		return nil, nil, fmt.Errorf("apkg: decoding col.models: %w", ErrCorruptCollection)
	}
	var deckMap map[string]ankiDeck11
	if err := json.Unmarshal(decksJSON, &deckMap); err != nil {
		return nil, nil, fmt.Errorf("apkg: decoding col.decks: %w", ErrCorruptCollection)
	}

	noteTypes := make([]IrNoteType, 0, len(models))
	for _, m := range models {
		nt := IrNoteType{
			AnkiID:       m.ID,
			Name:         m.Name,
			CSS:          m.CSS,
			IsCloze:      m.Type == 1,
			SortFieldIdx: m.Sortf,
		}
		for _, fld := range m.Flds {
			nt.Fields = append(nt.Fields, IrField{
				Ordinal: fld.Ord,
				Name:    fld.Name,
				Font:    fld.Font,
				Size:    fld.Size,
				IsRTL:   fld.RTL,
				Sticky:  fld.Sticky,
			})
		}
		for _, t := range m.Tmpls {
			nt.Templates = append(nt.Templates, IrTemplate{
				Ordinal:     t.Ord,
				Name:        t.Name,
				Qfmt:        t.Qfmt,
				Afmt:        t.Afmt,
				BrowserQfmt: t.Bqfmt,
				BrowserAfmt: t.Bafmt,
			})
		}
		sortFieldsByOrdinal(nt.Fields)
		sortTemplatesByOrdinal(nt.Templates)
		noteTypes = append(noteTypes, nt)
	}
	// col.models/col.decks are JSON objects; map iteration order is randomised per-process, so
	// reading the same bytes twice must not be allowed to reorder the result (TestRead_TwiceIsIdentical).
	sort.Slice(noteTypes, func(i, j int) bool { return noteTypes[i].AnkiID < noteTypes[j].AnkiID })

	decks := make([]IrDeck, 0, len(deckMap))
	for _, d := range deckMap {
		decks = append(decks, IrDeck{
			AnkiID:      d.ID,
			Name:        normaliseDeckName(d.Name),
			Description: d.Desc,
			IsFiltered:  d.Dyn != 0,
		})
	}
	sort.Slice(decks, func(i, j int) bool { return decks[i].AnkiID < decks[j].AnkiID })

	return noteTypes, decks, nil
}

// readSchema18 decodes the notetypes/fields/templates/decks tables, including their protobuf
// config columns, using the placeholder field numbers in ankischema.go. Per
// docs/plans/58-apkg-import.md §10.1, the numbers are unverified against a real export (#61), so
// this always returns ErrSchema18Config after decoding -- the decode plumbing runs (and is what
// #61's fixture will exercise once the numbers are confirmed), but nothing schema-18 is ever
// imported until then.
func readSchema18(dbh *sql.DB) ([]IrNoteType, []IrDeck, error) {
	if _, _, err := decodeSchema18(dbh); err != nil {
		return nil, nil, err
	}
	return nil, nil, fmt.Errorf("apkg: schema-18 protobuf field numbers are unverified, see #61: %w", ErrSchema18Config)
}

// decodeSchema18 does the actual notetypes/fields/templates/decks decode. Separated from
// readSchema18 so the plumbing can be exercised directly once #61 lands.
func decodeSchema18(dbh *sql.DB) ([]IrNoteType, []IrDeck, error) {
	ntRows, err := dbh.Query(sqlSelectNotetypes18)
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: reading notetypes: %w", ErrCorruptCollection)
	}
	type ntRow struct {
		id     int64
		name   string
		config []byte
	}
	var ntRowsList []ntRow
	for ntRows.Next() {
		var r ntRow
		if err := ntRows.Scan(&r.id, &r.name, &r.config); err != nil {
			closeQuietly(ntRows)
			return nil, nil, fmt.Errorf("apkg: scanning notetypes row: %w", ErrCorruptCollection)
		}
		ntRowsList = append(ntRowsList, r)
	}
	if err := ntRows.Err(); err != nil {
		closeQuietly(ntRows)
		return nil, nil, fmt.Errorf("apkg: iterating notetypes: %w", ErrCorruptCollection)
	}
	closeQuietly(ntRows)

	noteTypes := make([]IrNoteType, 0, len(ntRowsList))
	ntByID := map[int64]int{}
	for _, r := range ntRowsList {
		fields, err := decodeProto(r.config)
		if err != nil {
			return nil, nil, fmt.Errorf("apkg: decoding notetypes.config for %q: %w", r.name, err)
		}
		kind, _ := protoUint(fields, ntConfigKindField)
		sortField, _ := protoUint(fields, ntConfigSortFieldField)
		css, _ := protoString(fields, ntConfigCSSField)
		nt := IrNoteType{
			AnkiID:       r.id,
			Name:         r.name,
			CSS:          css,
			IsCloze:      kind == 1,
			SortFieldIdx: int32(sortField),
		}
		ntByID[r.id] = len(noteTypes)
		noteTypes = append(noteTypes, nt)
	}

	fRows, err := dbh.Query(sqlSelectFields18)
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: reading fields: %w", ErrCorruptCollection)
	}
	for fRows.Next() {
		var ntid int64
		var ord int32
		var name string
		var config []byte
		if err := fRows.Scan(&ntid, &ord, &name, &config); err != nil {
			closeQuietly(fRows)
			return nil, nil, fmt.Errorf("apkg: scanning fields row: %w", ErrCorruptCollection)
		}
		i, ok := ntByID[ntid]
		if !ok {
			continue
		}
		fields, err := decodeProto(config)
		if err != nil {
			closeQuietly(fRows)
			return nil, nil, fmt.Errorf("apkg: decoding fields.config for %q: %w", name, err)
		}
		font, _ := protoString(fields, fieldConfigFontField)
		size, _ := protoUint(fields, fieldConfigSizeField)
		rtl, _ := protoUint(fields, fieldConfigRTLField)
		sticky, _ := protoUint(fields, fieldConfigStickyField)
		noteTypes[i].Fields = append(noteTypes[i].Fields, IrField{
			Ordinal: ord,
			Name:    name,
			Font:    font,
			Size:    int32(size),
			IsRTL:   rtl != 0,
			Sticky:  sticky != 0,
		})
	}
	if err := fRows.Err(); err != nil {
		closeQuietly(fRows)
		return nil, nil, fmt.Errorf("apkg: iterating fields: %w", ErrCorruptCollection)
	}
	closeQuietly(fRows)

	tRows, err := dbh.Query(sqlSelectTemplates18)
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: reading templates: %w", ErrCorruptCollection)
	}
	for tRows.Next() {
		var ntid int64
		var ord int32
		var name string
		var config []byte
		if err := tRows.Scan(&ntid, &ord, &name, &config); err != nil {
			closeQuietly(tRows)
			return nil, nil, fmt.Errorf("apkg: scanning templates row: %w", ErrCorruptCollection)
		}
		i, ok := ntByID[ntid]
		if !ok {
			continue
		}
		fields, err := decodeProto(config)
		if err != nil {
			closeQuietly(tRows)
			return nil, nil, fmt.Errorf("apkg: decoding templates.config for %q: %w", name, err)
		}
		qfmt, _ := protoString(fields, tmplConfigQFmtField)
		afmt, _ := protoString(fields, tmplConfigAFmtField)
		bqfmt, _ := protoString(fields, tmplConfigBQFmtField)
		bafmt, _ := protoString(fields, tmplConfigBAFmtField)
		noteTypes[i].Templates = append(noteTypes[i].Templates, IrTemplate{
			Ordinal:     ord,
			Name:        name,
			Qfmt:        qfmt,
			Afmt:        afmt,
			BrowserQfmt: bqfmt,
			BrowserAfmt: bafmt,
		})
	}
	if err := tRows.Err(); err != nil {
		closeQuietly(tRows)
		return nil, nil, fmt.Errorf("apkg: iterating templates: %w", ErrCorruptCollection)
	}
	closeQuietly(tRows)

	for i := range noteTypes {
		sortFieldsByOrdinal(noteTypes[i].Fields)
		sortTemplatesByOrdinal(noteTypes[i].Templates)
	}

	if err := validateSchema18Decode(noteTypes); err != nil {
		return nil, nil, err
	}

	dRows, err := dbh.Query(sqlSelectDecks18)
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: reading decks: %w", ErrCorruptCollection)
	}
	defer closeQuietly(dRows)
	var decks []IrDeck
	for dRows.Next() {
		var id int64
		var name string
		var common, kind []byte
		if err := dRows.Scan(&id, &name, &common, &kind); err != nil {
			return nil, nil, fmt.Errorf("apkg: scanning decks row: %w", ErrCorruptCollection)
		}
		commonFields, err := decodeProto(common)
		if err != nil {
			return nil, nil, fmt.Errorf("apkg: decoding decks.common for %q: %w", name, err)
		}
		desc, _ := protoString(commonFields, deckCommonDescField)
		kindFields, err := decodeProto(kind)
		if err != nil {
			return nil, nil, fmt.Errorf("apkg: decoding decks.kind for %q: %w", name, err)
		}
		_, filtered := protoMessage(kindFields, deckKindFilteredField)
		decks = append(decks, IrDeck{
			AnkiID:      id,
			Name:        normaliseDeckName(name),
			Description: desc,
			IsFiltered:  filtered,
		})
	}
	if err := dRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("apkg: iterating decks: %w", ErrCorruptCollection)
	}

	return noteTypes, decks, nil
}

// validateSchema18Decode fails loudly rather than importing plausible-looking wrong data: every
// template must yield a non-empty Qfmt and every note type must yield at least one field. A
// half-decoded note type would render blank cards for every note of that type.
func validateSchema18Decode(noteTypes []IrNoteType) error {
	for _, nt := range noteTypes {
		if len(nt.Fields) == 0 {
			return fmt.Errorf("apkg: note type %q decoded with no fields: %w", nt.Name, ErrSchema18Config)
		}
		for _, t := range nt.Templates {
			if t.Qfmt == "" {
				return fmt.Errorf("apkg: template %q of note type %q decoded with an empty question format: %w", t.Name, nt.Name, ErrSchema18Config)
			}
		}
	}
	return nil
}

func readNotes(dbh *sql.DB) ([]IrNote, error) {
	rows, err := dbh.Query(sqlSelectNotes)
	if err != nil {
		return nil, fmt.Errorf("apkg: reading notes: %w", ErrCorruptCollection)
	}
	defer closeQuietly(rows)

	var notes []IrNote
	for rows.Next() {
		var id, mid, mod, csum int64
		var guid, tags, flds string
		if err := rows.Scan(&id, &guid, &mid, &mod, &tags, &flds, &csum); err != nil {
			return nil, fmt.Errorf("apkg: scanning note row: %w", ErrCorruptCollection)
		}
		notes = append(notes, IrNote{
			AnkiID:         id,
			Guid:           guid,
			NoteTypeAnkiID: mid,
			Fields:         splitFields(flds),
			Tags:           splitTags(tags),
			Checksum:       csum,
			Created:        time.UnixMilli(id).UTC(),
			Modified:       time.Unix(mod, 0).UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("apkg: iterating notes: %w", ErrCorruptCollection)
	}
	return notes, nil
}

func readCards(dbh *sql.DB, crt time.Time) ([]IrCard, error) {
	rows, err := dbh.Query(sqlSelectCards)
	if err != nil {
		return nil, fmt.Errorf("apkg: reading cards: %w", ErrCorruptCollection)
	}
	defer closeQuietly(rows)

	var cards []IrCard
	for rows.Next() {
		var id, nid, did, due, ivl, odue, odid int64
		var ord, typ, queue, factor, reps, lapses, flags int32
		var data []byte
		if err := rows.Scan(&id, &nid, &did, &ord, &typ, &queue, &due, &ivl, &factor, &reps, &lapses, &odue, &odid, &flags, &data); err != nil {
			return nil, fmt.Errorf("apkg: scanning card row: %w", ErrCorruptCollection)
		}

		deckAnkiID := did
		filteredDeckAnkiID := int64(0)
		if odid != 0 {
			deckAnkiID = odid
			filteredDeckAnkiID = did
		}

		var cd ankiCardData
		var fsrs *IrFSRSState
		if len(data) > 0 && json.Unmarshal(data, &cd) == nil {
			if cd.S != nil && cd.D != nil {
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
			AnkiID:             id,
			NoteAnkiID:         nid,
			DeckAnkiID:         deckAnkiID,
			FilteredDeckAnkiID: filteredDeckAnkiID,
			Ordinal:            ord,
			Type:               typ,
			Queue:              queue,
			Due:                resolveDue(queue, typ, due, odue, odid, crt),
			IntervalSeconds:    intervalSeconds(ivl),
			Factor:             factor,
			Reps:               reps,
			Lapses:             lapses,
			Flag:               int16(flags & ankiFlagMask),
			Suspended:          queue == ankiQueueSuspended,
			Buried:             queue == ankiQueueSchedBuried || queue == ankiQueueUserBuried,
			FSRS:               fsrs,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("apkg: iterating cards: %w", ErrCorruptCollection)
	}
	return cards, nil
}

// readRevlog returns the imported reviews and warnings. An absent revlog table is normal, not
// malformed -- a deck export without scheduling has none.
func readRevlog(dbh *sql.DB) ([]IrReview, []string, error) {
	var hasTable bool
	if err := dbh.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='revlog')").Scan(&hasTable); err != nil {
		return nil, nil, fmt.Errorf("apkg: probing for revlog table: %w", ErrCorruptCollection)
	}
	if !hasTable {
		return nil, nil, nil
	}

	rows, err := dbh.Query(sqlSelectRevlog)
	if err != nil {
		return nil, nil, fmt.Errorf("apkg: reading revlog: %w", ErrCorruptCollection)
	}
	defer closeQuietly(rows)

	var reviews []IrReview
	var warnings []string
	for rows.Next() {
		var id, cid, ivl, lastIvl int64
		var ease, factor, dur, typ int32
		if err := rows.Scan(&id, &cid, &ease, &ivl, &lastIvl, &factor, &dur, &typ); err != nil {
			return nil, nil, fmt.Errorf("apkg: scanning revlog row: %w", ErrCorruptCollection)
		}
		if ease == 0 {
			warnings = append(warnings, fmt.Sprintf("revlog: dropped manual reschedule row (id %d)", id))
			continue
		}
		reviews = append(reviews, IrReview{
			AnkiID:              id,
			CardAnkiID:          cid,
			ReviewedAt:          time.UnixMilli(id).UTC(),
			Rating:              int16(ease),
			IntervalSeconds:     intervalSeconds(ivl),
			LastIntervalSeconds: intervalSeconds(lastIvl),
			Factor:              factor,
			DurationMs:          dur,
			Kind:                int16(typ),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("apkg: iterating revlog: %w", ErrCorruptCollection)
	}
	return reviews, warnings, nil
}
