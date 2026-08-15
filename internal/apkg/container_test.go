package apkg

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"
)

func TestRead_PrefersNewestCollectionMember(t *testing.T) {
	pkg := buildDowngradeStubPackage(t)
	got := readBytes(t, pkg)
	if len(got.Notes) != 3 {
		t.Fatalf("Notes count = %d, want 3 (the real collection.anki21, not the 1-note collection.anki2 stub)", len(got.Notes))
	}
}

func TestRead_ZstdContainer(t *testing.T) {
	spec := defaultSynthSpec(t)
	plain := buildSchema11Package(t, spec)
	zstdPkg := buildZstdPackage(t, plain)

	want := readBytes(t, plain)
	got := readBytes(t, zstdPkg)
	assertIRMatches(t, got, want)
}

func TestRead_MemberCountCeiling(t *testing.T) {
	limits := DefaultArchiveLimits()
	members := map[string][]byte{}
	for i := 0; i <= limits.MaxMembers; i++ {
		members[string(rune('a'))+itoaTest(i)] = []byte("x")
	}
	pkg := zipMembers(t, members)
	_, err := Read(bytes.NewReader(pkg), int64(len(pkg)), limits)
	if !errors.Is(err, ErrTooManyMembers) {
		t.Fatalf("err = %v, want ErrTooManyMembers", err)
	}
}

func TestRead_MemberSizeCeiling(t *testing.T) {
	limits := ArchiveLimits{MaxMembers: 10, MaxMemberBytes: 100, MaxTotalBytes: 10_000}
	pkg := zipMembers(t, map[string][]byte{"collection.anki21": bytes.Repeat([]byte{0}, 200)})
	_, err := Read(bytes.NewReader(pkg), int64(len(pkg)), limits)
	if !errors.Is(err, ErrMemberTooLarge) {
		t.Fatalf("err = %v, want ErrMemberTooLarge", err)
	}
}

func TestRead_TotalSizeCeiling(t *testing.T) {
	// Only members Read() actually reads (the collection, and "media" if present) count against
	// the budget, so the ceiling has to be smaller than the real collection member's own size.
	spec := defaultSynthSpec(t)
	pkg := buildSchema11Package(t, spec)
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatalf("opening package: %v", err)
	}
	var collSize int64
	for _, f := range zr.File {
		if f.Name == "collection.anki21" {
			collSize = int64(f.UncompressedSize64)
		}
	}
	if collSize == 0 {
		t.Fatal("collection member not found")
	}
	limits := ArchiveLimits{MaxMembers: 10, MaxMemberBytes: collSize + 1000, MaxTotalBytes: collSize - 1}
	_, err = Read(bytes.NewReader(pkg), int64(len(pkg)), limits)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("err = %v, want ErrArchiveTooLarge", err)
	}
}

func TestRead_ZstdDeclaredSizeRejectedBeforeDecompressing(t *testing.T) {
	pkg := buildOversizePackage(t, true)
	limits := ArchiveLimits{MaxMembers: 100, MaxMemberBytes: 1 << 20, MaxTotalBytes: 10 << 20}
	_, err := Read(bytes.NewReader(pkg), int64(len(pkg)), limits)
	if !errors.Is(err, ErrMemberTooLarge) {
		t.Fatalf("err = %v, want ErrMemberTooLarge (declared-size gate)", err)
	}
}

func TestZstdDeclaredSize_HeaderForms(t *testing.T) {
	// Single-segment, FCS code 0 (1-byte field): magic + descriptor(0x20) + 1-byte size.
	singleSeg1 := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x20, 42}
	size, ok, err := zstdDeclaredSize(singleSeg1)
	if err != nil || !ok || size != 42 {
		t.Errorf("single-segment 1-byte: size=%d ok=%v err=%v, want 42,true,nil", size, ok, err)
	}

	// Not single-segment, FCS code 0 means "no content size field".
	notSingleSeg := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, 0x11} // descriptor 0, window descriptor 0x11
	_, ok, err = zstdDeclaredSize(notSingleSeg)
	if err != nil || ok {
		t.Errorf("descriptor with no FCS field: ok=%v err=%v, want false,nil", ok, err)
	}

	// FCS code 1 (2-byte field, +256 offset), single segment.
	twoByte := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x60, 0x01, 0x00} // code 1<<6=0x40 | single-seg 0x20 = 0x60; value 1 -> +256 = 257
	size, ok, err = zstdDeclaredSize(twoByte)
	if err != nil || !ok || size != 257 {
		t.Errorf("2-byte FCS: size=%d ok=%v err=%v, want 257,true,nil", size, ok, err)
	}

	// FCS code 2 (4-byte field), single segment, with a 1-byte dictionary id.
	fourByteWithDict := []byte{0x28, 0xB5, 0x2F, 0xFD, 0xA1, 0x07, 0x10, 0x20, 0x00, 0x00} // descriptor: code10=0x80|singleSeg0x20|dictFlag1=0xA1; dict id byte; then 4-byte LE size
	size, ok, err = zstdDeclaredSize(fourByteWithDict)
	if err != nil || !ok || size != 0x2010 {
		t.Errorf("4-byte FCS with dict: size=%d ok=%v err=%v, want %d,true,nil", size, ok, err, 0x2010)
	}

	// Truncated header.
	_, _, err = zstdDeclaredSize([]byte{0x28, 0xB5, 0x2F, 0xFD})
	if !errors.Is(err, ErrBadZstdFrame) {
		t.Errorf("truncated header: err = %v, want ErrBadZstdFrame", err)
	}
}

