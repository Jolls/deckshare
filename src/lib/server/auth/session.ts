/**
 * Session management — Lucia-style hand-rolled sessions (CLAUDE.md §12).
 *
 * The cookie carries a random token. Only the SHA-256 hash of that token is ever written to
 * `sessions.id`, so a read of the table (a backup, a compromised replica) discloses nothing
 * that can be replayed as a live session — the same reasoning as storing a password hash
 * instead of the password.
 */
import type { RequestEvent } from '@sveltejs/kit';
import { eq } from 'drizzle-orm';
import { db } from '$lib/server/db';
import { sessions, users } from '$lib/server/db/schema';
import { hashToken } from './token';

export { generateSessionToken, hashToken } from './token';

// `__Host-` requires the Secure attribute, Path=/, and no Domain attribute — all satisfied
// below. It's what stops a sibling subdomain (a future blog, a status page, anything with an
// XSS bug) from setting its own `Domain=enshu.example` cookie of the same name and silently
// logging a victim into an attacker-controlled session.
export const SESSION_COOKIE_NAME = '__Host-session';

const SESSION_LIFETIME_MS = 1000 * 60 * 60 * 24 * 30; // 30 days
/** Slide the expiry forward once less than this remains, so an active user is never logged out. */
const SESSION_RENEW_THRESHOLD_MS = 1000 * 60 * 60 * 24 * 15; // 15 days

export type SessionUser = Pick<typeof users.$inferSelect, 'id' | 'email' | 'displayName'>;

export async function createSession(token: string, userId: string) {
	const session = {
		id: hashToken(token),
		userId,
		expiresAt: new Date(Date.now() + SESSION_LIFETIME_MS)
	};
	await db.insert(sessions).values(session);
	return session;
}

export async function validateSessionToken(
	token: string
): Promise<{ session: typeof sessions.$inferSelect; user: SessionUser; renewed: boolean } | null> {
	const sessionId = hashToken(token);
	const [row] = await db
		.select({
			session: sessions,
			user: { id: users.id, email: users.email, displayName: users.displayName }
		})
		.from(sessions)
		.innerJoin(users, eq(sessions.userId, users.id))
		.where(eq(sessions.id, sessionId));

	if (!row) return null;

	if (Date.now() >= row.session.expiresAt.getTime()) {
		await db.delete(sessions).where(eq(sessions.id, sessionId));
		return null;
	}

	// Sliding expiration: renew once we're within the threshold of expiring. Callers use
	// `renewed` to decide whether the cookie actually needs re-sending — most requests, on
	// most days of a session's life, don't renew, and shouldn't emit `Set-Cookie`.
	let renewed = false;
	if (Date.now() >= row.session.expiresAt.getTime() - SESSION_RENEW_THRESHOLD_MS) {
		row.session.expiresAt = new Date(Date.now() + SESSION_LIFETIME_MS);
		await db
			.update(sessions)
			.set({ expiresAt: row.session.expiresAt })
			.where(eq(sessions.id, sessionId));
		renewed = true;
	}

	return { ...row, renewed };
}

export async function invalidateSession(token: string): Promise<void> {
	await db.delete(sessions).where(eq(sessions.id, hashToken(token)));
}

/** For logout, which only ever has the session id (see `locals.sessionId` in hooks.server.ts). */
export async function invalidateSessionById(sessionId: string): Promise<void> {
	await db.delete(sessions).where(eq(sessions.id, sessionId));
}

export async function invalidateAllUserSessions(userId: string): Promise<void> {
	await db.delete(sessions).where(eq(sessions.userId, userId));
}

/**
 * Sets the session cookie. `httpOnly` + `sameSite: lax` — no client script ever needs the
 * token. `secure: true` unconditionally: browsers treat `http://localhost` as a potentially
 * trustworthy origin, so this doesn't break local dev, and the `__Host-` prefix (see
 * `SESSION_COOKIE_NAME`) requires it anyway.
 */
export function setSessionCookie(event: RequestEvent, token: string, expiresAt: Date): void {
	event.cookies.set(SESSION_COOKIE_NAME, token, {
		path: '/',
		httpOnly: true,
		secure: true,
		sameSite: 'lax',
		expires: expiresAt
	});
}

export function deleteSessionCookie(event: RequestEvent): void {
	event.cookies.delete(SESSION_COOKIE_NAME, { path: '/' });
}
