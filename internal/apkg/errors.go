package apkg

import "errors"

var (
	ErrNotAPackage       = errors.New("apkg: not a zip archive")
	ErrNoCollection      = errors.New("apkg: no collection member in package")
	ErrTooManyMembers    = errors.New("apkg: archive member count exceeds limit")
	ErrMemberTooLarge    = errors.New("apkg: archive member exceeds per-member size limit")
	ErrArchiveTooLarge   = errors.New("apkg: archive exceeds total decompressed size limit")
	ErrBadZstdFrame      = errors.New("apkg: malformed zstd frame header")
	ErrUnknownSchema     = errors.New("apkg: collection matches neither schema 11 nor schema 18")
	ErrCorruptCollection = errors.New("apkg: collection is missing a required table or column")
	ErrMediaIndex        = errors.New("apkg: malformed media index")
	ErrSchema18Config    = errors.New("apkg: schema-18 config blob did not decode to a usable value")
	ErrNoteTypeMismatch  = errors.New("apkg: existing note type has a different field count")
)
