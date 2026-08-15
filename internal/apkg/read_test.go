package apkg

// Round-trip: the export half is #59; this file covers import determinism only
// (docs/plans/58-apkg-import.md §8).

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func readBytes(t *testing.T, pkg []byte) *IrCollection {
	t.Helper()
	col, err := Read(bytes.NewReader(pkg), int64(len(pkg)), DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return col
}

func TestRead_Schema11_MatchesSpec(t *testing.T) {
	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	got := readBytes(t, pkg)
	want := expectedIR(spec, 11)
	assertIRMatches(t, got, want)
}

// TestRead_Schema18_MatchesSpec exercises the same synthetic collection as
// TestRead_Schema11_MatchesSpec, written as a schema-18 package instead. Confirms the wire-format
// plumbing (protobuf decode, unicase collation registration, deck-kind oneof handling) is
// correct; it does not and cannot assert real-world field-number correctness on its own -- #61's
// real fixture (tests/fixtures/apkg/mathematics-schema18.apkg) is what verified those.
func TestRead_Schema18_MatchesSpec(t *testing.T) {
	spec := defaultSynthSpec(t)
	pkg := buildSchema18Package(t, spec)
	got := readBytes(t, pkg)
	want := expectedIR(spec, 18)
	assertIRMatches(t, got, want)
}

// TestRead_RealSchema18Fixture reads the real export that closed #61
// (tests/fixtures/apkg/mathematics-schema18.apkg, see its README) and asserts the facts that
// were hand-decoded from its raw protobuf bytes to verify ankischema.go's field numbers: a real
// Cloze note type is detected as cloze, a real Basic-family note type is not, every field/
// template decodes non-empty, and the deck tree resolves with neither deck marked filtered.
func TestRead_RealSchema18Fixture(t *testing.T) {
	col, err := ReadFile("../../tests/fixtures/apkg/mathematics-schema18.apkg", DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if col.SchemaVersion != 18 {
		t.Fatalf("SchemaVersion = %d, want 18", col.SchemaVersion)
	}

	var basic, cloze *IrNoteType
	for i := range col.NoteTypes {
		switch col.NoteTypes[i].Name {
		case "Basic+":
			basic = &col.NoteTypes[i]
		case "Cloze":
			cloze = &col.NoteTypes[i]
		}
	}
	if basic == nil {
		t.Fatal("Basic+ note type not found")
	}
	if cloze == nil {
		t.Fatal("Cloze note type not found")
	}
	if basic.IsCloze {
		t.Error("Basic+ note type decoded as cloze (ntConfigKindField wrong)")
	}
	if !cloze.IsCloze {
		t.Error("Cloze note type not decoded as cloze (ntConfigKindField wrong)")
	}
	for _, nt := range []*IrNoteType{basic, cloze} {
		if nt.CSS == "" {
			t.Errorf("%s: CSS decoded empty (ntConfigCSSField wrong)", nt.Name)
		}
		if len(nt.Fields) == 0 {
			t.Errorf("%s: no fields decoded", nt.Name)
		}
		for _, f := range nt.Fields {
			if f.Font != "Arial" || f.Size != 20 {
				t.Errorf("%s field %q: font/size = %q/%d, want Arial/20 (fieldConfigFontField/fieldConfigSizeField wrong)", nt.Name, f.Name, f.Font, f.Size)
			}
		}
		for _, tmpl := range nt.Templates {
			if tmpl.Qfmt == "" || tmpl.Afmt == "" {
				t.Errorf("%s template %q: qfmt/afmt = %q/%q, want both non-empty (tmplConfigQFmtField/tmplConfigAFmtField wrong)", nt.Name, tmpl.Name, tmpl.Qfmt, tmpl.Afmt)
			}
		}
	}

	if len(col.Decks) == 0 {
		t.Fatal("no decks decoded")
	}
	for _, d := range col.Decks {
		if d.IsFiltered {
			t.Errorf("deck %q decoded as filtered; this fixture has no filtered decks (deckKindNormalField wrong)", d.Name)
		}
	}

	if len(col.Media) == 0 {
		t.Fatal("no media decoded (mediaEntryField/mediaEntryNameField wrong)")
	}
}

func TestRead_OutOfOrderOrdArrays(t *testing.T) {
	pkg := buildOutOfOrderOrdPackage(t)
	got := readBytes(t, pkg)
	spec := defaultSynthSpec(t)
	want := expectedIR(spec, 11)
	assertIRMatches(t, got, want)
}

func TestRead_FilteredDeckUsesOdueAndOdid(t *testing.T) {
	pkg := buildFilteredDeckPackage(t)
	got := readBytes(t, pkg)

	var filteredCard *IrCard
	for i := range got.Cards {
		if got.Cards[i].AnkiID == 206 {
			filteredCard = &got.Cards[i]
		}
	}
	if filteredCard == nil {
		t.Fatal("filtered-deck card (anki_id 206) not found in result")
	}
	if filteredCard.DeckAnkiID != 1 {
		t.Errorf("DeckAnkiID = %d, want 1 (home deck, from odid)", filteredCard.DeckAnkiID)
	}
	if filteredCard.FilteredDeckAnkiID != 3 {
		t.Errorf("FilteredDeckAnkiID = %d, want 3 (the filtered deck)", filteredCard.FilteredDeckAnkiID)
	}
	wantDue := defaultSynthSpec(t).Crt.Add(7 * 24 * time.Hour)
	if filteredCard.Due.Kind != DueAt || !filteredCard.Due.At.Equal(wantDue) {
		t.Errorf("Due = %+v, want DueAt %v (derived from odue=7, not due=-12345)", filteredCard.Due, wantDue)
	}

	// The filtered deck itself is never created as an IrDeck consumer-visible entity separate
	// from being a deck in the collection -- the reader still reports it as a deck (write-side
	// filtering happens in dbwrite.go); assert it is present and marked filtered.
	var filteredDeck *IrDeck
	for i := range got.Decks {
		if got.Decks[i].AnkiID == 3 {
			filteredDeck = &got.Decks[i]
		}
	}
	if filteredDeck == nil || !filteredDeck.IsFiltered {
		t.Fatal("filtered deck not present or not marked IsFiltered")
	}
}

func TestRead_DueDiscriminatedByQueue(t *testing.T) {
	crt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name             string
		queue, typ       int32
		due, odue, odid  int64
		wantKind         IrDueKind
		wantPos          int32
		wantAtDaysOrSecs func() time.Time
	}{
		{"new", ankiQueueNew, ankiTypeNew, 42, 0, 0, DuePosition, 42, nil},
		{"learning_epoch_seconds", ankiQueueLearning, ankiTypeLearning, 1704200000, 0, 0, DueAt, 0, func() time.Time { return time.Unix(1704200000, 0).UTC() }},
		{"review_days_since_crt", ankiQueueReview, ankiTypeReview, 10, 0, 0, DueAt, 0, func() time.Time { return crt.Add(10 * 24 * time.Hour) }},
		{"type_review_in_daylearning_queue", ankiQueueDayLearning, ankiTypeReview, 3, 0, 0, DueAt, 0, func() time.Time { return crt.Add(3 * 24 * time.Hour) }},
		{"type_review_in_learning_queue", ankiQueueLearning, ankiTypeReview, 1704300000, 0, 0, DueAt, 0, func() time.Time { return time.Unix(1704300000, 0).UTC() }},
		{"held_new_position", ankiQueueSuspended, ankiTypeNew, 5, 0, 0, DuePosition, 5, nil},
		{"held_epoch_seconds", ankiQueueSchedBuried, ankiTypeReview, 2000000000, 0, 0, DueAt, 0, func() time.Time { return time.Unix(2000000000, 0).UTC() }},
		{"held_days_since_crt", ankiQueueUserBuried, ankiTypeReview, 4, 0, 0, DueAt, 0, func() time.Time { return crt.Add(4 * 24 * time.Hour) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveDue(c.queue, c.typ, c.due, c.odue, c.odid, crt)
			if got.Kind != c.wantKind {
				t.Fatalf("Kind = %v, want %v", got.Kind, c.wantKind)
			}
			switch c.wantKind {
			case DuePosition:
				if got.Position != c.wantPos {
					t.Errorf("Position = %d, want %d", got.Position, c.wantPos)
				}
			case DueAt:
				want := c.wantAtDaysOrSecs()
				if !got.At.Equal(want) {
					t.Errorf("At = %v, want %v", got.At, want)
				}
			}
		})
	}
}

func TestRead_NegativeIntervalIsSeconds(t *testing.T) {
	if got := intervalSeconds(-600); got != 600 {
		t.Errorf("intervalSeconds(-600) = %d, want 600", got)
	}
	if got := intervalSeconds(3); got != 259200 {
		t.Errorf("intervalSeconds(3) = %d, want 259200", got)
	}
}

func TestRead_TagsAndFieldSplitting(t *testing.T) {
	tags := splitTags(" tag1 tag2  tag3 ")
	if len(tags) != 3 || tags[0] != "tag1" || tags[1] != "tag2" || tags[2] != "tag3" {
		t.Errorf("splitTags = %#v", tags)
	}
	fields := splitFields("a\x1f\x1fc")
	if len(fields) != 3 || fields[0] != "a" || fields[1] != "" || fields[2] != "c" {
		t.Errorf("splitFields = %#v", fields)
	}
}

func TestRead_CardDataFSRSAndGarbage(t *testing.T) {
	spec := defaultSynthSpec(t)
	s, d := 5.5, 3.2
	spec.Cards[0].Data = mustJSON(t, ankiCardData{S: &s, D: &d})
	spec.Cards[1].Data = "{}"
	spec.Cards[2].Data = "not json"
	pkg := buildSchema11Package(t, spec)
	got := readBytes(t, pkg)

	byAnkiID := map[int64]IrCard{}
	for _, c := range got.Cards {
		byAnkiID[c.AnkiID] = c
	}
	if byAnkiID[spec.Cards[0].AnkiID].FSRS == nil {
		t.Fatal("card with valid s/d data: FSRS is nil, want populated")
	}
	if byAnkiID[spec.Cards[1].AnkiID].FSRS != nil {
		t.Error("card with {} data: FSRS should be nil")
	}
	if byAnkiID[spec.Cards[2].AnkiID].FSRS != nil {
		t.Error("card with garbage data: FSRS should be nil, not an error")
	}
}

func TestRead_NoRevlogTableIsNormal(t *testing.T) {
	spec := defaultSynthSpec(t)
	spec.Revlog = nil
	pkg := buildSchema11Package(t, spec)
	got := readBytes(t, pkg)
	if len(got.Reviews) != 0 {
		t.Errorf("Reviews = %v, want empty", got.Reviews)
	}
}

func TestRead_HomeDeckIsLowestNumberedCard(t *testing.T) {
	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	got := readBytes(t, pkg)
	var note1 *IrNote
	for i := range got.Notes {
		if got.Notes[i].AnkiID == 101 {
			note1 = &got.Notes[i]
		}
	}
	if note1 == nil {
		t.Fatal("note 101 not found")
	}
	if note1.HomeDeckAnkiID != 1 {
		t.Errorf("HomeDeckAnkiID = %d, want 1 (deck of card 201, note 101's lowest-id card)", note1.HomeDeckAnkiID)
	}
}

func TestRead_TwiceIsIdentical(t *testing.T) {
	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	first := readBytes(t, pkg)
	second := readBytes(t, pkg)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("reading the same bytes twice produced different IR:\n%+v\n!=\n%+v", first, second)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return string(b)
}

// assertIRMatches compares two IrCollections field-for-field with a helpful diff on mismatch,
// ignoring Warnings order (map iteration in importDecks etc. is not ordered, but here it is
// resolveHomeDecks's slice order, which is deterministic -- so this is a plain equality check).
func assertIRMatches(t *testing.T, got, want *IrCollection) {
	t.Helper()
	if !got.Crt.Equal(want.Crt) {
		t.Errorf("Crt = %v, want %v", got.Crt, want.Crt)
	}
	if got.SchemaVersion != want.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, want.SchemaVersion)
	}
	if !reflect.DeepEqual(sortedNoteTypes(got.NoteTypes), sortedNoteTypes(want.NoteTypes)) {
		t.Errorf("NoteTypes mismatch:\ngot:  %+v\nwant: %+v", got.NoteTypes, want.NoteTypes)
	}
	if !reflect.DeepEqual(sortedDecks(got.Decks), sortedDecks(want.Decks)) {
		t.Errorf("Decks mismatch:\ngot:  %+v\nwant: %+v", got.Decks, want.Decks)
	}
	if !reflect.DeepEqual(got.Notes, want.Notes) {
		t.Errorf("Notes mismatch:\ngot:  %+v\nwant: %+v", got.Notes, want.Notes)
	}
	if !reflect.DeepEqual(got.Cards, want.Cards) {
		t.Errorf("Cards mismatch:\ngot:  %+v\nwant: %+v", got.Cards, want.Cards)
	}
	if !reflect.DeepEqual(got.Reviews, want.Reviews) {
		t.Errorf("Reviews mismatch:\ngot:  %+v\nwant: %+v", got.Reviews, want.Reviews)
	}
}

func sortedNoteTypes(nts []IrNoteType) []IrNoteType {
	out := append([]IrNoteType(nil), nts...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].AnkiID < out[j-1].AnkiID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortedDecks(ds []IrDeck) []IrDeck {
	out := append([]IrDeck(nil), ds...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].AnkiID < out[j-1].AnkiID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
