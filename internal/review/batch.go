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
}

// Batch is one page of the reviewer's queue: the initial batch rendered inline in the page
// response, or one GET /api/reviews/next refill.
type Batch struct {
	Cards     []Card
	Cursor    string // opaque; "" when Exhausted
	Exhausted bool
}

// queueRow is the common shape BuildBatch's two source shapes -- ListDueCardsForStudyRow (the
// single-query path) and ListReviewCardsForStudyRow/ListNewCardsForStudyRow (the mixed-mode
// path, #116) -- get normalised into before rendering, so renderQueueRows has exactly one row
// shape to work with regardless of which query produced it. SortKey is only meaningful for
// review-ish rows (used to build the next single-query or review sub-cursor); zero on a new row.
type queueRow struct {
	CardID        pgtype.UUID
	CardOrdinal   int32
	Unseen        bool
	Due           pgtype.Timestamptz
	Stability     float64
	Difficulty    float64
	State         int16
	Reps          int32
	Lapses        int32
	ScheduledDays int32
	LearningSteps int16
	LastReview    pgtype.Timestamptz
	NoteFields    []byte
	NoteTags      []string
	NoteTypeID    pgtype.UUID
	NoteTypeName  string
	IsCloze       bool
	TemplateName  string
	Qfmt          string
	Afmt          string
	SortKey       float64
}

func queueRowFromDue(r db.ListDueCardsForStudyRow) queueRow {
	return queueRow{
		CardID: r.CardID, CardOrdinal: r.CardOrdinal, Unseen: r.Unseen, Due: r.Due,
		Stability: r.Stability, Difficulty: r.Difficulty, State: r.State, Reps: r.Reps,
		Lapses: r.Lapses, ScheduledDays: r.ScheduledDays, LearningSteps: r.LearningSteps,
		LastReview: r.LastReview, NoteFields: r.NoteFields, NoteTags: r.NoteTags,
		NoteTypeID: r.NoteTypeID, NoteTypeName: r.NoteTypeName, IsCloze: r.IsCloze,
		TemplateName: r.TemplateName, Qfmt: r.Qfmt, Afmt: r.Afmt, SortKey: r.SortKey,
	}
}

func queueRowFromReview(r db.ListReviewCardsForStudyRow) queueRow {
	return queueRow{
		CardID: r.CardID, CardOrdinal: r.CardOrdinal, Unseen: false, Due: r.Due,
		Stability: r.Stability, Difficulty: r.Difficulty, State: r.State, Reps: r.Reps,
		Lapses: r.Lapses, ScheduledDays: r.ScheduledDays, LearningSteps: r.LearningSteps,
		LastReview: r.LastReview, NoteFields: r.NoteFields, NoteTags: r.NoteTags,
		NoteTypeID: r.NoteTypeID, NoteTypeName: r.NoteTypeName, IsCloze: r.IsCloze,
		TemplateName: r.TemplateName, Qfmt: r.Qfmt, Afmt: r.Afmt, SortKey: r.SortKey,
	}
}

