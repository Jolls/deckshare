/**
 * UUIDv7 — time-ordered UUIDs (RFC 9562 §5.7).
 *
 * Isomorphic: the client generates `review_log` ids before they reach the server, which is
 * what makes the write queue's retries idempotent (`ON CONFLICT (id) DO NOTHING`).
 * Postgres 16 has no `uuidv7()`, so ids are generated in TS on both sides.
 *
 * Layout: 48-bit big-endian Unix milliseconds, 4-bit version, 12 random bits,
 * 2-bit variant, 62 random bits.
 */
export function uuidv7(now: number = Date.now()): string {
	const bytes = new Uint8Array(16);
	crypto.getRandomValues(bytes);

	// 48-bit timestamp, big-endian.
	bytes[0] = (now / 2 ** 40) & 0xff;
	bytes[1] = (now / 2 ** 32) & 0xff;
	bytes[2] = (now / 2 ** 24) & 0xff;
	bytes[3] = (now / 2 ** 16) & 0xff;
	bytes[4] = (now / 2 ** 8) & 0xff;
	bytes[5] = now & 0xff;

	bytes[6] = (bytes[6]! & 0x0f) | 0x70; // version 7
	bytes[8] = (bytes[8]! & 0x3f) | 0x80; // variant 10

	const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
