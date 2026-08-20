package apkg

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"golang.org/x/text/unicode/norm"
)

// readMediaIndex parses the package's "media" member into index -> filename. The legacy
// container spells it as a JSON object ({"0":"cat.jpg"}); the modern container spells it as a
// protobuf list where an entry's POSITION is the zip member name the JSON spelled as a key
// (apkg-format.md). Sniffed on the first byte -- '{' is JSON, anything else is protobuf -- not
// branched on the package version, so a version number's meaning cannot be got wrong.
func readMediaIndex(b []byte) (map[string]string, error) {
	if len(b) == 0 {
		return map[string]string{}, nil
	}
	if b[0] == '{' {
		var idx map[string]string
		if err := json.Unmarshal(b, &idx); err != nil {
			return nil, fmt.Errorf("apkg: decoding JSON media index: %w", ErrMediaIndex)
		}
		return idx, nil
	}
	// The protobuf spelling (modern container) uses mediaEntryField/mediaEntryNameField, both
	// confirmed against a real export's media filenames (ankischema.go, #61).
	return decodeProtoMediaIndex(b)
}

// decodeProtoMediaIndex is the protobuf media-index decode logic, kept separate for direct
// testing.
func decodeProtoMediaIndex(b []byte) (map[string]string, error) {
	fields, err := decodeProto(b)
	if err != nil {
		return nil, fmt.Errorf("apkg: decoding protobuf media index: %w", ErrMediaIndex)
	}
	idx := map[string]string{}
	pos := 0
	for _, f := range fields {
		if f.Number != mediaEntryField || f.Type != protoBytes {
			continue
		}
		entry, err := decodeProto(f.Bytes)
		if err != nil {
			return nil, fmt.Errorf("apkg: decoding protobuf media entry: %w", ErrMediaIndex)
		}
		name, ok := protoString(entry, mediaEntryNameField)
		if !ok {
			return nil, fmt.Errorf("apkg: media entry missing name: %w", ErrMediaIndex)
		}
		idx[strconv.Itoa(pos)] = name
		pos++
	}
	return idx, nil
}

// collectMedia reads every media member named by idx, NFC-normalises its filename, hashes it,
// and applies the first-seen-wins collision policy (docs/schema.md, Media). Entries are visited
// in ascending numeric index order so the policy is deterministic across re-imports. Returns the
// media entries and one warning per dropped file.
func collectMedia(z *zip.Reader, idx map[string]string, limits ArchiveLimits, budget *int64) ([]IrMedia, []string, error) {
	files := make(map[string]*zip.File, len(z.File))
	for _, f := range z.File {
		files[f.Name] = f
	}

	indices := make([]string, 0, len(idx))
	for k := range idx {
		indices = append(indices, k)
	}
	sort.Slice(indices, func(i, j int) bool {
		ni, _ := strconv.Atoi(indices[i])
		nj, _ := strconv.Atoi(indices[j])
		return ni < nj
	})

	var media []IrMedia
	var warnings []string
	seenByName := map[string]IrMedia{}
	for _, idxKey := range indices {
		filename := idx[idxKey]
		f, ok := files[idxKey]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("media: member %q referenced by index but missing from archive", idxKey))
			continue
		}
		data, err := memberBytes(f, limits, budget)
		if err != nil {
			return nil, nil, err
		}
		// Newer Anki exports (meta version 3) zstd-compress individual media members too, not
		// just the collection and the media index (docs/apkg-format.md's now-corrected media
		// section). Media bytes are otherwise arbitrary, so a false-positive magic-number match
		// on a legitimate file must not corrupt it: only swap in the decompressed bytes if the
		// frame actually decodes, and keep the raw bytes on any other decompress error -- that
		// is ErrBadZstdFrame territory, where the magic-number match was likely coincidental.
		// ErrMemberTooLarge is different: reaching it means decompressZstd got far enough to
		// trust this genuinely is zstd (a parsed declared size, or real decoded output, past the
		// size ceiling), so falling back to the raw bytes would silently store the still-
		// compressed frame as the media blob -- the exact bug this fix exists to close, just
		// gated on size instead of on the reader missing the case entirely. Drop it instead, with
		// a warning, the same way a member missing from the archive is handled above.
		if sniffZstd(data) {
			decompressed, derr := decompressZstd(data, limits, budget)
			switch {
			case derr == nil:
				data = decompressed
			case errors.Is(derr, ErrMemberTooLarge):
				warnings = append(warnings, fmt.Sprintf("media: member %q (%s) exceeds the decompressed-size limit; dropped", idxKey, filename))
				continue
			}
		}
		normalised := norm.NFC.String(filename)
		sum := sha256.Sum256(data)
		hexSum := hex.EncodeToString(sum[:])

		if existing, dup := seenByName[normalised]; dup {
			if existing.SHA256 == hexSum {
				continue // identical bytes under the same name -- silent drop
			}
			warnings = append(warnings, fmt.Sprintf("media: dropped %q (a different file of the same name was already imported)", normalised))
			continue
		}

		entry := IrMedia{
			Index:     idxKey,
			Filename:  normalised,
			SHA256:    hexSum,
			SizeBytes: int64(len(data)),
			Data:      data,
		}
		seenByName[normalised] = entry
		media = append(media, entry)
	}
	return media, warnings, nil
}
