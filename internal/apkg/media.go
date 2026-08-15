package apkg

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	// The protobuf spelling (modern container) uses mediaEntryField/mediaEntryNameField, which
	// are unverified placeholders like schema-18's field numbers (ankischema.go, #61) -- fail
	// loudly rather than risk silently mis-decoding filenames from a real export.
	return nil, fmt.Errorf("apkg: protobuf media index decoding is not yet supported (see #61): %w", ErrSchema18Config)
}

// decodeProtoMediaIndex is the protobuf media-index decode logic, kept separate so it can be
// exercised directly once #61 verifies mediaEntryField/mediaEntryNameField.
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