func TestRead_MediaIndexJSON(t *testing.T) {
	jsonIdx, err := readMediaIndex([]byte(`{"0":"cat.jpg","1":"dog.png"}`))
	if err != nil {
		t.Fatalf("readMediaIndex(JSON): %v", err)
	}
	if jsonIdx["0"] != "cat.jpg" || jsonIdx["1"] != "dog.png" {
		t.Fatalf("JSON media index = %v", jsonIdx)
	}
}

// TestRead_MediaIndexProtobufFailsUntilVerified mirrors TestRead_Schema18_FailsUntilVerified:
// the modern container's protobuf media index uses the same unverified field numbers (#61), so
// readMediaIndex must fail loudly rather than risk silently mis-decoding filenames, while
// decodeProtoMediaIndex (the plumbing #61 will unlock) still works correctly today.
func TestRead_MediaIndexProtobufFailsUntilVerified(t *testing.T) {
	var proto []byte
	proto = append(proto, encodeMediaEntry("cat.jpg")...)
	proto = append(proto, encodeMediaEntry("dog.png")...)

	_, err := readMediaIndex(proto)
	if !errors.Is(err, ErrSchema18Config) {
		t.Fatalf("readMediaIndex(protobuf) err = %v, want ErrSchema18Config", err)
	}

	protoIdx, err := decodeProtoMediaIndex(proto)
	if err != nil {
		t.Fatalf("decodeProtoMediaIndex: %v", err)
	}
	if protoIdx["0"] != "cat.jpg" || protoIdx["1"] != "dog.png" {
		t.Fatalf("protobuf media index = %v", protoIdx)
	}
}

func encodeMediaEntry(name string) []byte {
	entry := encodeProtoString(mediaEntryNameField, name)
	var out []byte
	out = appendVarint(out, uint64(mediaEntryField)<<3|uint64(protoBytes))
	out = appendVarint(out, uint64(len(entry)))
	out = append(out, entry...)
	return out
}

func TestRead_MediaFilenameNFCAndCollision(t *testing.T) {
	// "café.png" in NFD form (e with combining acute accent).
	nfd := "café.png"
	idx := map[string]string{"0": nfd}
	z := zipMembers(t, map[string][]byte{"0": []byte("data-a")})
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatalf("opening zip: %v", err)
	}
	budget := int64(1 << 20)
	media, warnings, err := collectMedia(zr, idx, DefaultArchiveLimits(), &budget)
	if err != nil {
		t.Fatalf("collectMedia: %v", err)
	}
	if len(media) != 1 || media[0].Filename != "café.png" {
		t.Fatalf("media = %+v, want NFC-normalised café.png", media)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}

	// Collision: two different indices map to the same normalised name with different bytes.
	idx2 := map[string]string{"0": "x.png", "1": "x.png"}
	z2 := zipMembers(t, map[string][]byte{"0": []byte("aaa"), "1": []byte("bbb")})
	zr2, _ := zip.NewReader(bytes.NewReader(z2), int64(len(z2)))
	budget2 := int64(1 << 20)
	media2, warnings2, err := collectMedia(zr2, idx2, DefaultArchiveLimits(), &budget2)
	if err != nil {
		t.Fatalf("collectMedia: %v", err)
	}
	if len(media2) != 1 {
		t.Fatalf("media2 = %+v, want exactly one entry kept", media2)
	}
	if len(warnings2) != 1 {
		t.Fatalf("warnings2 = %v, want exactly one collision warning", warnings2)
	}

	// Identical bytes under one name: no warning.
	idx3 := map[string]string{"0": "y.png", "1": "y.png"}
	z3 := zipMembers(t, map[string][]byte{"0": []byte("same"), "1": []byte("same")})
	zr3, _ := zip.NewReader(bytes.NewReader(z3), int64(len(z3)))
	budget3 := int64(1 << 20)
	_, warnings3, err := collectMedia(zr3, idx3, DefaultArchiveLimits(), &budget3)
	if err != nil {
		t.Fatalf("collectMedia: %v", err)
	}
	if len(warnings3) != 0 {
		t.Fatalf("warnings3 = %v, want none (identical bytes, silent drop)", warnings3)
	}
}

func TestRead_MediaBytesNeverZstdSniffed(t *testing.T) {
	zstdMagic := []byte{0x28, 0xB5, 0x2F, 0xFD, 'x', 'y', 'z'}
	idx := map[string]string{"0": "weird.bin"}
	z := zipMembers(t, map[string][]byte{"0": zstdMagic})
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatalf("opening zip: %v", err)
	}
	budget := int64(1 << 20)
	media, _, err := collectMedia(zr, idx, DefaultArchiveLimits(), &budget)
	if err != nil {
		t.Fatalf("collectMedia: %v", err)
	}
	if len(media) != 1 || !bytes.Equal(media[0].Data, zstdMagic) {
		t.Fatalf("media bytes were mangled: %+v", media)
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
