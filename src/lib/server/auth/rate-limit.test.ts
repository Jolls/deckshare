import { afterEach, describe, expect, it, vi } from 'vitest';
import { _resetRateLimitsForTests, checkRateLimit } from './rate-limit';

afterEach(() => {
	_resetRateLimitsForTests();
	vi.useRealTimers();
});

describe('checkRateLimit', () => {
	it('allows up to the limit within the window', () => {
		for (let i = 0; i < 3; i++) {
			expect(checkRateLimit('key-a', 3, 1000)).toBe(true);
		}
	});

	it('rejects once the limit is exceeded', () => {
		for (let i = 0; i < 3; i++) checkRateLimit('key-b', 3, 1000);
		expect(checkRateLimit('key-b', 3, 1000)).toBe(false);
	});

	it('tracks distinct keys independently', () => {
		for (let i = 0; i < 3; i++) checkRateLimit('key-c1', 3, 1000);
		expect(checkRateLimit('key-c1', 3, 1000)).toBe(false);
		expect(checkRateLimit('key-c2', 3, 1000)).toBe(true);
	});

	it('resets once the window elapses', () => {
		vi.useFakeTimers();
		for (let i = 0; i < 3; i++) checkRateLimit('key-d', 3, 1000);
		expect(checkRateLimit('key-d', 3, 1000)).toBe(false);

		vi.advanceTimersByTime(1001);
		expect(checkRateLimit('key-d', 3, 1000)).toBe(true);
	});
});
