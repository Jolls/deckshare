/**
 * The §2.7 divergence check: what the server computed against what the client predicted.
 *
 * `predicted` is compared and then thrown away. It never influences `after`, it is never
 * stored, and there is deliberately **no table** for the mismatches — the expected rate is
 * zero, so this is an alert condition, not queryable data. A table would imply routine
 * divergence is normal, which is exactly the assumption that makes the check worthless.
 *
 * A nonzero counter means a stale client bundle, a `ts-fsrs` version skew, parameters that
 * never reached the client after a refit, or tampering. All four are bugs to fix, and
 * `recomputeUserCardState` repairs whatever they touched.
 *
 * **Never "clean this up" because it never fires** (CLAUDE.md §17). Never firing is the point.
 */
import type { CardState } from '$lib/fsrs';
import type { WireCardState } from '$lib/review/types';

/**
 * `ts-fsrs` rounds `stability` and `difficulty` to 8 decimal places, and the columns are
 * `double precision`, so a gap this wide is a real disagreement rather than a float artefact.
 */
const EPSILON = 1e-6;

export interface DivergentField {
	field: string;
	server: number | string;
	client: number | string;
}

/** Whole seconds. `due` agreeing to the millisecond is not something two clocks owe us. */
function toSecond(iso: string | Date): number {
	return Math.floor(new Date(iso).getTime() / 1000);
}

/**
 * The comparison rules of architecture.md §6, and nothing beyond them: `state`, `reps` and
 * `lapses` exactly, `stability` and `difficulty` within `1e-6`, `due` to the second.
 *
 * Returns every field that disagrees, so the log line names all of them rather than only the
 * first — a version skew moves several at once and one field is a misleading diagnosis.
 */
export function compareToPrediction(server: CardState, predicted: WireCardState): DivergentField[] {
	const out: DivergentField[] = [];

	for (const field of ['state', 'reps', 'lapses'] as const) {
		if (server[field] !== predicted[field]) {
			out.push({ field, server: server[field], client: predicted[field] });
		}
	}
	for (const field of ['stability', 'difficulty'] as const) {
		if (Math.abs(server[field] - predicted[field]) > EPSILON) {
			out.push({ field, server: server[field], client: predicted[field] });
		}
	}
	if (toSecond(server.due) !== toSecond(predicted.due)) {
		out.push({ field: 'due', server: server.due.toISOString(), client: predicted.due });
	}

	return out;
}

let divergences = 0;

/** Process-lifetime count of divergent grades. The alert signal; zero is the only good value. */
export function divergenceCount(): number {
	return divergences;
}

/**
 * Emits the structured line §6 specifies and bumps the counter. Never throws and never
 * changes what gets stored: a divergence is recorded, not acted on.
 */
export function reportDivergence(entry: {
	userId: string;
	cardId: string;
	eventId: string;
	/** The version that produced the authoritative state, from `user_fsrs_params`. */
	serverFsrsVersion: number;
	/** The version the client says produced `predicted`. Its word for it, like `predicted`. */
	clientFsrsVersion: number;
	fields: DivergentField[];
}): void {
	divergences += 1;
	console.warn(JSON.stringify({ event: 'fsrs_divergence', ...entry, count: divergences }));
}
