package apkg

import (
	"encoding/binary"
	"fmt"
)

// protoWireType is the low three bits of a protobuf tag.
type protoWireType uint8

const (
	protoVarint protoWireType = 0
	protoI64    protoWireType = 1
	protoBytes  protoWireType = 2
	protoI32    protoWireType = 5
)

// maxProtoFields bounds decodeProto against hostile input: these blobs are small config records,
// so anything larger is malformed or hostile.
const maxProtoFields = 4096

// protoField is one decoded field occurrence, in encounter order.
type protoField struct {
	Number uint32
	Type   protoWireType
	Varint uint64 // valid for protoVarint / protoI64 / protoI32
	Bytes  []byte // valid for protoBytes; aliases the input, never copied
}

// decodeProto walks b and returns every top-level field occurrence. Groups (wire types 3 and 4)
// are rejected: no message this reader touches uses them, and skipping them correctly needs a
// nesting stack that would only ever run on malformed input.
func decodeProto(b []byte) ([]protoField, error) {
	var fields []protoField
	for len(b) > 0 {
		if len(fields) >= maxProtoFields {
			return nil, fmt.Errorf("apkg: protobuf message exceeds %d fields: %w", maxProtoFields, ErrSchema18Config)
		}
		tag, n := decodeVarint(b)
		if n <= 0 {
			return nil, fmt.Errorf("apkg: truncated protobuf tag: %w", ErrSchema18Config)
		}
		b = b[n:]
		num := uint32(tag >> 3)
		wt := protoWireType(tag & 0x7)
		switch wt {
		case protoVarint:
			v, n := decodeVarint(b)
			if n <= 0 {
				return nil, fmt.Errorf("apkg: truncated protobuf varint: %w", ErrSchema18Config)
			}
			b = b[n:]
			fields = append(fields, protoField{Number: num, Type: wt, Varint: v})
		case protoI64:
			if len(b) < 8 {
				return nil, fmt.Errorf("apkg: truncated protobuf fixed64: %w", ErrSchema18Config)
			}
			v := binary.LittleEndian.Uint64(b[:8])
			b = b[8:]
			fields = append(fields, protoField{Number: num, Type: wt, Varint: v})
		case protoBytes:
			ln, n := decodeVarint(b)
			if n <= 0 {
				return nil, fmt.Errorf("apkg: truncated protobuf length prefix: %w", ErrSchema18Config)
			}
			b = b[n:]
			if ln > uint64(len(b)) {
				return nil, fmt.Errorf("apkg: protobuf length prefix runs past end of message: %w", ErrSchema18Config)
			}
			fields = append(fields, protoField{Number: num, Type: wt, Bytes: b[:ln]})
			b = b[ln:]
		case protoI32:
			if len(b) < 4 {
				return nil, fmt.Errorf("apkg: truncated protobuf fixed32: %w", ErrSchema18Config)
			}
			v := uint64(binary.LittleEndian.Uint32(b[:4]))
			b = b[4:]
			fields = append(fields, protoField{Number: num, Type: wt, Varint: v})
		default:
			return nil, fmt.Errorf("apkg: unsupported protobuf wire type %d: %w", wt, ErrSchema18Config)
		}
	}
	return fields, nil
}

// decodeVarint reads a base-128 varint from the start of b. Returns the value and the number of
// bytes consumed. binary.Uvarint's own error convention is n == 0 (truncated) or n < 0 (overflow,
// -n bytes read) -- both satisfy the `n <= 0` check every call site above already makes, so this
// is a drop-in replacement for the hand-rolled loop it used to be, and it additionally rejects an
// overlong (>10 byte / >64-bit) varint outright instead of silently truncating it.
func decodeVarint(b []byte) (uint64, int) {
	return binary.Uvarint(b)
}

// protoLast returns the last occurrence of field number n with wire type wt. Last, not first:
// protobuf's own rule for a repeated scalar is that the final value wins.
func protoLast(fields []protoField, n uint32, wt protoWireType) (protoField, bool) {
	var out protoField
	var found bool
	for _, f := range fields {
		if f.Number == n && f.Type == wt {
			out, found = f, true
		}
	}
	return out, found
}

// protoString returns the last occurrence of field number n as a string, and whether it was
// present with wire type 2.
func protoString(fields []protoField, n uint32) (string, bool) {
	f, ok := protoLast(fields, n, protoBytes)
	return string(f.Bytes), ok
}

// protoUint returns the last varint occurrence of field number n.
func protoUint(fields []protoField, n uint32) (uint64, bool) {
	f, ok := protoLast(fields, n, protoVarint)
	return f.Varint, ok
}

// protoMessage returns the last length-delimited occurrence of n, decoded as a nested message.
func protoMessage(fields []protoField, n uint32) ([]protoField, bool) {
	f, ok := protoLast(fields, n, protoBytes)
	if !ok {
		return nil, false
	}
	nested, err := decodeProto(f.Bytes)
	if err != nil {
		return nil, false
	}
	return nested, true
}
