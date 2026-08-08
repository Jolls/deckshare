/**
 * A minimal, schema-less protobuf wire-format reader.
 *
 * Schema 18+ collections keep note-type, field, template and deck configuration in protobuf
 * BLOB columns rather than JSON, and the modern package format keeps its media index and its
 * `meta` header in protobuf too. Reading those is unavoidable if `.apkg` files from current
 * Anki are to be readable at all.
 *
 * This decodes the *wire format* only — the generic tag/length encoding published in the
 * protobuf language spec — and knows nothing about Anki's messages. The field numbers those
 * messages use are format facts recorded next to their readers in `anki-schema.ts` and
 * `media.ts`, never generated from Anki's `.proto` files (CLAUDE.md §2.7).
 *
 * Tolerance is the design rule: a newer Anki adding a field, or using a wire feature we do not
 * model, must not make an otherwise-readable collection unreadable. Unknown field numbers are
 * retained, and groups are retained unparsed. Only genuine corruption — a truncated varint, a
 * length that overruns the buffer — is an error.
 */

/** One decoded field occurrence. Which member is set follows from the wire type. */
export type PbField =
	| { readonly wire: 'varint'; readonly value: bigint }
	| { readonly wire: 'fixed64'; readonly value: bigint }
	| { readonly wire: 'bytes'; readonly value: Uint8Array }
	| { readonly wire: 'fixed32'; readonly value: number }
	/**
	 * A deprecated start/end-group pair, kept as its raw body rather than decoded. Anki's
	 * messages do not use groups; this exists so that one appearing cannot fail an import.
	 */
	| { readonly wire: 'group'; readonly value: Uint8Array };

/** Field number -> every occurrence of it, in encounter order. */
export type PbMessage = ReadonlyMap<number, readonly PbField[]>;

const textDecoder = new TextDecoder();

/** Varints are at most ten bytes and the value is truncated to 64 bits (protobuf spec). */
const MAX_VARINT_BYTES = 10;
const UINT64_MASK = (1n << 64n) - 1n;

const WIRE_VARINT = 0;
const WIRE_FIXED64 = 1;
const WIRE_BYTES = 2;
const WIRE_GROUP_START = 3;
const WIRE_GROUP_END = 4;
const WIRE_FIXED32 = 5;

export function decodeMessage(bytes: Uint8Array): PbMessage {
	const out = new Map<number, PbField[]>();
	let pos = 0;

	while (pos < bytes.length) {
		const key = readVarint(bytes, pos);
		pos = key.next;
		const fieldNumber = Number(key.value >> 3n);
		const wireType = Number(key.value & 7n);
		if (fieldNumber === 0) throw new Error('Malformed protobuf: field number 0.');
		if (wireType === WIRE_GROUP_END) {
			throw new Error('Malformed protobuf: end-group marker without a matching start.');
		}

		const read = readValue(bytes, pos, wireType, fieldNumber);
		pos = read.next;

		const existing = out.get(fieldNumber);
		if (existing) existing.push(read.field);
		else out.set(fieldNumber, [read.field]);
	}

	return out;
}

function readValue(
	bytes: Uint8Array,
	start: number,
	wireType: number,
	fieldNumber: number
): { field: PbField; next: number } {
	switch (wireType) {
		case WIRE_VARINT: {
			const v = readVarint(bytes, start);
			return { field: { wire: 'varint', value: v.value }, next: v.next };
		}
		case WIRE_FIXED64: {
			if (start + 8 > bytes.length) throw new Error('Malformed protobuf: fixed64 overrun.');
			const value = new DataView(bytes.buffer, bytes.byteOffset + start, 8).getBigUint64(0, true);
			return { field: { wire: 'fixed64', value }, next: start + 8 };
		}
		case WIRE_BYTES: {
			const len = readVarint(bytes, start);
			const end = len.next + Number(len.value);
			if (end > bytes.length || end < len.next) {
				throw new Error('Malformed protobuf: length-delimited overrun.');
			}
			return { field: { wire: 'bytes', value: bytes.subarray(len.next, end) }, next: end };
		}
		case WIRE_FIXED32: {
			if (start + 4 > bytes.length) throw new Error('Malformed protobuf: fixed32 overrun.');
			const value = new DataView(bytes.buffer, bytes.byteOffset + start, 4).getUint32(0, true);
			return { field: { wire: 'fixed32', value }, next: start + 4 };
		}
		case WIRE_GROUP_START: {
			// Skipped rather than decoded, and kept as opaque bytes. Failing here would contradict
			// the tolerance rule in the module header: a group is a thing we do not model, not a
			// thing that is wrong.
			const end = scanGroup(bytes, start, fieldNumber);
			return {
				field: { wire: 'group', value: bytes.subarray(start, end.bodyEnd) },
				next: end.next
			};
		}
		default:
			throw new Error(`Malformed protobuf: unknown wire type ${wireType}.`);
	}
}

