package apkg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
)

func TestUnresolveDue_InverseOfResolveDue(t *testing.T) {
	crt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		queue int32
		typ   int32
		due   IrDue
	}{
		{"new position", ankiQueueNew, ankiTypeNew, IrDue{Kind: DuePosition, Position: 7}},
		{"learning epoch seconds", ankiQueueLearning, ankiTypeLearning, IrDue{Kind: DueAt, At: crt.Add(2*time.Hour + 30*time.Minute)}},
		{"review days since crt", ankiQueueReview, ankiTypeReview, IrDue{Kind: DueAt, At: crt.AddDate(0, 0, 5)}},
		{"day-learning days since crt", ankiQueueDayLearning, ankiTypeReview, IrDue{Kind: DueAt, At: crt.AddDate(0, 0, 12)}},
		{"suspended review, epoch seconds", ankiQueueSuspended, ankiTypeReview, IrDue{Kind: DueAt, At: crt.Add(90 * time.Minute)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := unresolveDue(tt.queue, tt.due, crt)
			got := resolveDue(tt.queue, tt.typ, raw, 0, 0, crt)
			if got.Kind != tt.due.Kind {
				t.Fatalf("resolveDue(unresolveDue(...)).Kind = %v, want %v", got.Kind, tt.due.Kind)
			}
			switch tt.due.Kind {
			case DuePosition:
				if got.Position != tt.due.Position {
					t.Errorf("Position = %d, want %d", got.Position, tt.due.Position)
				}
			case DueAt:
				if !got.At.Equal(tt.due.At) {
					t.Errorf("At = %v, want %v", got.At, tt.due.At)
				}
			}
		})
	}
}

func TestUnintervalSeconds_InverseOfIntervalSeconds(t *testing.T) {
	tests := []int64{0, 86400, 3 * 86400, 90, 600, 86400 * 30}
	for _, seconds := range tests {
		raw := unintervalSeconds(seconds)
		if got := intervalSeconds(raw); got != seconds {
			t.Errorf("intervalSeconds(unintervalSeconds(%d)) = %d, want %d", seconds, got, seconds)
		}
	}
}

func TestEncodeTags_InverseOfSplitTags(t *testing.T) {
	tests := [][]string{nil, {}, {"tag1"}, {"tag1", "tag2"}}
	for _, tags := range tests {
		got := splitTags(encodeTags(tags))
		if len(got) != len(tags) {
			t.Fatalf("splitTags(encodeTags(%v)) = %v", tags, got)
		}
		for i := range tags {
			if got[i] != tags[i] {
				t.Errorf("splitTags(encodeTags(%v))[%d] = %q, want %q", tags, i, got[i], tags[i])
			}
		}
	}
}

func TestEncodeCardData_RoundTripsThroughAnkiCardData(t *testing.T) {
	data, err := encodeCardData(&IrFSRSState{Stability: 4.5, Difficulty: 6.25})
	if err != nil {
		t.Fatalf("encodeCardData: %v", err)
	}
	var cd ankiCardData
	if err := json.Unmarshal([]byte(data), &cd); err != nil {
		t.Fatalf("unmarshalling encoded data: %v", err)
	}
	if cd.S == nil || *cd.S != 4.5 {
		t.Errorf("S = %v, want 4.5", cd.S)
	}
	if cd.D == nil || *cd.D != 6.25 {
		t.Errorf("D = %v, want 6.25", cd.D)
	}

	empty, err := encodeCardData(nil)
	if err != nil {
		t.Fatalf("encodeCardData(nil): %v", err)
	}
	if empty != "{}" {
		t.Errorf("encodeCardData(nil) = %q, want \"{}\"", empty)
	}
}

