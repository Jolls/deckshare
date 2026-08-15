package http

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Jolls/enshu/internal/auth"
	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	"github.com/Jolls/enshu/internal/review"
)

func registerDeckRoutes(mux *http.ServeMux, store db.Beginner, pages map[string]*template.Template) {
	mux.Handle("GET /decks", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		decks, err := db.New(store).ListDecksForUser(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		render(w, pages["decks"], http.StatusOK, map[string]any{"User": user, "Decks": decks})
	})))

	mux.Handle("GET /decks/new", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		render(w, pages["deck_new"], http.StatusOK, map[string]any{"User": user})
	})))

	mux.Handle("POST /decks", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		description := r.PostForm.Get("description")
		if name == "" || len(name) > 200 {
			render(w, pages["deck_new"], http.StatusBadRequest, map[string]any{
				"User": user, "Name": name, "Description": description,
				"Error": "Name must be between 1 and 200 characters",
			})
			return
		}

		tx, err := store.Begin(r.Context())
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		deck, err := db.CreateDeckWithAccess(r.Context(), tx, user.ID, name, description)
		if err != nil {
			if db.IsUniqueViolation(err, "decks_owner_id_name_key") {
				render(w, pages["deck_new"], http.StatusConflict, map[string]any{
					"User": user, "Name": name, "Description": description,
					"Error": "You already have a deck with that name",
				})
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/decks/"+deck.ID.String(), http.StatusSeeOther)
	})))

	mux.Handle("GET /decks/{id}", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		q := db.New(store)
		deck, err := q.GetDeckForUser(r.Context(), db.GetDeckForUserParams{UserID: user.ID, DeckID: deckID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFound(w)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		counts, err := q.CountDeckContents(r.Context(), db.CountDeckContentsParams{DeckID: deckID, UserID: user.ID})
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		notes, err := q.ListNotesInDeck(r.Context(), db.ListNotesInDeckParams{UserID: user.ID, DeckID: deckID})
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		params, err := review.EffectiveParams(r.Context(), q, user.ID, deckID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		render(w, pages["deck"], http.StatusOK, map[string]any{
			"User": user, "Deck": deck, "Counts": counts, "Notes": notes,
			"DesiredRetention": params.DesiredRetention(),
		})
	})))

	mux.Handle("GET /decks/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		deck, err := db.New(store).GetDeckForSettingsEdit(r.Context(), db.GetDeckForSettingsEditParams{UserID: user.ID, DeckID: deckID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFound(w)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		render(w, pages["deck_edit"], http.StatusOK, map[string]any{"User": user, "Deck": deck})
	})))

	mux.Handle("POST /decks/{id}/edit", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		description := r.PostForm.Get("description")
		if name == "" || len(name) > 200 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		n, err := db.New(store).UpdateDeck(r.Context(), db.UpdateDeckParams{
			Name: name, Description: description, DeckID: deckID, UserID: user.ID,
		})
		if err != nil {
			if db.IsUniqueViolation(err, "decks_owner_id_name_key") {
				http.Error(w, "a deck with that name already exists", http.StatusConflict)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if n == 0 {
			notFound(w)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/delete", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if err := deleteDeck(r.Context(), store, deckID, user.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFound(w)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/decks", http.StatusSeeOther)
	})))

	mux.Handle("POST /decks/{id}/settings/fsrs", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFromContext(r.Context())
		deckID, ok := pathUUID(r, "id")
		if !ok {
			notFound(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		retention, atoiErr := strconv.ParseFloat(r.PostForm.Get("desired_retention"), 64)
		if atoiErr != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		params, err := fsrs.NewDefaultParams(retention)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		n, err := db.New(store).UpsertDeckFsrsRetention(r.Context(), db.UpsertDeckFsrsRetentionParams{
			UserID: user.ID, DeckID: deckID, FsrsVersion: int16(params.Version()), DesiredRetention: retention,
		})
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if n == 0 {
			notFound(w)
			return
		}
		http.Redirect(w, r, "/decks/"+deckID.String(), http.StatusSeeOther)
	})))
}

func deleteDeck(ctx context.Context, store db.Beginner, deckID, userID pgtype.UUID) error {
	tx, err := store.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := db.DeleteDeck(ctx, tx, deckID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
