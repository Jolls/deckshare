package http

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Jolls/deckshare/internal/db"
)

// Logged-out pages pass no User, so they never carry a data-theme (#268): Pico follows the OS.
func TestLayout_LoggedOutPagesHaveNoThemeAttr(t *testing.T) {
	pages, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	for _, name := range []string{"login", "signup", "reset_password"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := pages[name].ExecuteTemplate(&buf, "layout", map[string]any{"Invalid": true}); err != nil {
				t.Fatalf("execute %s: %v", name, err)
			}
			body := buf.String()
			if !strings.Contains(body, `<html lang="en">`) {
				t.Errorf("%s: want plain <html lang=\"en\">", name)
			}
			if strings.Contains(body, "data-theme") {
				t.Errorf("%s: logged-out page must not carry data-theme", name)
			}
		})
	}
}

func TestLayout_DataThemeAttr(t *testing.T) {
	pages, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	tests := []struct{ scheme, want string }{
		{"auto", `<html lang="en">`},
		{"", `<html lang="en">`},
		{"light", `<html lang="en" data-theme="light">`},
		{"dark", `<html lang="en" data-theme="dark">`},
	}
	for _, tt := range tests {
		t.Run("scheme="+tt.scheme, func(t *testing.T) {
			var buf bytes.Buffer
			if err := pages["not_found"].ExecuteTemplate(&buf, "layout", map[string]any{"User": db.User{ColorScheme: tt.scheme}}); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("body missing %s", tt.want)
			}
		})
	}
}
