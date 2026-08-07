/**
 * In-memory, per-process, fixed-window rate limiter for `/login` and `/signup`.
 *
 * Two things make these endpoints special enough to need this even before real traffic:
 * argon2's async calls run on Node's libuv threadpool (default size 4), so a burst of
 * concurrent junk requests can saturate every threadpool consumer in the process — not just
 * auth, but `fs`/`dns`/`zlib` too. And with nothing else in front of it, `/login` is a
 * credential-stuffing endpoint at whatever rate a single connection can push requests.
 *
 * Deliberately no Redis or other shared store: Phase 1 is single-instance (CLAUDE.md §3), and
 * a `Map` that resets on restart is the right amount of complexity for that. Revisit if the
 * stack ever goes multi-instance.
 */

interface Window {
	count: number;
	resetAt: number;
}

const windows = new Map<string, Window>();

/** Bound the map's size against an attacker who tries to grow it with many distinct keys. */
const MAX_TRACKED_KEYS = 50_000;
let lastSweep = Date.now();
const SWEEP_INTERVAL_MS = 60_000;

function sweepExpired(now: number): void {
	if (now - lastSweep < SWEEP_INTERVAL_MS && windows.size < MAX_TRACKED_KEYS) return;
	lastSweep = now;
	for (const [key, window] of windows) {
		if (now >= window.resetAt) windows.delete(key);
	}
}

/**
 * Returns `true` if `key` is still under `limit` hits within the trailing `windowMs`, and
 * records this call as one of them. Returns `false` once the limit is exceeded — callers
 * should treat that as "reject with 429", not "silently drop".
 */
export function checkRateLimit(key: string, limit: number, windowMs: number): boolean {
	const now = Date.now();
	sweepExpired(now);

	const window = windows.get(key);
	if (!window || now >= window.resetAt) {
		windows.set(key, { count: 1, resetAt: now + windowMs });
		return true;
	}

	if (window.count >= limit) return false;
	window.count++;
	return true;
}

/** Test-only: drop all tracked state between test cases. */
export function _resetRateLimitsForTests(): void {
	windows.clear();
}
