/**
 * Auth flow against a real database — signup, login, wrong password, duplicate email, and
 * session expiry/invalidation. Skipped when `DATABASE_URL` is unset; CI always has one.
 *
 * Unlike `migrations.test.ts` / `day-boundary.test.ts`, this can't spin up its own throwaway
 * database: `signup.ts` / `login.ts` / `session.ts` go through the `$lib/server/db` singleton,
 * and SvelteKit's `$env/dynamic/private` snapshots `process.env` once when the Vite/Vitest
 * process starts — reassigning `process.env.DATABASE_URL` afterwards has no effect on it. So
 * this runs against whatever database `DATABASE_URL` already points to, with a random suffix
 * on every email to avoid colliding with a previous run, and deletes what it created after.
 *
 * The three modules are imported dynamically, gated on `url`, because importing them (via
 * `$lib/server/db`) throws immediately if `DATABASE_URL` isn't set — a static import would
 * crash the whole file instead of letting `describe.skipIf` skip it.
 */
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { drizzle, type PostgresJsDatabase } from 'drizzle-orm/postgres-js';
import postgres from 'postgres';
import { sql } from 'drizzle-orm';
import * as schema from '../db/schema';
import { hashToken } from './token';

const url = process.env.DATABASE_URL;
const runId = Math.random().toString(36).slice(2);
const email = (label: string) => `${label}-${runId}@auth-test.enshu.invalid`;

let client: postgres.Sql | undefined;
let db: PostgresJsDatabase<typeof schema> | undefined;

let signUp: typeof import('./signup').signUp;
let SignupError: typeof import('./signup').SignupError;
let logIn: typeof import('./login').logIn;
let LoginError: typeof import('./login').LoginError;
let validateSessionToken: typeof import('./session').validateSessionToken;
let invalidateSession: typeof import('./session').invalidateSession;
let invalidateSessionById: typeof import('./session').invalidateSessionById;
let createSession: typeof import('./session').createSession;
let generateSessionToken: typeof import('./session').generateSessionToken;

beforeAll(async () => {
	if (!url) return;
	client = postgres(url, { max: 1 });
	db = drizzle(client, { schema });

	({ signUp, SignupError } = await import('./signup'));
	({ logIn, LoginError } = await import('./login'));
	({
		validateSessionToken,
		invalidateSession,
		invalidateSessionById,
		createSession,
		generateSessionToken
	} = await import('./session'));
});

afterAll(async () => {
	if (!url || !db) return;
	// Deleting the users cascades their sessions (schema.sessions.userId is ON DELETE CASCADE).
	await db
		.delete(schema.users)
		.where(sql`lower(email) like ${'%-' + runId + '@auth-test.enshu.invalid'}`);
	await client?.end();
});

describe.skipIf(!url)('signUp', () => {
	it('creates a user and starts a valid session', async () => {
		const { user, token, expiresAt } = await signUp(email('new'), 'password123', 'New User');
		expect(user.email).toBe(email('new'));
		expect(expiresAt.getTime()).toBeGreaterThan(Date.now());

		const result = await validateSessionToken(token);
		expect(result?.user.id).toBe(user.id);
	});

	it('rejects a duplicate email, case-insensitively', async () => {
		await signUp(email('dup'), 'password123', 'First');
		await expect(signUp(email('dup').toUpperCase(), 'password123', 'Second')).rejects.toThrow(
			SignupError
		);
	});

	it('rejects a password shorter than 8 characters', async () => {
		await expect(signUp(email('short'), 'short', 'X')).rejects.toThrow(SignupError);
	});
});

describe.skipIf(!url)('logIn', () => {
	beforeAll(async () => {
		if (!url) return;
		await signUp(email('login'), 'correct-password', 'Login User');
	});

	it('succeeds with the right password', async () => {
		const { user, token } = await logIn(email('login'), 'correct-password');
		expect(user.email).toBe(email('login'));
		expect(await validateSessionToken(token)).not.toBeNull();
	});

	it('is case-insensitive on email', async () => {
		const { user } = await logIn(email('login').toUpperCase(), 'correct-password');
		expect(user.email).toBe(email('login'));
	});

	it('rejects the wrong password', async () => {
		await expect(logIn(email('login'), 'wrong-password')).rejects.toThrow(LoginError);
	});

	it('rejects an unknown email without revealing that it is unknown', async () => {
		await expect(logIn(email('nobody'), 'whatever123')).rejects.toThrow(LoginError);
	});
});

describe.skipIf(!url)('session lifecycle', () => {
	it('invalidates a session on logout', async () => {
		const { token } = await signUp(email('logout'), 'password123', 'Logout User');
		expect(await validateSessionToken(token)).not.toBeNull();

		await invalidateSession(token);
		expect(await validateSessionToken(token)).toBeNull();
	});

	it('invalidates a session by id, which is what the logout route uses', async () => {
		const { token } = await signUp(email('logout-by-id'), 'password123', 'Logout By Id');
		const result = await validateSessionToken(token);
		expect(result).not.toBeNull();

		await invalidateSessionById(result!.session.id);
		expect(await validateSessionToken(token)).toBeNull();
	});

	it('rejects an expired session', async () => {
		const { user } = await signUp(email('expired'), 'password123', 'Expired User');
		const token = generateSessionToken();
		await createSession(token, user.id);

		// Manufacture expiry directly, scoped to this exact session (not just this user —
		// signUp already started a separate session for them).
		await db!
			.update(schema.sessions)
			.set({ expiresAt: new Date(Date.now() - 1000) })
			.where(sql`${schema.sessions.id} = ${hashToken(token)}`);

		expect(await validateSessionToken(token)).toBeNull();
	});

	it('reports renewed: false for a freshly created session', async () => {
		const { token } = await signUp(email('fresh'), 'password123', 'Fresh Session');
		const result = await validateSessionToken(token);
		expect(result?.renewed).toBe(false);
	});

	it('reports renewed: true and slides expiry once inside the renewal threshold', async () => {
		const { user } = await signUp(email('renew'), 'password123', 'Renew Session');
		const token = generateSessionToken();
		await createSession(token, user.id);

		// 10 days out is inside the 15-day renewal threshold but not yet expired.
		const nearExpiry = new Date(Date.now() + 10 * 24 * 60 * 60 * 1000);
		await db!
			.update(schema.sessions)
			.set({ expiresAt: nearExpiry })
			.where(sql`${schema.sessions.id} = ${hashToken(token)}`);

		const result = await validateSessionToken(token);
		expect(result?.renewed).toBe(true);
		expect(result?.session.expiresAt.getTime()).toBeGreaterThan(nearExpiry.getTime());
	});
});
