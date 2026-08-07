/**
 * The study-day boundary.
 *
 * "Due today" is not midnight UTC. It is a per-user rollover hour (default 04:00 local) so
 * late-night study counts as the previous day — `users.timezone` + `users.day_start_hour`.
 *
 * It is computed **in the query**, not in the client: a user who crosses a timezone must not
 * see a phantom empty queue, and the client's clock is not authoritative. Every queue query
 * depends on these expressions.
 */
import { sql, type SQL, type SQLWrapper } from 'drizzle-orm';
import { users } from './schema';

/** A timezone value: normally the `users.timezone` column, or an IANA name for tests. */
export type TimezoneArg = SQLWrapper | string;
/** A rollover hour: normally the `users.day_start_hour` column, or a literal 0–23. */
export type DayStartHourArg = SQLWrapper | number;
/** The instant to resolve the study day around. Defaults to `now()` in the database. */
export type NowArg = SQLWrapper | Date;

/**
 * Boundary `offsetDays` study-days from the one containing `now`.
 *
 * The arithmetic happens on the *local wall clock* — convert to local time, subtract the
 * rollover hour so `date_trunc` lands on the right day, truncate, add the hour back, then
 * convert to UTC. Doing it this way is what makes it correct across a DST transition: the
 * next boundary is the next 04:00 local, which may be 23 or 25 hours away, not 24.
 */
function studyDayBoundary(
	timezone: TimezoneArg,
	dayStartHour: DayStartHourArg,
	now: NowArg,
	offsetDays: number
): SQL<Date> {
	const tz = sql`${timezone}::text`;
	const hour = sql`make_interval(hours => (${dayStartHour})::int)`;
	// postgres-js cannot bind a Date to an explicitly-cast placeholder; send ISO 8601 text.
	const instant = now instanceof Date ? now.toISOString() : now;
	const local = sql`((${instant})::timestamptz AT TIME ZONE ${tz})`;
	const offset = sql`make_interval(days => ${offsetDays})`;
	return sql`((date_trunc('day', ${local} - ${hour}) + ${hour} + ${offset}) AT TIME ZONE ${tz})`;
}

/** The instant the user's current study day began. */
export function studyDayStart(
	timezone: TimezoneArg = users.timezone,
	dayStartHour: DayStartHourArg = users.dayStartHour,
	now: NowArg = sql`now()`
): SQL<Date> {
	return studyDayBoundary(timezone, dayStartHour, now, 0);
}

/**
 * The instant the user's current study day ends — the exclusive upper bound of the queue
 * window. A card counts as due today when `user_card_state.due < studyDayEnd(...)`.
 */
export function studyDayEnd(
	timezone: TimezoneArg = users.timezone,
	dayStartHour: DayStartHourArg = users.dayStartHour,
	now: NowArg = sql`now()`
): SQL<Date> {
	return studyDayBoundary(timezone, dayStartHour, now, 1);
}
