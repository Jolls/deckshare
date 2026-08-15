package review

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	"github.com/Jolls/enshu/internal/render"
)

// StudyDay is the per-user rollover window a batch fetch is scoped to (docs/schema.md, plan
// §0.9): the arithmetic runs on the user's local wall clock, computed once by GetStudyDayWindow.
type StudyDay struct {
	Start time.Time
	End   time.Time
}

// Card is one due or never-seen card, rendered and previewed for the reviewer's queue.
type Card struct {
	CardID   pgtype.UUID
	Unseen   bool
	Question template.HTML // sanitised by render, {{type:Field}} widget already spliced
	Answer   template.HTML
	Preview  fsrs.Preview // all four branches [§2.6] -- the whole reason the client never waits
	Prior    CardStateDTO
}

// Batch is one page of the reviewer's queue: the initial batch rendered inline in the page
// response, or one GET /api/reviews/next refill.
type Batch struct {
	Cards       []Card
	Cursor      string // opaque; "" when Exhausted
	Exhausted   bool
	StudyDayEnd time.Time
}

func cardStateToDTO(s fsrs.CardState) CardStateDTO {
	dto := CardStateDTO{
		Due:           s.Due,
		State:         uint8(s.State),
		Stability:     s.Stability,
		Difficulty:    s.Difficulty,
		Reps:          s.Reps,
		Lapses:        s.Lapses,
		ScheduledDays: s.ScheduledDays,
		LearningSteps: s.LearningSteps,
	}
	if !s.LastReview.IsZero() {
		lr := s.LastReview
		dto.LastReview = &lr
	}
	return dto
}

// lastSubdeck returns the last "::" component of an Anki-style deck name.
func lastSubdeck(name string) string {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		return name[i+2:]
	}
	return name
}

// BuildBatch fetches up to limit due cards after cur and precomputes all four rating outcomes for
// each (CLAUDE.md §2.6: no FSRS ever runs in the browser). now is the fetch instant; the client's
// grade will be recomputed server-side at its own reviewedAt, so a branch going stale as
// wall-clock advances is expected (architecture.md §6), not a bug. A render error on one card
// fails the whole batch: a card that cannot render cannot be graded honestly.
func BuildBatch(ctx context.Context, store db.DBTX, p fsrs.Params, userID, deckID pgtype.UUID,
	deckName string, window StudyDay, cur Cursor, limit int32, now time.Time) (Batch, error) {
	q := db.New(store)

	rows, err := q.ListDueCardsForStudy(ctx, db.ListDueCardsForStudyParams{
		UserID:        userID,
		DeckID:        deckID,
		StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
		StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
		CursorDue:     cur.dueArg(),
		CursorCardID:  cur.cardIDArg(),
		BatchSize:     limit,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("review: list due cards: %w", err)
	}

	batch := Batch{
		Exhausted:   int32(len(rows)) < limit,
		StudyDayEnd: window.End,
	}
	if len(rows) == 0 {
		return batch, nil
	}

	noteTypeIDs := make([]pgtype.UUID, 0, len(rows))
	seen := make(map[pgtype.UUID]bool, len(rows))
	for _, r := range rows {
		if !seen[r.NoteTypeID] {
			seen[r.NoteTypeID] = true
			noteTypeIDs = append(noteTypeIDs, r.NoteTypeID)
		}
	}
	fieldRows, err := q.ListFieldsForNoteTypes(ctx, noteTypeIDs)
	if err != nil {
		return Batch{}, fmt.Errorf("review: list fields: %w", err)
	}
	fieldsByNoteType := make(map[pgtype.UUID][]db.ListFieldsForNoteTypesRow, len(noteTypeIDs))
	for _, f := range fieldRows {
		fieldsByNoteType[f.NoteTypeID] = append(fieldsByNoteType[f.NoteTypeID], f)
	}

	cards := make([]Card, 0, len(rows))
	for _, r := range rows {
		var fieldValues []string
		if err := json.Unmarshal(r.NoteFields, &fieldValues); err != nil {
			return Batch{}, fmt.Errorf("review: unmarshal note fields for card %s: %w", r.CardID.String(), err)
		}
		noteFields := fieldsByNoteType[r.NoteTypeID]
		fields := make([]render.Field, 0, len(noteFields))
		for _, f := range noteFields {
			var value string
			if int(f.Ordinal) < len(fieldValues) {
				value = fieldValues[f.Ordinal]
			}
			fields = append(fields, render.Field{Name: f.Name, Value: value})
		}

		note := render.Note{
			Fields:   fields,
			Tags:     r.NoteTags,
			NoteType: r.NoteTypeName,
			Deck:     deckName,
			Subdeck:  lastSubdeck(deckName),
		}
		tmpl := render.Template{Name: r.TemplateName, Qfmt: r.Qfmt, Afmt: r.Afmt}
		rendered, err := render.RenderCard(tmpl, note, r.CardOrdinal, r.IsCloze)
		if err != nil {
			return Batch{}, fmt.Errorf("review: render card %s: %w", r.CardID.String(), err)
		}

		prior := fsrs.CardState{
			Due:           r.Due.Time,
			Stability:     r.Stability,
			Difficulty:    r.Difficulty,
			State:         fsrs.State(r.State),
			Reps:          r.Reps,
			Lapses:        r.Lapses,
			ScheduledDays: r.ScheduledDays,
			LearningSteps: r.LearningSteps,
		}
		if r.LastReview.Valid {
			prior.LastReview = r.LastReview.Time
		}
		preview, err := fsrs.PreviewAll(p, prior, now)
		if err != nil {
			return Batch{}, fmt.Errorf("review: preview card %s: %w", r.CardID.String(), err)
		}

		cards = append(cards, Card{
			CardID:   r.CardID,
			Unseen:   r.Unseen,
			Question: render.TypeAnswerInput(rendered.Question),
			Answer:   render.TypeAnswerExpected(rendered.Answer),
			Preview:  preview,
			Prior:    cardStateToDTO(prior),
		})
	}
	batch.Cards = cards

	if !batch.Exhausted {
		last := rows[len(rows)-1]
		next := Cursor{CardID: last.CardID}
		if last.QueueKey.InfinityModifier == pgtype.Infinity {
			next.Infinite = true
		} else {
			next.Due = last.QueueKey.Time.UTC()
		}
		batch.Cursor = EncodeCursor(next)
	}

	return batch, nil
}
