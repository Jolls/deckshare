package render

import (
	"errors"
	"testing"
)

func TestTokenise(t *testing.T) {
	toks, err := tokenise("Q: {{Front}} {{#Extra}}({{Extra}}){{/Extra}} {{^Extra}}(none){{/Extra}}")
	if err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	var kinds []kind
	for _, tok := range toks {
		kinds = append(kinds, tok.kind)
	}
	want := []kind{kindText, kindField, kindText, kindOpen, kindText, kindField, kindText, kindClose, kindText, kindOpenNeg, kindText, kindClose}
	if len(kinds) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(kinds), len(want), toks)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("token %d kind = %v, want %v", i, kinds[i], want[i])
		}
	}
}

func TestTokenise_FilterSplitting(t *testing.T) {
	toks, err := tokenise("{{text:cloze:Text}}")
	if err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	if len(toks) != 1 || toks[0].kind != kindField {
		t.Fatalf("got %+v", toks)
	}
	if toks[0].name != "Text" {
		t.Errorf("name = %q, want Text", toks[0].name)
	}
	if len(toks[0].filters) != 2 || toks[0].filters[0] != "text" || toks[0].filters[1] != "cloze" {
		t.Errorf("filters = %v, want [text cloze]", toks[0].filters)
	}
}

func TestTokenise_CommentDropped(t *testing.T) {
	toks, err := tokenise("a{{!a comment}}b")
	if err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	if len(toks) != 2 {
		t.Fatalf("got %d tokens, want 2 (comment dropped): %+v", len(toks), toks)
	}
}

func TestTokenise_EmptyTagIsLiteral(t *testing.T) {
	toks, err := tokenise("a{{}}b{{ }}c")
	if err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	var buf string
	for _, tok := range toks {
		if tok.kind != kindText {
			t.Fatalf("expected all-text tokens, got %+v", tok)
		}
		buf += tok.text
	}
	if buf != "a{{}}b{{ }}c" {
		t.Errorf("buf = %q", buf)
	}
}

func TestTokenise_UnterminatedTag(t *testing.T) {
	_, err := tokenise("a{{Front")
	if !errors.Is(err, ErrUnterminatedTag) {
		t.Fatalf("err = %v, want ErrUnterminatedTag", err)
	}
}

func TestTokenise_SectionOffsets(t *testing.T) {
	toks, err := tokenise("{{#X}}")
	if err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	if toks[0].name != "X" {
		t.Errorf("name = %q, want X", toks[0].name)
	}
}
