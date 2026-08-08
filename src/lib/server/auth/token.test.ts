import { describe, expect, it } from 'vitest';
import { generateSessionToken, hashToken } from './token';

describe('generateSessionToken', () => {
	it('produces distinct, non-empty tokens', () => {
		const a = generateSessionToken();
		const b = generateSessionToken();
		expect(a).not.toBe(b);
		expect(a.length).toBeGreaterThan(0);
	});
});

describe('hashToken', () => {
	it('is deterministic', () => {
		const token = generateSessionToken();
		expect(hashToken(token)).toBe(hashToken(token));
	});

	it('never reproduces the raw token', () => {
		const token = generateSessionToken();
		expect(hashToken(token)).not.toBe(token);
	});

	it('produces a 64-character hex digest (SHA-256)', () => {
		expect(hashToken(generateSessionToken())).toMatch(/^[0-9a-f]{64}$/);
	});
});