/** Find the end-group marker matching a start-group at `start`, honouring nesting. */
function scanGroup(
	bytes: Uint8Array,
	start: number,
	groupField: number
): { bodyEnd: number; next: number } {
	let pos = start;
	let depth = 0;
	for (;;) {
		if (pos >= bytes.length) throw new Error('Malformed protobuf: unterminated group.');
		const key = readVarint(bytes, pos);
		const tagStart = pos;
		pos = key.next;
		const fieldNumber = Number(key.value >> 3n);
		const wireType = Number(key.value & 7n);

		if (wireType === WIRE_GROUP_END) {
			if (depth === 0) {
				if (fieldNumber !== groupField) {
					throw new Error('Malformed protobuf: mismatched end-group marker.');
				}
				return { bodyEnd: tagStart, next: pos };
			}
			depth -= 1;
			continue;
		}
		if (wireType === WIRE_GROUP_START) {
			depth += 1;
			continue;
		}
		pos = readValue(bytes, pos, wireType, fieldNumber).next;
	}
}

/** The last occurrence of `fieldNumber`, matching protobuf's last-one-wins for singular fields. */
function last(msg: PbMessage, fieldNumber: number): PbField | undefined {
	const fields = msg.get(fieldNumber);
	return fields === undefined ? undefined : fields[fields.length - 1];
}

export function pbVarint(msg: PbMessage, fieldNumber: number): number | undefined {
	const field = last(msg, fieldNumber);
	if (field === undefined) return undefined;
	if (field.wire !== 'varint') return undefined;
	return Number(field.value);
}

export function pbBool(msg: PbMessage, fieldNumber: number): boolean | undefined {
	const value = pbVarint(msg, fieldNumber);
	return value === undefined ? undefined : value !== 0;
}

export function pbBytes(msg: PbMessage, fieldNumber: number): Uint8Array | undefined {
	const field = last(msg, fieldNumber);
	if (field === undefined) return undefined;
	if (field.wire !== 'bytes') return undefined;
	return field.value;
}

export function pbString(msg: PbMessage, fieldNumber: number): string | undefined {
	const bytes = pbBytes(msg, fieldNumber);
	return bytes === undefined ? undefined : textDecoder.decode(bytes);
}

/**
 * A singular embedded message.
 *
 * Repeated occurrences of an embedded-message field **merge**, they do not overwrite: the spec
 * defines the merge as concatenating the encodings and parsing the result, which makes
 * last-one-wins (correct for scalars and strings) wrong here. A writer is free to split one
 * message across several occurrences, and taking only the last would silently drop the fields
 * carried by the earlier ones — a note type with no CSS, or a deck with no description.
 */
export function pbMessageField(msg: PbMessage, fieldNumber: number): PbMessage | undefined {
	const occurrences = (msg.get(fieldNumber) ?? []).filter(
		(f): f is Extract<PbField, { wire: 'bytes' }> => f.wire === 'bytes'
	);
	if (occurrences.length === 0) return undefined;
	return decodeMessage(concatBytes(occurrences.map((f) => f.value)));
}

/** Every occurrence of a repeated length-delimited field, in encounter order. */
export function pbRepeatedMessages(msg: PbMessage, fieldNumber: number): PbMessage[] {
	const fields = msg.get(fieldNumber) ?? [];
	return fields
		.filter((f): f is Extract<PbField, { wire: 'bytes' }> => f.wire === 'bytes')
		.map((f) => decodeMessage(f.value));
}

function concatBytes(parts: Uint8Array[]): Uint8Array {
	const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
	let offset = 0;
	for (const part of parts) {
		out.set(part, offset);
		offset += part.length;
	}
	return out;
}

function readVarint(bytes: Uint8Array, start: number): { value: bigint; next: number } {
	let value = 0n;
	let shift = 0n;
	let pos = start;
	for (let read = 0; ; read += 1) {
		const byte = bytes[pos];
		if (byte === undefined) throw new Error('Malformed protobuf: truncated varint.');
		if (read >= MAX_VARINT_BYTES) {
			throw new Error('Malformed protobuf: varint longer than ten bytes.');
		}
		pos += 1;
		value |= BigInt(byte & 0x7f) << shift;
		if ((byte & 0x80) === 0) break;
		shift += 7n;
	}
	// The tenth byte carries bit 63 plus six bits that the spec says to discard, so the value is
	// truncated rather than allowed to exceed 64 bits. Without this, a hostile varint yields a
	// bigint above 2^64 that `Number()` then silently rounds.
	return { value: value & UINT64_MASK, next: pos };
}
