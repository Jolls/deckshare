/**
 * CSRF protection for state-changing requests (CLAUDE.md §12).
 *
 * This is the only defence layer: every non-GET/HEAD request's `Origin` header must match the
 * request URL's origin, enforced centrally in `hooks.server.ts` so no future route can ship
 * without it by forgetting to call this. (An earlier version of this comment claimed the JSON
 * `Content-Type` requirement was a second layer — nothing actually checks `Content-Type`, so
 * that was never true. Don't rely on it as a fallback.)
 */
import { error, type RequestEvent } from '@sveltejs/kit';

export function assertSameOrigin(event: RequestEvent): void {
	const origin = event.request.headers.get('origin');
	if (!origin || origin !== event.url.origin) {
		error(403, 'Cross-site request rejected');
	}
}
