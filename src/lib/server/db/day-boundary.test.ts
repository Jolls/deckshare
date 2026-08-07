/**
 * The day boundary is computed by Postgres against the IANA timezone database, so these
 * assertions have to run against a real server. Skipped when `DATABASE_URL` is unset; CI
 * always has one.
 */
import { describe, it, expect, afterAll } from 'vitest';
import { drizzle } from 'drizzle-orm/postgres-js';
import postgres from 'postgres';
import { sql } from 'drizzle-orm';
import { studyDayStart, studyDayEnd, type NowArg } from './day-boundary';

const url = process.env.DATABASE_URL;
const client = url ? postgres(url, { max: 1 }) : undefined;

afterAll(async () => {
	await client?.end();
});

/** Read the boundaries back as epoch seconds — an unambiguous wire format to assert on. */
async function boundaries(timezone: string, dayStartHour: number, now: NowArg) {
	const db = drizzle(client!);
	const [row] = await db.execute<{ day_start: string; day_end: string }>(
		sql`select extract(epoch from ${studyDayStart(timezone, dayStartHour, now)}) as day_start,
		           extract(epoch from ${studyDayEnd(timezone, dayStartHour, now)}) as day_end`
	);
	return {
		start: new Date(Number(row!.day_start) * 1000).toISOString(),
		end: new Date(Number(row!.day_end) * 1000).toISOString()
	};
}

describe.skipIf(!url)('study day boundary', () => {
	it('rolls over at 04:00 local, not midnight UTC', async () => {
		// 09:00 UTC = 11:00 CEST — after the rollover, so the study day is 2026-06-15.
		expect(await boundaries('Europe/Berlin', 4, new Date('2026-06-15T09:00:00Z'))).toEqual({
			start: '2026-06-15T02:00:00.000Z', // 04:00 CEST
			end: '2026-06-16T02:00:00.000Z'
		});
	});

	it('counts late-night study as the previous day', async () => {
		// 01:30 CEST on the 15th is still the study day that began 04:00 on the 14th.
		expect(await boundaries('Europe/Berlin', 4, new Date('2026-06-14T23:30:00Z'))).toEqual({
			start: '2026-06-14T02:00:00.000Z',
			end: '2026-06-15T02:00:00.000Z'
		});
	});

	it('honours a non-default day_start_hour', async () => {
		expect(await boundaries('Europe/Berlin', 0, new Date('2026-06-15T09:00:00Z'))).toEqual({
			start: '2026-06-14T22:00:00.000Z', // 00:00 CEST on the 15th
			end: '2026-06-15T22:00:00.000Z'
		});
	});

	describe('across a DST transition', () => {
		// America/New_York springs forward 2026-03-08 02:00 EST -> 03:00 EDT.
		it('resolves the pre-transition study day in standard time', async () => {
			// 01:30 EST on the 8th — before the 04:00 rollover, so the day began on the 7th.
			expect(await boundaries('America/New_York', 4, new Date('2026-03-08T06:30:00Z'))).toEqual({
				start: '2026-03-07T09:00:00.000Z', // 04:00 EST
				end: '2026-03-08T08:00:00.000Z' // 04:00 EDT
			});
		});

		it('makes the transition day 23 hours long, not 24', async () => {
			const { start, end } = await boundaries(
				'America/New_York',
				4,
				new Date('2026-03-08T06:30:00Z')
			);
			const hours = (Date.parse(end) - Date.parse(start)) / 3_600_000;
			expect(hours).toBe(23);
		});

		it('resolves the post-transition study day in daylight time', async () => {
			// 10:00 EDT on the 8th.
			expect(await boundaries('America/New_York', 4, new Date('2026-03-08T14:00:00Z'))).toEqual({
				start: '2026-03-08T08:00:00.000Z',
				end: '2026-03-09T08:00:00.000Z'
			});
		});

		it('makes the autumn transition day 25 hours long', async () => {
			// Falls back 2026-11-01 02:00 EDT -> 01:00 EST. 02:00 EST is inside the study day
			// that began 04:00 EDT on 10-31, and that day contains the extra hour.
			const { start, end } = await boundaries(
				'America/New_York',
				4,
				new Date('2026-11-01T07:00:00Z')
			);
			expect((Date.parse(end) - Date.parse(start)) / 3_600_000).toBe(25);
		});
	});

	describe('across a timezone change', () => {
		const instant = new Date('2026-06-15T02:00:00Z');

		it('gives the same instant different study days in different zones', async () => {
			// 03:00 BST — still yesterday's study day.
			expect(await boundaries('Europe/London', 4, instant)).toEqual({
				start: '2026-06-14T03:00:00.000Z',
				end: '2026-06-15T03:00:00.000Z'
			});
			// 11:00 JST — today's.
			expect(await boundaries('Asia/Tokyo', 4, instant)).toEqual({
				start: '2026-06-14T19:00:00.000Z',
				end: '2026-06-15T19:00:00.000Z'
			});
		});

		it('never leaves the user outside their own window', async () => {
			// The phantom-empty-queue bug: whatever zone the user is in, `now` must fall inside
			// [start, end). A client-side or UTC-midnight boundary breaks exactly this.
			for (const zone of ['Pacific/Kiritimati', 'Pacific/Niue', 'Asia/Kathmandu', 'UTC']) {
				const { start, end } = await boundaries(zone, 4, instant);
				expect(Date.parse(start)).toBeLessThanOrEqual(instant.getTime());
				expect(Date.parse(end)).toBeGreaterThan(instant.getTime());
			}
		});
	});
});
