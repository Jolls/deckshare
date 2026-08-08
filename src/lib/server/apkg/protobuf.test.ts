/**
 * Wire-format tests for the schema-less protobuf decoder.
 *
 * These test the *mechanism*, which is the part that can be verified: the encoding is the
 * public protobuf wire format, not anything Anki-specific. The field *numbers* Anki's messages
 * use remain unverified (issue #25) and nothing here speaks to them.
 */
import { describe, expect, it } from 'vitest';
import {
	decodeMessage,
	pbBool,
	pbMessageField,
	pbRepeatedMessages,
	pbString,
	pbVarint
} from './protobuf';

const encoder = new TextEncoder();

function varint(value: bigint | number): number[] {
	const bytes: number[] = [];
	let v = BigInt(value);
	do {
		const byte = Number(v & 0x7fn);
		v >>= 7n;
		bytes.push(v > 0n ? byte | 0x80 : byte);
	} while (v > 0n);
	return bytes;
}

function tag(fieldNumber: number, wireType: number): number[] {
	return varint((fieldNumber << 3) | wireType);
}

function lengthDelimited(fieldNumber: number, payload: Uint8Array | number[]): number[] {
	const bytes = [...payload];
	return [...tag(fieldNumber, 2), ...varint(bytes.length), ...bytes];
}

function bytes(...parts: number[][]): Uint8Array {
	return new Uint8Array(parts.flat());
}

describe('scalar fields', () => {
	it('decodes varints, strings and bools', () => {
		const msg = decodeMessage(
			bytes([...tag(1, 0), ...varint(150)], lengthDelimited(2, encoder.encode('hello')), [
				...tag(3, 0),
				...varint(1)
			])
		);
		expect(pbVarint(msg, 1)).toBe(150);
		expect(pbString(msg, 2)).toBe('hello');
		expect(pbBool(msg, 3)).toBe(true);
	});

	it('takes the last occurrence of a repeated scalar, per the spec', () => {
		const msg = decodeMessage(bytes([...tag(1, 0), ...varint(1)], [...tag(1, 0), ...varint(2)]));
		expect(pbVarint(msg, 1)).toBe(2);
	});

	it('returns undefined for a field that is absent or of the wrong wire type', () => {
		const msg = decodeMessage(bytes([...tag(1, 0), ...varint(7)]));
		expect(pbVarint(msg, 2)).toBeUndefined();
		// Field 1 exists but is a varint, not length-delimited.
		expect(pbString(msg, 1)).toBeUndefined();
	});
});

describe('embedded messages', () => {
	it('merges repeated occurrences field-by-field rather than keeping the last', () => {
		// The spec defines merging an embedded message as concatenating the encodings and parsing
		// the result. Last-one-wins here would drop `css` entirely — a note type that silently
		// loses its styling, or a deck that loses its description.
		const first = lengthDelimited(1, bytes([...tag(3, 0), ...varint(42)]));
		const second = lengthDelimited(1, bytes(lengthDelimited(4, encoder.encode('css'))));
		const merged = pbMessageField(decodeMessage(bytes(first, second)), 1);

		expect(merged).toBeDefined();
		expect(pbVarint(merged!, 3)).toBe(42);
		expect(pbString(merged!, 4)).toBe('css');
	});

	it('reads a repeated message field as a list in encounter order', () => {
		const msg = decodeMessage(
			bytes(
				lengthDelimited(1, bytes(lengthDelimited(1, encoder.encode('a')))),
				lengthDelimited(1, bytes(lengthDelimited(1, encoder.encode('b'))))
			)
		);
		expect(pbRepeatedMessages(msg, 1).map((m) => pbString(m, 1))).toEqual(['a', 'b']);
	});
});

describe('tolerance', () => {
	it('retains an unknown field number instead of rejecting the message', () => {
		// A newer Anki adding a field must not make an otherwise-readable collection unreadable.
		const msg = decodeMessage(bytes([...tag(1, 0), ...varint(5)], [...tag(9999, 0), ...varint(1)]));
		expect(pbVarint(msg, 1)).toBe(5);
		expect(msg.get(9999)).toHaveLength(1);
	});

	it('skips a group without failing, keeping the fields around it', () => {
		// Groups are the deprecated wire types 3/4. We do not model them, but "not modelled" is
		// not "corrupt" — throwing here would fail a whole import over a field we never read.
		const group = [
			...tag(5, 3),
			...tag(1, 0),
			...varint(7),
			...lengthDelimited(2, encoder.encode('inner')),
			...tag(5, 4)
		];
		const msg = decodeMessage(
			bytes([...tag(1, 0), ...varint(11)], group, lengthDelimited(2, encoder.encode('after')))
		);

		expect(pbVarint(msg, 1)).toBe(11);
		// Parsing continued past the group and found the field that followed it.
		expect(pbString(msg, 2)).toBe('after');
		expect(msg.get(5)?.[0]?.wire).toBe('group');
	});

	it('skips nested groups without losing the outer frame', () => {
		const nested = [
			...tag(5, 3),
			...tag(6, 3),
			...tag(1, 0),
			...varint(1),
			...tag(6, 4),
			...tag(5, 4)
		];
		const msg = decodeMessage(bytes(nested, [...tag(1, 0), ...varint(99)]));
		expect(pbVarint(msg, 1)).toBe(99);
	});
});

describe('malformed input', () => {
	it('rejects a truncated varint', () => {
		// Continuation bit set on the final byte: there is no next byte to read.
		expect(() => decodeMessage(bytes([...tag(1, 0), 0x80]))).toThrow(/truncated varint/);
	});

	it('rejects a varint longer than ten bytes', () => {
		expect(() =>
			decodeMessage(
				bytes([...tag(1, 0), 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01])
			)
		).toThrow(/longer than ten bytes/);
	});

	it('truncates a ten-byte varint to 64 bits rather than overflowing', () => {
		// Every value bit set across ten bytes. Untruncated this exceeds 2^64 and `Number()` then
		// rounds it silently; truncated it is exactly 2^64 - 1.
		const msg = decodeMessage(
			bytes([...tag(1, 0), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f])
		);
		const field = msg.get(1)?.[0];
		expect(field?.wire).toBe('varint');
		expect(field?.value).toBe((1n << 64n) - 1n);
	});

	it('rejects a length-delimited field that overruns the buffer', () => {
		// Declares 50 bytes of payload, supplies two.
		expect(() => decodeMessage(bytes([...tag(1, 2), ...varint(50), 0x01, 0x02]))).toThrow(
			/length-delimited overrun/
		);
	});

	it('rejects field number zero and an orphan end-group marker', () => {
		expect(() => decodeMessage(bytes([...tag(0, 0), ...varint(1)]))).toThrow(/field number 0/);
		expect(() => decodeMessage(bytes(tag(1, 4)))).toThrow(/end-group marker/);
	});

	it('rejects an unterminated group', () => {
		expect(() => decodeMessage(bytes([...tag(5, 3), ...tag(1, 0), ...varint(1)]))).toThrow(
			/unterminated group/
		);
	});
});
