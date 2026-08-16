package textimport

import (
	"fmt"
	"strings"
)

// ClozeDepth is how much text a cloze card blanks per {{cN::...}} marker, from precise
// word-level recall up to whole-sentence recall. It only shapes the generated prompt -- Parse
// and everything downstream of it don't know or care what depth was asked for.
type ClozeDepth string

const (
	ClozeDepthWords     ClozeDepth = "words"
	ClozeDepthPhrases   ClozeDepth = "phrases"
	ClozeDepthSentences ClozeDepth = "sentences"

	// DefaultClozeDepth is used whenever a caller doesn't specify a depth, or specifies one
	// that isn't one of the ClozeDepths options -- BuildPrompt never errors on this, since an
	// unrecognised depth costs nothing worse than a slightly-off prompt.
	DefaultClozeDepth = ClozeDepthPhrases
)

// ClozeDepthOption is one depth choice, paired with the label a picker UI shows the user.
type ClozeDepthOption struct {
	Value ClozeDepth
	Label string
}

// ClozeDepths lists every depth in easiest-to-hardest order, for a UI to render as a dial
// (single choice) rather than a set of independent toggles -- see the instruction text below,
// which describes each depth as one point on a spectrum rather than a mechanical word-count
// rule, deliberately leaning on the AI's own judgement of what counts as a "phrase" or
// "sentence" in the source material rather than trying to specify that ourselves.
var ClozeDepths = []ClozeDepthOption{
	{ClozeDepthWords, "Words — hide individual key words or short phrases"},
	{ClozeDepthPhrases, "Phrases — hide meaningful phrases or clauses"},
	{ClozeDepthSentences, "Sentences — hide entire sentences or lines"},
}

var clozeDepthInstructions = map[ClozeDepth]string{
	ClozeDepthWords: "Hide individual key words or short phrases (a few words at most) at a " +
		"time -- the reader should be able to recall each blank from the surrounding context " +
		"alone.",
	ClozeDepthPhrases: "Hide meaningful phrases or clauses -- a run of words that forms one " +
		"idea, longer than a single word but short of a whole sentence.",
	ClozeDepthSentences: "Hide entire sentences or lines at a time. Put the WHOLE passage -- " +
		"every sentence or line -- in one card's fields, giving each sentence its own cloze " +
		"number (c1, c2, c3, and so on) rather than making a separate card per sentence. A card " +
		"whose field is nothing but the one hidden sentence has no surrounding text left to " +
		"recall it from; the other sentences must stay visible as context around the blank. Only " +
		"split into more than one card if the full passage would be unwieldy as a single one.",
}

// PromptOptions is everything BuildPrompt needs to know about the target note type and how hard
// to make a cloze reply. ClozeDepth is ignored unless IsCloze is true.
type PromptOptions struct {
	NoteTypeName string
	FieldNames   []string
	IsCloze      bool
	ClozeDepth   ClozeDepth
}

// BuildPrompt returns the human-readable prompt text a user copies into their own AI, followed
// by their source material. It's assembled from independent blocks -- a fields/output-format
// block that's always present, plus cloze-only blocks -- so a future new option (a different
// question style, say) is one more conditional block, not a rewrite of this function.
func BuildPrompt(opts PromptOptions) string {
	var b strings.Builder
	writeFieldsBlock(&b, opts.NoteTypeName, opts.FieldNames)
	writeContentFidelityBlock(&b)
	writeOutputRulesBlock(&b, len(opts.FieldNames))
	if opts.IsCloze {
		writeClozeSyntaxBlock(&b)
		writeClozeDepthBlock(&b, opts.ClozeDepth)
	}
	b.WriteString("\nHere is my source material:\n\n")
	return b.String()
}

func writeFieldsBlock(b *strings.Builder, noteTypeName string, fieldNames []string) {
	fmt.Fprintf(b, "I'm creating flashcards for a %q note type with these fields, in order:\n", noteTypeName)
	for i, name := range fieldNames {
		fmt.Fprintf(b, "%d. %s\n", i+1, name)
	}
}

// writeContentFidelityBlock exists because a weak reply can satisfy every structural rule below
// (right field count, valid JSON, correct cloze syntax) while still being useless -- e.g. a card
// whose field just reads "Line 1" instead of the actual source text. Structure alone doesn't
// imply content, so this has to be said explicitly rather than assumed.
func writeContentFidelityBlock(b *strings.Builder) {
	b.WriteString("\nEvery field's text must come from the source material itself -- quote it " +
		"(nearly) verbatim, never a generic placeholder like \"Line 1\" or \"Section A\". Break the " +
		"material into one card per natural unit (a sentence, line, or verse is usually right) " +
		"unless it's short enough that a single card covers all of it.\n")
}

func writeOutputRulesBlock(b *strings.Builder, fieldCount int) {
	b.WriteString("\nFor each flashcard, output exactly one line containing a JSON object of this form:\n")
	b.WriteString(`{"fields":["value for field 1","value for field 2"],"tags":["optional","tags"]}` + "\n")
	b.WriteString("\nRules:\n")
	b.WriteString("- One JSON object per line (NDJSON) -- do not wrap the lines in a JSON array.\n")
	b.WriteString("- Output only these lines: no prose before or after, no markdown code fence, no numbering.\n")
	fmt.Fprintf(b, "- \"fields\" must have exactly %d values, in the field order above.\n", fieldCount)
	b.WriteString("- \"tags\" is optional; omit it, or leave it empty, if a card has no tags.\n")
}

func writeClozeSyntaxBlock(b *strings.Builder) {
	b.WriteString("\nThis is a cloze note type: mark text to be hidden with {{c1::the hidden text}}, " +
		"{{c2::the hidden text}}, and so on. Each distinct number becomes its own flashcard, and " +
		"numbering restarts at c1 for every new card. You may add an optional hint after a second " +
		"\"::\", e.g. {{c1::Paris::capital city}}.\n")
}

func writeClozeDepthBlock(b *strings.Builder, depth ClozeDepth) {
	instruction, ok := clozeDepthInstructions[depth]
	if !ok {
		instruction = clozeDepthInstructions[DefaultClozeDepth]
	}
	fmt.Fprintf(b, "\nHow much to hide per cloze marker: %s\n", instruction)
}
