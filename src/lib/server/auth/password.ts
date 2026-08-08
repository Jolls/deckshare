/**
 * Password hashing — argon2id via `@node-rs/argon2` (native binding, prebuilt), never bcrypt.
 *
 * `memoryCost`/`timeCost`/`parallelism` are passed explicitly rather than relying on the
 * library's defaults: the binding's own docs describe a default of `memoryCost: 4096,
 * timeCost: 3`, but the actual compiled default is `m=19456, t=2, p=1` (OWASP's current
 * minimum for argon2id) — correct, but silently so. An undeclared reliance on that default
 * means a future dependency bump could weaken it without review noticing. `@node-rs/argon2`
 * is pinned to an exact version in `package.json` for the same reason CLAUDE.md §3 pins
 * `ts-fsrs` exact: password hashing is correctness/security-critical, not routine.
 */
import { hash, hashSync, verify, type Options } from '@node-rs/argon2';

export const ARGON2_OPTIONS: Options = {
	memoryCost: 19456,
	timeCost: 2,
	parallelism: 1
};

export function hashPassword(password: string): Promise<string> {
	return hash(password, ARGON2_OPTIONS);
}

export function verifyPassword(hashedPassword: string, password: string): Promise<boolean> {
	return verify(hashedPassword, password);
}

/**
 * A valid argon2id hash of no real account's password. Login verifies against this when the
 * email doesn't match anyone, so a mismatched email and a wrong password take the same amount
 * of time — the difference is otherwise a user-enumeration oracle.
 */
export const DUMMY_PASSWORD_HASH = hashSync(
	'not-a-real-password-used-only-for-timing',
	ARGON2_OPTIONS
);
