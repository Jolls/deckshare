import { describe, it, expect } from 'vitest';
import { uuidv7 } from './uuid';

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe('uuidv7', () => {
	it('sets the version and variant bits', () => {
		for (let i = 0; i < 100; i++) expect(uuidv7()).toMatch(UUID_RE);
	});

	it('encodes the timestamp big-endian in the first 48 bits', () => {
		const ms = 0x0192_3f4e_5d6c;
		expect(uuidv7(ms).replace(/-/g, '').slice(0, 12)).toBe('01923f4e5d6c');
	});

	it('sorts lexicographically by generation time', () => {
		const ids = [1, 2, 3, 4, 5].map((s) => uuidv7(Date.UTC(2026, 0, s)));
		expect([...ids].sort()).toEqual(ids);
	});

	it('is unique within a millisecond', () => {
		const ids = new Set(Array.from({ length: 1000 }, () => uuidv7(1_700_000_000_000)));
		expect(ids.size).toBe(1000);
	});
});
