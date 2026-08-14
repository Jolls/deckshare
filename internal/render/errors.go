package render

import "errors"

// Structural template errors -- these fail the whole render (§0.6 of docs/plans/
// 55-note-type-rendering-sanitisation.md). Unknown field/filter names degrade to inert text
// instead and never reach these.
var (
	ErrUnclosedSection = errors.New("render: unclosed section")
	ErrSectionMismatch = errors.New("render: section close does not match its open")
	ErrUnterminatedTag = errors.New("render: unterminated {{ tag")
)
