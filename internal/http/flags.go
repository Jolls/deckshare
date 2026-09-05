package http

import (
	"html/template"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/deckshare/internal/auth"
	"github.com/Jolls/deckshare/internal/db"
)

// #207: a student flags a card mid-review with a text comment; the deck owner (or anyone else
// holding can_view_flags) reviews and resolves it. can_view_flags is deck_access's eighth
// independent permission, deliberately separate from can_view_progress -- see migration
// 00019_card_flags.sql.
const (
	flagStatusOpen     = "open"
	flagStatusResolved = "resolved"

	maxFlagCommentLen = 2000 // generous bound on a plain-text feedback comment, in runes -- matches
	// the textarea's maxlength (review.html), so a comment the client accepted never gets a
	// surprise server-side rejection just because it uses multi-byte characters
)

// flagRow is one row of flags.html, folded from db.ListFlagsForDeckRow -- CreatedAt is
// pre-formatted here (mirrors studentProgressRow's *Display fields, progress.go) since
// pgtype.Timestamptz has no Format method for the template to call directly.
type flagRow struct {
	ID                   pgtype.UUID
	NoteID               pgtype.UUID
	Label                string
	Comment              string
	FlaggedByEmail       string
	FlaggedByDisplayName string
	CreatedDisplay       string
}

func registerFlagRoutes(mux *http.ServeMux, store db.Beginner, pages, fragments map[string]*template.Template) {
	// Called from inside the reviewer's #review-stage (review.html/review.js) via hx-post. cardId
	// rides as a form field rather than a path segment -- the reviewer only knows the current
	// card client-side, at request time, so a static hx-post target is simpler than templating
	// the path per card. Answers a fragment, never a redirect: a redirect would drop the client
	// mid review-session (CLAUDE.md §2.6 -- flagging must stay decoupled from grading, but it
	// still must not navigate the page away from the reviewer).
	mux.Handle("POST /decks/{id}/cards/flags", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if !parseForm(w, r) {
			return
		}
		var cardID pgtype.UUID
		if err := cardID.Scan(r.PostForm.Get("cardId")); err != nil {
			badRequest(w)
			return
		}
		comment := strings.TrimSpace(r.PostForm.Get("comment"))
		if comment == "" || utf8.RuneCountInString(comment) > maxFlagCommentLen {
			badRequest(w)
			return
		}

		q := db.New(store)
		card, err := q.GetCardForFlag(r.Context(), db.GetCardForFlagParams{UserID: user.ID, CardID: cardID, DeckID: deckID})
		if handleQueryErr(w, err) {
			return
		}
		if err := q.UpsertCardFlag(r.Context(), db.UpsertCardFlagParams{
			CardID: card.ID, DeckID: card.DeckID, FlaggedByUserID: user.ID, Comment: comment,
		}); err != nil {
			serverError(w)
			return
		}
		renderFragment(w, fragments["flag_status"], http.StatusOK, "flag_status", map[string]any{"Flagged": true})
	})))

	mux.Handle("GET /decks/{id}/flags", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForFlags(r.Context(), db.GetDeckForFlagsParams{UserID: user.ID, DeckID: deckID})
		if handleQueryErrPage(w, pages, user, err) {
			return
		}

		status := flagStatusOpen
		if r.URL.Query().Get("status") == flagStatusResolved {
			status = flagStatusResolved
		}
		rows, err := q.ListFlagsForDeck(r.Context(), db.ListFlagsForDeckParams{UserID: user.ID, DeckID: deckID, Status: status})
		if err != nil {
			serverError(w)
			return
		}
		flags := make([]flagRow, len(rows))
		for i, row := range rows {
			flags[i] = flagRow{
				ID: row.ID, NoteID: row.NoteID, Label: row.Label, Comment: row.Comment,
				FlaggedByEmail: row.FlaggedByEmail, FlaggedByDisplayName: row.FlaggedByDisplayName,
				CreatedDisplay: row.CreatedAt.Time.Format("2006-01-02 15:04"),
			}
		}
		render(w, pages["flags"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "Flags": flags, "Status": status,
		})
	})))

	mux.Handle("POST /decks/{id}/flags/{flagId}/resolve", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		flagID, ok := pathUUID(r, "flagId")
		if !ok {
			notFoundPage(w, pages, user)
			return
		}
		q := db.New(store)
		rows, err := q.ResolveCardFlag(r.Context(), db.ResolveCardFlagParams{
			ResolvedByUserID: user.ID, FlagID: flagID, DeckID: deckID,
		})
		if err != nil {
			serverError(w)
			return
		}
		if rows == 0 {
			notFoundPage(w, pages, user)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String()+"/flags", http.StatusSeeOther)
	})))
}
