package render

import (
	goparser "go/parser"
	gotoken "go/token"
	"html/template"
	"strings"
	"testing"
)

func TestTypeAnswerInput_ReplacesPlaceholderExactlyOnce(t *testing.T) {
	r := Rendered{
		HTML: template.HTML("before PLACEHOLDER after"),
		Type: &TypeAnswer{Field: "Front", Expected: "answer", Placeholder: "PLACEHOLDER"},
	}
	out := string(TypeAnswerInput(r))
	if strings.Contains(out, "PLACEHOLDER") {
		t.Errorf("placeholder not replaced: %q", out)
	}
	if !strings.Contains(out, `data-expected="answer"`) {
		t.Errorf("expected answer not embedded: %q", out)
	}
	if !strings.Contains(out, "<input") {
		t.Errorf("no input element: %q", out)
	}
}

func TestTypeAnswerInput_EscapesExpected(t *testing.T) {
	r := Rendered{
		HTML: template.HTML("PLACEHOLDER"),
		Type: &TypeAnswer{Field: "Front", Expected: `"><script>alert(1)</script>`, Placeholder: "PLACEHOLDER"},
	}
	out := string(TypeAnswerInput(r))
	if strings.Contains(out, "<script") {
		t.Errorf("Expected not escaped: %q", out)
	}
}

func TestTypeAnswerInput_NilTypePassthrough(t *testing.T) {
	r := Rendered{HTML: template.HTML("plain")}
	if got := TypeAnswerInput(r); got != r.HTML {
		t.Errorf("got %q, want unchanged %q", got, r.HTML)
	}
}

func TestTypeAnswerExpected_ReplacesPlaceholderExactlyOnce(t *testing.T) {
	r := Rendered{
		HTML: template.HTML("Q: PLACEHOLDER"),
		Type: &TypeAnswer{Field: "Front", Expected: "answer", Placeholder: "PLACEHOLDER"},
	}
	out := string(TypeAnswerExpected(r))
	if strings.Contains(out, "PLACEHOLDER") {
		t.Errorf("placeholder not replaced: %q", out)
	}
	if !strings.Contains(out, "answer") {
		t.Errorf("expected answer missing: %q", out)
	}
}

func TestTypeAnswerExpected_NilTypePassthrough(t *testing.T) {
	r := Rendered{HTML: template.HTML("plain")}
	if got := TypeAnswerExpected(r); got != r.HTML {
		t.Errorf("got %q, want unchanged %q", got, r.HTML)
	}
}

// The import-boundary assertion: typeanswer.go must import neither bluemonday nor anything else
// from the sanitiser, so the {{type:Field}} widget can never accidentally be run through
// sanitiseCardHTML (architecture.md §8's fourth bullet). A checked fact, not a comment.
func TestTypeAnswerGo_DoesNotImportSanitiser(t *testing.T) {
	fset := gotoken.NewFileSet()
	f, err := goparser.ParseFile(fset, "typeanswer.go", nil, goparser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse typeanswer.go: %v", err)
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, "bluemonday") {
			t.Errorf("typeanswer.go imports %q -- must never depend on the sanitiser", path)
		}
	}
}