// TestExport_RoundTripsThroughReimport is CLAUDE.md §10.3's round-trip target applied to export:
// importing a package, exporting it back out, and re-importing the result into a second,
// independent user must produce equivalent decks/note types/notes/cards/scheduling state/review
// history -- not byte-identical Anki ids (Export deliberately reuses the originals when it can,
// but position/factor are documented as lossy, apkg-format.md's Export section), but the same
// content a user would recognise.
func TestExport_RoundTripsThroughReimport(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerA := seedUser(t, tx)
	now := time.Now()

	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	col1 := readBytes(t, pkg)

	if _, err := Import(ctx, tx, ownerA, col1, now, testMediaStore(t)); err != nil {
		t.Fatalf("first Import: %v", err)
	}

	col2, err := Export(ctx, tx, ownerA, now)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(col2.Decks) != len(spec.Decks) {
		t.Fatalf("Export: %d decks, want %d", len(col2.Decks), len(spec.Decks))
	}
	if len(col2.Cards) != len(spec.Cards) {
		t.Fatalf("Export: %d cards, want %d", len(col2.Cards), len(spec.Cards))
	}

	var buf bytes.Buffer
	if err := Write(col2, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	col3 := readBytes(t, buf.Bytes()) // Write's own output must re-parse cleanly

	ownerB := seedUser(t, tx)
	if _, err := Import(ctx, tx, ownerB, col3, now, testMediaStore(t)); err != nil {
		t.Fatalf("second Import: %v", err)
	}

	q := db.New(tx)

	for _, d := range spec.Decks {
		a, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerA, Name: d.Name})
		if err != nil {
			t.Fatalf("owner A deck %q: %v", d.Name, err)
		}
		b, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerB, Name: d.Name})
		if err != nil {
			t.Fatalf("owner B deck %q: %v", d.Name, err)
		}
		if a.Description != b.Description {
			t.Errorf("deck %q: description = %q, want %q", d.Name, b.Description, a.Description)
		}
	}

	for _, n := range spec.Notes {
		a, err := q.GetNoteByOwnerAndGuid(ctx, db.GetNoteByOwnerAndGuidParams{OwnerID: ownerA, Guid: n.Guid})
		if err != nil {
			t.Fatalf("owner A note %q: %v", n.Guid, err)
		}
		b, err := q.GetNoteByOwnerAndGuid(ctx, db.GetNoteByOwnerAndGuidParams{OwnerID: ownerB, Guid: n.Guid})
		if err != nil {
			t.Fatalf("owner B note %q: %v", n.Guid, err)
		}
		if string(a.Fields) != string(b.Fields) {
			t.Errorf("note %q: fields = %s, want %s", n.Guid, b.Fields, a.Fields)
		}
		if a.Checksum != b.Checksum {
			t.Errorf("note %q: checksum = %d, want %d", n.Guid, b.Checksum, a.Checksum)
		}

		cardsA, err := findCardsForNote(ctx, tx, a.ID)
		if err != nil {
			t.Fatalf("owner A cards of note %q: %v", n.Guid, err)
		}
		cardsB, err := findCardsForNote(ctx, tx, b.ID)
		if err != nil {
			t.Fatalf("owner B cards of note %q: %v", n.Guid, err)
		}
		if len(cardsA) != len(cardsB) {
			t.Fatalf("note %q: %d cards for owner A, %d for owner B", n.Guid, len(cardsA), len(cardsB))
		}

		for ordinal, cardA := range cardsA {
			cardB, ok := cardsB[ordinal]
			if !ok {
				t.Fatalf("note %q ordinal %d: no matching card for owner B", n.Guid, ordinal)
			}

			// #82: a new card's imported queue position must survive export -> reimport, not just
			// import -> DB. defaultSynthSpec's own new cards (AnkiID 202-205) already exercise
			// out-of-card-id-order positions.
			if cardA.ImportDuePosition != cardB.ImportDuePosition {
				t.Errorf("note %q ordinal %d: import_due_position = %+v, want %+v",
					n.Guid, ordinal, cardB.ImportDuePosition, cardA.ImportDuePosition)
			}

			stateA, errA := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: ownerA, CardID: cardA.ID})
			stateB, errB := q.GetUserCardState(ctx, db.GetUserCardStateParams{UserID: ownerB, CardID: cardB.ID})
			hasA := !errors.Is(errA, pgx.ErrNoRows)
			hasB := !errors.Is(errB, pgx.ErrNoRows)
			if hasA != hasB {
				t.Fatalf("note %q ordinal %d: owner A has user_card_state=%v, owner B has %v", n.Guid, ordinal, hasA, hasB)
			}
			if !hasA {
				continue
			}
			if errA != nil {
				t.Fatalf("owner A GetUserCardState: %v", errA)
			}
			if errB != nil {
				t.Fatalf("owner B GetUserCardState: %v", errB)
			}
			if stateA.Stability != stateB.Stability || stateA.Difficulty != stateB.Difficulty {
				t.Errorf("note %q ordinal %d: stability/difficulty = %v/%v, want %v/%v",
					n.Guid, ordinal, stateB.Stability, stateB.Difficulty, stateA.Stability, stateA.Difficulty)
			}
			if stateA.State != stateB.State {
				t.Errorf("note %q ordinal %d: state = %d, want %d", n.Guid, ordinal, stateB.State, stateA.State)
			}
			if !stateA.Due.Time.Equal(stateB.Due.Time) {
				t.Errorf("note %q ordinal %d: due = %v, want %v", n.Guid, ordinal, stateB.Due.Time, stateA.Due.Time)
			}
			if stateA.Reps != stateB.Reps || stateA.Lapses != stateB.Lapses {
				t.Errorf("note %q ordinal %d: reps/lapses = %d/%d, want %d/%d",
					n.Guid, ordinal, stateB.Reps, stateB.Lapses, stateA.Reps, stateA.Lapses)
			}

			reviewsA, err := q.ListReviewLogForCard(ctx, db.ListReviewLogForCardParams{CardID: cardA.ID, UserID: ownerA})
			if err != nil {
				t.Fatalf("owner A review log: %v", err)
			}
			reviewsB, err := q.ListReviewLogForCard(ctx, db.ListReviewLogForCardParams{CardID: cardB.ID, UserID: ownerB})
			if err != nil {
				t.Fatalf("owner B review log: %v", err)
			}
			if len(reviewsA) != len(reviewsB) {
				t.Fatalf("note %q ordinal %d: %d review_log rows for owner A, %d for owner B", n.Guid, ordinal, len(reviewsA), len(reviewsB))
			}
			for i := range reviewsA {
				if reviewsA[i].Rating != reviewsB[i].Rating || !reviewsA[i].ReviewedAt.Time.Equal(reviewsB[i].ReviewedAt.Time) {
					t.Errorf("note %q ordinal %d review %d: rating/reviewed_at = %d/%v, want %d/%v",
						n.Guid, ordinal, i, reviewsB[i].Rating, reviewsB[i].ReviewedAt.Time, reviewsA[i].Rating, reviewsA[i].ReviewedAt.Time)
				}
			}
		}
	}
}

// noteCard is what findCardsForNote returns for one card: its id plus its imported new-card
// queue position (#82), for TestExport_RoundTripsThroughReimport's position round-trip check.
type noteCard struct {
	ID                pgtype.UUID
	ImportDuePosition pgtype.Int4
}

// findCardsForNote returns a note's cards keyed by ordinal.
func findCardsForNote(ctx context.Context, tx pgx.Tx, noteID pgtype.UUID) (map[int32]noteCard, error) {
	rows, err := tx.Query(ctx, `SELECT id, ordinal, import_due_position FROM cards WHERE note_id = $1`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int32]noteCard{}
	for rows.Next() {
		var c noteCard
		var ordinal int32
		if err := rows.Scan(&c.ID, &ordinal, &c.ImportDuePosition); err != nil {
			return nil, err
		}
		out[ordinal] = c
	}
	return out, rows.Err()
}
