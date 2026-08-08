import { describe, expect, it } from 'vitest';
import type { RequestEvent } from '@sveltejs/kit';
import { assertSameOrigin } from './csrf';

function makeEvent(origin: string | null): RequestEvent {
	const url = 'https://enshu.example/login';
	const headers: Record<string, string> = {};
	if (origin !== null) headers['origin'] = origin;
	return {
		request: new Request(url, { method: 'POST', headers }),
		url: new URL(url)
	} as RequestEvent;
}

describe('assertSameOrigin', () => {
	it('allows a request whose Origin matches the request URL', () => {
		expect(() => assertSameOrigin(makeEvent('https://enshu.example'))).not.toThrow();
	});

	it('rejects a request with no Origin header', () => {
		expect(() => assertSameOrigin(makeEvent(null))).toThrow(
			expect.objectContaining({ status: 403 })
		);
	});

	it('rejects a request from a different origin', () => {
		expect(() => assertSameOrigin(makeEvent('https://evil.example'))).toThrow(
			expect.objectContaining({ status: 403 })
		);
	});
});
