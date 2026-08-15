package apkg

// Round-trip: the export half is #59; this file covers import determinism only
// (docs/plans/58-apkg-import.md §8).

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestRead_Schema18_FailsUntilVerified(t *testing.T) {
	spec := defaultSynthSpec(t)
	pkg := buildSchema18Package(t, spec)
	_, err := Read(bytes.NewReader(pkg), int64(len(pkg)), DefaultArchiveLimits())
	if err == nil {
		t.Fatal("Read of a schema-18 package succeeded; want ErrSchema18Config (see #61, docs/plans/58-apkg-import.md §10.1)")
	}
	if !errors.Is(err, ErrSchema18Config) {
		t.Fatalf("Read error = %v, want ErrSchema18Config", err)
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