func queueRowFromNew(r db.ListNewCardsForStudyRow) queueRow {
	return queueRow{
		CardID: r.CardID, CardOrdinal: r.CardOrdinal, Unseen: true, Due: r.Due,
		Stability: r.Stability, Difficulty: r.Difficulty, State: r.State, Reps: r.Reps,
		Lapses: r.Lapses, ScheduledDays: r.ScheduledDays, LearningSteps: r.LearningSteps,
		NoteFields: r.NoteFields, NoteTags: r.NoteTags,
		NoteTypeID: r.NoteTypeID, NoteTypeName: r.NoteTypeName, IsCloze: r.IsCloze,
		TemplateName: r.TemplateName, Qfmt: r.Qfmt, Afmt: r.Afmt,
	}
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

// hashSeedFor is the salt for rev.order='random' (#116): stable within a study day (so pagination
// across refills doesn't skip/repeat cards) and per-user (so two users studying the same shared
// deck don't get identical shuffles), reshuffling the next study day.
func hashSeedFor(userID pgtype.UUID, window StudyDay) string {
	return userID.String() + "|" + window.Start.UTC().Format(time.RFC3339Nano)
}

// BuildBatch fetches up to limit due cards after cur and precomputes all four rating outcomes for
// each (CLAUDE.md §2.6: no FSRS ever runs in the browser). now is the fetch instant; the client's
// grade will be recomputed server-side at its own reviewedAt, so a branch going stale as
// wall-clock advances is expected (architecture.md §6), not a bug. A render error on one card
// fails the whole batch: a card that cannot render cannot be graded honestly.
//
// New (never-seen) cards are capped at the deck's preset new/perDay minus the introductions
// already logged this study day (#101); review-state cards are capped independently at the
// deck's preset rev/perDay minus reviews already logged today (#115). Learning/relearning cards
// are never capped. order and mix are the deck's preset rev/order and new/mix (#116); mix ==
// NewMixMixed branches to the two-query interleave path, everything else to the single query.
func BuildBatch(ctx context.Context, store db.DBTX, p fsrs.Params, userID, deckID pgtype.UUID,
	deckName string, window StudyDay, newPerDay, revPerDay int32, order RevOrder, mix NewMix,
	cur Cursor, limit int32, now time.Time) (Batch, error) {
	q := db.New(store)

	introduced, err := q.CountNewIntroducedToday(ctx, db.CountNewIntroducedTodayParams{
		UserID:        userID,
		DeckID:        deckID,
		StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
		StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
	})
	if err != nil {
		return Batch{}, fmt.Errorf("review: count new introduced today: %w", err)
	}

	reviewed, err := q.CountReviewedToday(ctx, db.CountReviewedTodayParams{
		UserID:        userID,
		DeckID:        deckID,
		StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
		StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
	})
	if err != nil {
		return Batch{}, fmt.Errorf("review: count reviewed today: %w", err)
	}

	newRemaining := NewRemaining(newPerDay, introduced)
	revRemaining := RevRemaining(revPerDay, reviewed)

	if mix == NewMixMixed {
		return buildMixedBatch(ctx, q, p, userID, deckID, deckName, window, order, newRemaining, revRemaining, cur, limit, now)
	}
	return buildSingleBatch(ctx, q, p, userID, deckID, deckName, window, order, mix, newRemaining, revRemaining, cur, limit, now)
}

// buildSingleBatch is BuildBatch's path for every new.mix mode except "mixed": one keyset query,
// one combined (sort_key, card_id) cursor (#116 doc comment on ListDueCardsForStudy).
func buildSingleBatch(ctx context.Context, q *db.Queries, p fsrs.Params, userID, deckID pgtype.UUID,
	deckName string, window StudyDay, order RevOrder, mix NewMix, newRemaining, revRemaining int32,
	cur Cursor, limit int32, now time.Time) (Batch, error) {
	rows, err := q.ListDueCardsForStudy(ctx, db.ListDueCardsForStudyParams{
		NewMix:         string(mix),
		RevOrder:       string(order),
		HashSeed:       hashSeedFor(userID, window),
		UserID:         userID,
		DeckID:         deckID,
		StudyDayStart:  pgtype.Timestamptz{Time: window.Start, Valid: true},
		StudyDayEnd:    pgtype.Timestamptz{Time: window.End, Valid: true},
		RevRemaining:   revRemaining,
		NewRemaining:   newRemaining,
		CursorGroupBit: cur.groupBitArg(),
		CursorKey:      cur.keyArg(),
		CursorCardID:   cur.cardIDArg(),
		BatchSize:      limit,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("review: list due cards: %w", err)
	}

	batch := Batch{
		Exhausted: int32(len(rows)) < limit,
	}
	if len(rows) == 0 {
		return batch, nil
	}

	queueRows := make([]queueRow, len(rows))
	for i, r := range rows {
		queueRows[i] = queueRowFromDue(r)
	}
	cards, err := renderQueueRows(ctx, q, deckID, deckName, p, now, queueRows)
	if err != nil {
		return Batch{}, err
	}
	batch.Cards = cards

	if !batch.Exhausted {
		last := rows[len(rows)-1]
		batch.Cursor = EncodeCursor(Cursor{GroupBit: last.GroupBit, Key: last.SortKey, CardID: last.CardID})
	}
	return batch, nil
}

// buildMixedBatch is BuildBatch's path for new.mix = "mixed" (#116): two independent keyset
// fetches, each capped at limit, interleaved by their fetched counts (not a cross-fetch running
// total -- see interleave's doc comment) into up to limit cards. Exhausted only once both
// sub-fetches come back undersized. A source that contributed zero picks this round keeps its
// incoming sub-cursor position unchanged (carryRevCursor/carryNewCursor) rather than resetting to
// start, since nothing about it was consumed.
func buildMixedBatch(ctx context.Context, q *db.Queries, p fsrs.Params, userID, deckID pgtype.UUID,
	deckName string, window StudyDay, order RevOrder, newRemaining, revRemaining int32,
	cur Cursor, limit int32, now time.Time) (Batch, error) {
	hashSeed := hashSeedFor(userID, window)

	reviewRows, err := q.ListReviewCardsForStudy(ctx, db.ListReviewCardsForStudyParams{
		RevOrder:      string(order),
		HashSeed:      hashSeed,
		UserID:        userID,
		DeckID:        deckID,
		StudyDayStart: pgtype.Timestamptz{Time: window.Start, Valid: true},
		StudyDayEnd:   pgtype.Timestamptz{Time: window.End, Valid: true},
		RevRemaining:  revRemaining,
		CursorKey:     cur.revKeyArg(),
		CursorCardID:  cur.revCardIDArg(),
		BatchSize:     limit,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("review: list review cards: %w", err)
	}

	newRows, err := q.ListNewCardsForStudy(ctx, db.ListNewCardsForStudyParams{
		UserID:       userID,
		DeckID:       deckID,
		NewRemaining: newRemaining,
		CursorCardID: cur.newCardIDArg(),
		BatchSize:    limit,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("review: list new cards: %w", err)
	}

	// Exhausted requires both sub-fetches undersized (each returned fewer than limit rows, so
	// neither has more rows behind it) AND their combined count not exceeding limit: an
	// undersized-but-still-combined-oversized pair (e.g. 15 review + 15 new against a limit of
	// 20) still has interleave truncating the display to limit, leaving fetched-but-unpicked
	// rows for the next refill to pick up via the carried-forward sub-cursors.
	combined := len(reviewRows) + len(newRows)
	batch := Batch{
		Exhausted: int32(len(reviewRows)) < limit && int32(len(newRows)) < limit && int32(combined) <= limit,
	}

	picks := interleave(len(reviewRows), len(newRows), int(limit))
	if len(picks) == 0 {
		return batch, nil
	}

	queueRows := make([]queueRow, 0, len(picks))
	revTaken, newTaken := 0, 0
	for _, wantReview := range picks {
		if wantReview {
			queueRows = append(queueRows, queueRowFromReview(reviewRows[revTaken]))
			revTaken++
		} else {
			queueRows = append(queueRows, queueRowFromNew(newRows[newTaken]))
			newTaken++
		}
	}

	cards, err := renderQueueRows(ctx, q, deckID, deckName, p, now, queueRows)
	if err != nil {
		return Batch{}, err
	}
	batch.Cards = cards

	if !batch.Exhausted {
		next := Cursor{Mixed: true}
		if revTaken > 0 {
			last := reviewRows[revTaken-1]
			next.RevKey, next.RevCardID = last.SortKey, last.CardID
		} else {
			next.RevAtStart, next.RevKey, next.RevCardID = carryRevCursor(cur)
		}
		if newTaken > 0 {
			next.NewCardID = newRows[newTaken-1].CardID
		} else {
			next.NewAtStart, next.NewCardID = carryNewCursor(cur)
		}
		batch.Cursor = EncodeCursor(next)
	}
	return batch, nil
}

// carryRevCursor and carryNewCursor forward cur's sub-cursor position unchanged, for the source
// that contributed zero picks in this fetch (buildMixedBatch). cur.AtStart -- the zero Cursor,
// before any Mixed field was ever set -- means "fresh" for both sub-cursors, same as
// Cursor.revKeyArg/newCardIDArg treat it.
func carryRevCursor(cur Cursor) (atStart bool, key float64, id pgtype.UUID) {
	if cur.AtStart {
		return true, 0, pgtype.UUID{}
	}
	return cur.RevAtStart, cur.RevKey, cur.RevCardID
}

func carryNewCursor(cur Cursor) (atStart bool, id pgtype.UUID) {
	if cur.AtStart {
		return true, pgtype.UUID{}
	}
	return cur.NewAtStart, cur.NewCardID
}

// renderQueueRows turns fetched rows into rendered, sanitised, FSRS-previewed Cards -- shared by
// both BuildBatch paths so there is exactly one render/preview implementation regardless of which
// query (or two queries, interleaved) produced the rows.
func renderQueueRows(ctx context.Context, q *db.Queries, deckID pgtype.UUID, deckName string,
	p fsrs.Params, now time.Time, rows []queueRow) ([]Card, error) {
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
		return nil, fmt.Errorf("review: list fields: %w", err)
	}
	fieldsByNoteType := make(map[pgtype.UUID][]db.ListFieldsForNoteTypesRow, len(noteTypeIDs))
	for _, f := range fieldRows {
		fieldsByNoteType[f.NoteTypeID] = append(fieldsByNoteType[f.NoteTypeID], f)
	}

	mediaRefs, err := q.ListMediaRefsForDeck(ctx, deckID)
	if err != nil {
		return nil, fmt.Errorf("review: list media refs: %w", err)
	}
	mediaByFilename := make(map[string]string, len(mediaRefs))
	for _, m := range mediaRefs {
		mediaByFilename[m.Filename] = m.Sha256
	}
	resolveMedia := func(filename string) (string, bool) {
		sha, ok := mediaByFilename[filename]
		return sha, ok
	}

	cards := make([]Card, 0, len(rows))
	for _, r := range rows {
		var fieldValues []string
		if err := json.Unmarshal(r.NoteFields, &fieldValues); err != nil {
			return nil, fmt.Errorf("review: unmarshal note fields for card %s: %w", r.CardID.String(), err)
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
			return nil, fmt.Errorf("review: render card %s: %w", r.CardID.String(), err)
		}
		rendered.Question.HTML = template.HTML(render.RewriteMediaSrcs(string(rendered.Question.HTML), resolveMedia))
		rendered.Answer.HTML = template.HTML(render.RewriteMediaSrcs(string(rendered.Answer.HTML), resolveMedia))

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
			return nil, fmt.Errorf("review: preview card %s: %w", r.CardID.String(), err)
		}

		cards = append(cards, Card{
			CardID:   r.CardID,
			Unseen:   r.Unseen,
			Question: render.TypeAnswerInput(rendered.Question),
			Answer:   render.TypeAnswerExpected(rendered.Answer),
			Preview:  preview,
		})
	}
	return cards, nil
}
