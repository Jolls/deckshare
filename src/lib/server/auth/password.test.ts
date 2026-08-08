import { describe, expect, it } from 'vitest';
import { DUMMY_PASSWORD_HASH, hashPassword, verifyPassword } from './password';

describe('password hashing', () => {
	it('round-trips through hash and verify', async () => {
		const hash = await hashPassword('correct horse battery staple');
		expect(await verifyPassword(hash, 'correct horse battery staple')).toBe(true);
	});

	it('rejects the wrong password', async () => {
		const hash = await hashPassword('correct horse battery staple');
		expect(await verifyPassword(hash, 'wrong password')).toBe(false);
	});

	// CLAUDE.md task: argon2id, not bcrypt.
	it('hashes with argon2id', async () => {
		const hash = await hashPassword('correct horse battery staple');
		expect(hash).toMatch(/^\$argon2id\$/);
	});

	// Security review: the cost parameters are declared explicitly in password.ts precisely so
	// a dependency bump can't silently weaken them without this failing.
	it('uses the declared memory/time cost, not whatever the library defaults to', async () => {
		const hash = await hashPassword('correct horse battery staple');
		expect(hash).toContain('m=19456,t=2,p=1');
	});

	it('never verifies the dummy hash against a real password', async () => {
		expect(await verifyPassword(DUMMY_PASSWORD_HASH, 'correct horse battery staple')).toBe(false);
	});
});
