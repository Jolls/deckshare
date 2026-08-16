// Package textimport parses the NDJSON interchange format for issue #85's paste-in AI import:
// a user picks a deck + note type, copies a generated prompt (BuildPrompt) into whatever AI
// they already use along with their own source material, and pastes the reply back in. The
// format itself is not meant to be human-readable -- only the prompt is -- so Parse never tries
// to be lenient about genuinely malformed content. It is pure: no internal/db import, no
// net/http import, no I/O.
package textimport

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Note is one parsed JSON object: positional field values (matched by index to the target note
// type's field order, decided before the prompt was generated) plus optional tags.
type Note struct {
	Fields []string
	Tags   []string
}

// LineError is a parse failure, located by a best-effort line number computed from the byte
// offset of the value that failed -- exact when values are one-per-line as the prompt asks for,
// still a useful locator when they aren't.
type LineError struct {
	Line    int
	Message string
}

func (e LineError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
}

type rawNote struct {
	Fields []string `json:"fields"`
	Tags   []string `json:"tags"`
}

// Parse reads a stream of JSON objects, {"fields":[...],"tags":[...]}, into notes. It uses
// encoding/json's streaming decoder rather than splitting on "\n": objects are self-delimiting
// JSON, so this parses correctly whether the AI put one per line (as asked), ran them all
// together on one line with no separators, or anything in between -- a real reply was observed
// doing the latter despite the prompt's instructions. A single leading and/or trailing markdown
// code-fence line (``` or ```json) wrapping the whole reply is also tolerated, since a chat UI's
// copy button sometimes includes one. The first value that isn't valid JSON stops parsing and is
// reported as a LineError -- once the stream desyncs there's no reliable way to resync, but that
// costs nothing here since a caller that sees any error must reject the whole batch anyway.
// Field-count and content validation against a specific note type is the caller's job.
func Parse(text string) ([]Note, []LineError) {
	text = stripFence(text)

	dec := json.NewDecoder(strings.NewReader(text))
	var notes []Note
	var errs []LineError
	for dec.More() {
		offset := dec.InputOffset()
		var raw rawNote
		if err := dec.Decode(&raw); err != nil {
			errs = append(errs, LineError{
				Line:    1 + strings.Count(text[:offset], "\n"),
				Message: "not a valid JSON object: " + err.Error(),
			})
			break
		}
		notes = append(notes, Note(raw))
	}
	return notes, errs
}

// stripFence drops a single markdown code-fence line wrapping the whole reply, if present --
// e.g. a chat UI's copy button including the ```json ... ``` fence despite the prompt asking
// for none. It only looks at the very start/end of the text, never mid-stream, so it can't mask
// a fence-looking line that's actually malformed content in the middle of the reply.
func stripFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(trimmed, "```"); ok {
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			trimmed = strings.TrimSpace(rest[i+1:])
		} else {
			trimmed = ""
		}
	}
	return strings.TrimSpace(strings.TrimSuffix(trimmed, "```"))
}
