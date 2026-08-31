package http

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/Jolls/enshu/web"
)

// pagePartials lists extra template files a page needs beyond layout.html + <name>.html --
// "review" pulls in the hidden-card partial shared with the refill fragment (§0.12 of
// docs/plans/56-reviewer-batch-grading.md), so the two can never draw different markup for the
// same card shape.
var pagePartials = map[string][]string{
	"review":    {"templates/review_cards.html", "templates/back_to_deck.html"},
	"study":     {"templates/review_cards.html", "templates/back_to_decks.html"},
	"deck_edit": {"templates/back_to_deck.html"},
	"access":    {"templates/messages.html", "templates/back_to_deck.html"},
	"login":     {"templates/messages.html"},
	"signup":    {"templates/messages.html"},
	"settings":  {"templates/messages.html"},
	"deck_new":  {"templates/messages.html", "templates/back_to_decks.html"},
	"import":    {"templates/messages.html", "templates/back_to_decks.html"},
	"import_ai": {"templates/back_to_decks.html", "templates/no_note_types.html"},
	"note_form": {"templates/no_note_types.html"},
}

func parseTemplates() (map[string]*template.Template, error) {
	pages := map[string]*template.Template{}
	for _, name := range []string{
		"login", "signup", "settings",
		"decks", "deck_new", "deck", "deck_edit", "access",
		"notetypes", "notetype_form", "note_form",
		"review", "study", "import", "import_ai",
	} {
		files := append([]string{"templates/layout.html", "templates/" + name + ".html"}, pagePartials[name]...)
		t, err := template.ParseFS(web.Templates, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// parseFragments parses templates that render standalone, without the layout -- htmx refill
// responses (internal/http/review.go).
func parseFragments() (map[string]*template.Template, error) {
	fragments := map[string]*template.Template{}
	for _, name := range []string{"review_cards"} {
		t, err := template.ParseFS(web.Templates, "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s fragment: %w", name, err)
		}
		fragments[name] = t
	}
	return fragments, nil
}

func render(w http.ResponseWriter, t *template.Template, status int, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("render template: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderFragment executes a standalone fragment template (no layout) -- htmx refill responses.
func renderFragment(w http.ResponseWriter, t *template.Template, status int, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("render fragment: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
