package http

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/Jolls/enshu/web"
)

func parseTemplates() (map[string]*template.Template, error) {
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "signup", "home", "settings"} {
		t, err := template.ParseFS(web.Templates, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
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
