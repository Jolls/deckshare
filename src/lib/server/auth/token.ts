/**
 * Session token generation/hashing, split out from `session.ts` so it has no dependency on
 * `$lib/server/db` — that module throws at import time if `DATABASE_URL` isn't set, which
 * would otherwise make this pure logic impossible to unit test without a live database.
 */
import { createHash, randomBytes } from 'node:crypto';

export function generateSessionToken(): string {
	return randomBytes(20).toString('base64url');
}

/** The value actually stored as `sessions.id` — never the raw token (see `session.ts`). */
export function hashToken(token: string): string {
	return createHash('sha256').update(token).digest('hex');
}
