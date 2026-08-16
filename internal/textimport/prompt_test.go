package textimport

import (
	"strings"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	basic := BuildPrompt(PromptOptions{
		NoteTypeName: "Basic",
		FieldNames:   []string{"Front", "Back"},
	})
	if !strings.Contains(basic, "Front") || !strings.Contains(basic, "Back") {
		t.Errorf("BuildPrompt should mention field names, got: %s", basic)
	}
	if strings.Contains(basic, "{{c1::") {
		t.Errorf("non-cloze prompt should not mention cloze syntax, got: %s", basic)
	}

	cloze := BuildPrompt(PromptOptions{
		NoteTypeName: "Cloze",
		FieldNames:   []string{"Text", "Extra"},
		IsCloze:      true,
		ClozeDepth:   ClozeDepthSentences,
	})
	if !strings.Contains(cloze, "{{c1::") {
		t.Errorf("cloze prompt should mention cloze syntax, got: %s", cloze)
	}
	if !strings.Contains(cloze, clozeDepthInstructions[ClozeDepthSentences]) {
		t.Errorf("cloze prompt should mention the requested depth's instruction, got: %s", cloze)
	}
}

func TestBuildPrompt_UnrecognisedClozeDepthFallsBackToDefault(t *testing.T) {
	got := BuildPrompt(PromptOptions{
		NoteTypeName: "Cloze",
		FieldNames:   []string{"Text"},
		IsCloze:      true,
		ClozeDepth:   ClozeDepth("nonsense"),
	})
	if !strings.Contains(got, clozeDepthInstructions[DefaultClozeDepth]) {
		t.Errorf("unrecognised depth should fall back to the default instruction, got: %s", got)
	}
}

func TestBuildPrompt_NonClozeIgnoresDepth(t *testing.T) {
	got := BuildPrompt(PromptOptions{
		NoteTypeName: "Basic",
		FieldNames:   []string{"Front", "Back"},
		IsCloze:      false,
		ClozeDepth:   ClozeDepthSentences,
	})
	if strings.Contains(got, "How much to hide") {
		t.Errorf("non-cloze prompt should not mention depth at all, got: %s", got)
	}
}

// Regression: a real reply satisfied every structural rule (right field count, valid JSON,
// correct cloze syntax) while filling fields with "Line 1"-style placeholders instead of the
// actual source text. The prompt must say explicitly not to do that, for every note type, not
// just cloze ones -- structural validity doesn't imply real content.
func TestBuildPrompt_DemandsRealContentNotPlaceholders(t *testing.T) {
	for _, isCloze := range []bool{false, true} {
		got := BuildPrompt(PromptOptions{
			NoteTypeName: "Basic",
			FieldNames:   []string{"Front", "Back"},
			IsCloze:      isCloze,
		})
		if !strings.Contains(got, "source material itself") || !strings.Contains(got, "Line 1") {
			t.Errorf("isCloze=%v: prompt should warn against generic placeholder content, got: %s", isCloze, got)
		}
	}
}

// Regression: a real reply at ClozeDepthSentences made one card per sentence with nothing but
// that sentence -- fully hidden by its own cloze marker -- as the card's whole field, so the
// question side rendered as bare "[...]" with no surrounding text to recall it from. The prompt
// must say explicitly to keep the whole passage in one field with per-sentence cloze numbers.
func TestBuildPrompt_SentenceDepthKeepsSurroundingContext(t *testing.T) {
	got := BuildPrompt(PromptOptions{
		NoteTypeName: "Cloze",
		FieldNames:   []string{"Text", "Extra"},
		IsCloze:      true,
		ClozeDepth:   ClozeDepthSentences,
	})
	if !strings.Contains(got, "WHOLE passage") {
		t.Errorf("sentence-depth prompt should say to keep the whole passage in one field, got: %s", got)
	}
	if !strings.Contains(got, "no surrounding text left") {
		t.Errorf("sentence-depth prompt should explain why an isolated sentence-only card fails, got: %s", got)
	}
}
