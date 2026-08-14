package render

import (
	"errors"
	"strings"
	"testing"
)

func note(fields ...Field) Note {
	return Note{Fields: fields, Tags: []string{"tag1", "tag2"}, NoteType: "Basic", Deck: "Parent::Child", Subdeck: "Child"}
}

func TestRenderCard_FieldPlain(t *testing.T) {
	c, err := RenderCard(Template{Name: "Card 1", Qfmt: "{{Front}}", Afmt: "{{Back}}"},
		note(Field{"Front", "hello"}, Field{"Back", "world"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "hello" {
		t.Errorf("question = %q", c.Question.HTML)
	}
	if string(c.Answer.HTML) != "world" {
		t.Errorf("answer = %q", c.Answer.HTML)
	}
}

func TestRenderCard_FieldHTMLPassthrough(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{Front}}", Afmt: "{{Back}}"},
		note(Field{"Front", "<b>bold</b>"}, Field{"Back", ""}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "<b>bold</b>" {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_UnknownFieldName(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{Missing}}", Afmt: "{{Back}}"}, note(Field{"Back", "x"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "[unknown field: Missing]") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_SpecialTags(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{Type}}/{{Deck}}/{{Subdeck}}/{{Tags}}/{{Card}}", Afmt: "{{Back}}"},
		note(Field{"Back", "x"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	want := "Basic/Parent::Child/Child/tag1 tag2/"
	if !strings.HasPrefix(string(c.Question.HTML), want) {
		t.Errorf("question = %q, want prefix %q", c.Question.HTML, want)
	}
}

func TestRenderCard_SectionTruthy(t *testing.T) {
	tmpl := Template{Qfmt: "{{#Front}}yes{{/Front}}{{^Front}}no{{/Front}}", Afmt: "{{Back}}"}
	c, err := RenderCard(tmpl, note(Field{"Front", "x"}, Field{"Back", ""}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "yes" {
		t.Errorf("question = %q, want yes", c.Question.HTML)
	}

	c2, err := RenderCard(tmpl, note(Field{"Front", ""}, Field{"Back", ""}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c2.Question.HTML) != "no" {
		t.Errorf("question = %q, want no", c2.Question.HTML)
	}
}

func TestRenderCard_SectionWhitespaceAndHTMLOnlyIsEmpty(t *testing.T) {
	tmpl := Template{Qfmt: "{{#Front}}yes{{/Front}}", Afmt: "{{Back}}"}
	c, err := RenderCard(tmpl, note(Field{"Front", "<br> \n "}, Field{"Back", ""}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "" {
		t.Errorf("question = %q, want empty (whitespace/HTML-only field is falsy)", c.Question.HTML)
	}
}

func TestRenderCard_SectionNested(t *testing.T) {
	tmpl := Template{Qfmt: "{{#A}}{{#B}}both{{/B}}{{/A}}", Afmt: "x"}
	c, err := RenderCard(tmpl, note(Field{"A", "1"}, Field{"B", "1"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "both" {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_SectionCNActiveInactive(t *testing.T) {
	tmpl := Template{Qfmt: "{{#c1}}active{{/c1}}{{^c1}}inactive{{/c1}}", Afmt: "x"}
	c, err := RenderCard(tmpl, note(Field{"Text", "{{c1::x}}"}), 0, true) // ordinal 0 -> cloze number 1
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "active" {
		t.Errorf("question = %q, want active", c.Question.HTML)
	}

	c2, err := RenderCard(tmpl, note(Field{"Text", "{{c1::x}}"}), 1, true) // ordinal 1 -> cloze number 2, not 1
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c2.Question.HTML) != "inactive" {
		t.Errorf("question = %q, want inactive", c2.Question.HTML)
	}
}

func TestRenderCard_SectionUnclosed(t *testing.T) {
	_, err := RenderCard(Template{Qfmt: "{{#A}}x", Afmt: "y"}, note(), 0, false)
	if !errors.Is(err, ErrUnclosedSection) {
		t.Fatalf("err = %v, want ErrUnclosedSection", err)
	}
}

func TestRenderCard_SectionMismatchedClose(t *testing.T) {
	_, err := RenderCard(Template{Qfmt: "{{#A}}x{{/B}}", Afmt: "y"}, note(), 0, false)
	if !errors.Is(err, ErrSectionMismatch) {
		t.Fatalf("err = %v, want ErrSectionMismatch", err)
	}
}

func TestRenderCard_SectionCloseWithoutOpen(t *testing.T) {
	_, err := RenderCard(Template{Qfmt: "x{{/A}}", Afmt: "y"}, note(), 0, false)
	if !errors.Is(err, ErrSectionMismatch) {
		t.Fatalf("err = %v, want ErrSectionMismatch", err)
	}
}

func TestRenderCard_MalformedSectionInFalseBranchStillErrors(t *testing.T) {
	// The false branch of {{^Front}} is never emitted, but its unclosed {{#X}} inside must
	// still be a structural error (§3.1): a template that only breaks for some notes is worse.
	_, err := RenderCard(Template{Qfmt: "{{^Front}}{{#X}}oops{{/Front}}", Afmt: "y"},
		note(Field{"Front", "present"}), 0, false)
	if err == nil {
		t.Fatal("expected a structural error from the unreached branch")
	}
}

func TestRenderCard_FrontSideBasic(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{Front}}", Afmt: "{{FrontSide}}<hr>{{Back}}"},
		note(Field{"Front", "Q"}, Field{"Back", "A"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Answer.HTML) != "Q<hr>A" {
		t.Errorf("answer = %q", c.Answer.HTML)
	}
}

func TestRenderCard_FrontSideOnQuestionSideUnsupported(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{FrontSide}}", Afmt: "x"}, note(), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "[unsupported: FrontSide on the question side]") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_FilterText(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{text:Front}}", Afmt: "x"},
		note(Field{"Front", "<b>bold</b> &amp; more"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if strings.Contains(string(c.Question.HTML), "<b>") {
		t.Errorf("text filter left HTML tags: %q", c.Question.HTML)
	}
	if !strings.Contains(string(c.Question.HTML), "bold") || !strings.Contains(string(c.Question.HTML), "&amp;") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_FilterFurigana(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{furigana:Front}}", Afmt: "x"},
		note(Field{"Front", "漢字[かんじ]"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	want := "<ruby>漢字<rt>かんじ</rt></ruby>"
	if string(c.Question.HTML) != want {
		t.Errorf("question = %q, want %q", c.Question.HTML, want)
	}
}

func TestRenderCard_FilterKanjiKana(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{kanji:Front}}", Afmt: "{{kana:Front}}"},
		note(Field{"Front", "漢字[かんじ]"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "漢字" {
		t.Errorf("kanji = %q", c.Question.HTML)
	}
	if string(c.Answer.HTML) != "かんじ" {
		t.Errorf("kana = %q", c.Answer.HTML)
	}
}

func TestRenderCard_FilterHint(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{hint:Front}}", Afmt: "x"}, note(Field{"Front", "the hint"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "<summary>Front</summary>") {
		t.Errorf("question = %q", c.Question.HTML)
	}
	if !strings.Contains(string(c.Question.HTML), "the hint") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_FilterHintEmptyField(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{hint:Front}}", Afmt: "x"}, note(Field{"Front", ""}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if string(c.Question.HTML) != "" {
		t.Errorf("question = %q, want empty", c.Question.HTML)
	}
}

func TestRenderCard_FilterChainedTextCloze(t *testing.T) {
	// {{text:cloze:Text}}: cloze applies first (nearest the name), text applies to its output.
	c, err := RenderCard(Template{Qfmt: "{{text:cloze:Text}}", Afmt: "x"},
		note(Field{"Text", "a <b>{{c1::hidden}}</b> b"}), 0, true)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if strings.Contains(string(c.Question.HTML), "<b>") || strings.Contains(string(c.Question.HTML), "<span") {
		t.Errorf("text filter should have stripped the cloze span's HTML: %q", c.Question.HTML)
	}
}

func TestRenderCard_FilterUnknownName(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{bogus:Front}}", Afmt: "x"}, note(Field{"Front", "x"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "[unknown filter: bogus]") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_FilterUnsupportedTTS(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{tts:Front}}", Afmt: "x"}, note(Field{"Front", "x"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "[unsupported: tts]") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_ClozeSingleFrontAndBack(t *testing.T) {
	tmpl := Template{Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"}
	c, err := RenderCard(tmpl, note(Field{"Text", "the {{c1::answer}} here"}), 0, true)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), `<span class="cloze">[...]</span>`) {
		t.Errorf("question = %q", c.Question.HTML)
	}
	if !strings.Contains(string(c.Answer.HTML), `<span class="cloze">answer</span>`) {
		t.Errorf("answer = %q", c.Answer.HTML)
	}
}

func TestRenderCard_ClozeWithHintFront(t *testing.T) {
	tmpl := Template{Qfmt: "{{cloze:Text}}", Afmt: "x"}
	c, err := RenderCard(tmpl, note(Field{"Text", "{{c1::answer::a hint}}"}), 0, true)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), `[a hint]`) {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

// The §8 rule's own fixture: the non-active numbers must appear as plain text on BOTH sides.
func TestRenderCard_ClozeMultiActiveBlankedOthersRevealed(t *testing.T) {
	tmpl := Template{Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"}
	c, err := RenderCard(tmpl, note(Field{"Text", "{{c1::one}} and {{c2::two}}"}), 0, true) // active = c1
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "[...]") {
		t.Errorf("active cloze not blanked on question: %q", c.Question.HTML)
	}
	if !strings.Contains(string(c.Question.HTML), "two") {
		t.Errorf("inactive cloze not revealed on question: %q", c.Question.HTML)
	}
	if !strings.Contains(string(c.Answer.HTML), "one") || !strings.Contains(string(c.Answer.HTML), "two") {
		t.Errorf("both clozes should be revealed on answer: %q", c.Answer.HTML)
	}
}

func TestRenderCard_ClozeNested(t *testing.T) {
	tmpl := Template{Qfmt: "{{cloze:Text}}", Afmt: "{{cloze:Text}}"}
	c, err := RenderCard(tmpl, note(Field{"Text", "{{c1::a {{c2::b}}}}"}), 1, true) // active = c2
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	// c1 (inactive) revealed as plain text, recursing into its nested c2 (active) which blanks.
	if !strings.Contains(string(c.Question.HTML), "a") || !strings.Contains(string(c.Question.HTML), "[...]") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_ClozeUnterminatedMarkerIsLiteral(t *testing.T) {
	tmpl := Template{Qfmt: "{{cloze:Text}}", Afmt: "x"}
	c, err := RenderCard(tmpl, note(Field{"Text", "{{c1:not a marker}}"}), 0, true)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "not a marker") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_ClozeFilterOnNonClozeNoteTypeRevealsAll(t *testing.T) {
	tmpl := Template{Qfmt: "{{cloze:Text}}", Afmt: "x"}
	c, err := RenderCard(tmpl, note(Field{"Text", "{{c1::x}}"}), 0, false) // isCloze=false
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if strings.Contains(string(c.Question.HTML), "[...]") {
		t.Errorf("non-cloze note type must never blank: %q", c.Question.HTML)
	}
	if !strings.Contains(string(c.Question.HTML), "x") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_TypeQuestionSidePlaceholderOnly(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{type:Front}}", Afmt: "{{Front}}"}, note(Field{"Front", "answer"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if c.Question.Type == nil {
		t.Fatal("Question.Type should be set")
	}
	if strings.Contains(string(c.Question.HTML), "<") {
		t.Errorf("question side should contain no HTML, only the placeholder: %q", c.Question.HTML)
	}
}

func TestRenderCard_TypeExpectedStripsHTML(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{type:Front}}", Afmt: "x"}, note(Field{"Front", "<b>answer</b>"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if c.Question.Type.Expected != "answer" {
		t.Errorf("Expected = %q, want %q", c.Question.Type.Expected, "answer")
	}
}

func TestRenderCard_TypeDuplicateOnOneSide(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{type:Front}}{{type:Front}}", Afmt: "x"}, note(Field{"Front", "x"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if !strings.Contains(string(c.Question.HTML), "[duplicate type: Front]") {
		t.Errorf("question = %q", c.Question.HTML)
	}
}

func TestRenderCard_FrontSideCarriesTypePlaceholder(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "{{type:Front}}", Afmt: "{{FrontSide}}<hr>{{Back}}"},
		note(Field{"Front", "ans"}, Field{"Back", "b"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if c.Answer.Type == nil {
		t.Fatal("Answer.Type should be adopted from the question side via FrontSide")
	}
	if c.Answer.Type.Placeholder != c.Question.Type.Placeholder {
		t.Error("answer side's placeholder should be identical to the question's, not a fresh nonce")
	}
}

func TestRenderCard_SanitisesAssembledHTML(t *testing.T) {
	c, err := RenderCard(Template{Qfmt: "<div>{{Front}}", Afmt: "{{Front}}</div><script>alert(1)</script>{{Back}}"},
		note(Field{"Front", ""}, Field{"Back", "safe"}), 0, false)
	if err != nil {
		t.Fatalf("RenderCard: %v", err)
	}
	if strings.Contains(string(c.Answer.HTML), "<script") || strings.Contains(string(c.Answer.HTML), "alert(1)") {
		t.Errorf("answer = %q", c.Answer.HTML)
	}
}
