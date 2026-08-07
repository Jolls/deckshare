import type { Side } from './types';

/** Matches `{{c1::hidden}}` and `{{c1::hidden::hint}}`. */
const CLOZE_RE = /\{\{c(\d+)::([\s\S]*?)\}\}/g;

interface ClozeMatch {
	number: number;
	hidden: string;
	hint?: string;
}

function parseInner(inner: string): { hidden: string; hint?: string } {
	// Hint is separated from the hidden text by "::". Hidden text itself is not expected
	// to contain "::" in practice (Anki's own cloze syntax has the same ambiguity).
	const hintIdx = inner.indexOf('::');
	if (hintIdx === -1) return { hidden: inner };
	return { hidden: inner.slice(0, hintIdx), hint: inner.slice(hintIdx + 2) };
}

function matches(fieldText: string): ClozeMatch[] {
	const out: ClozeMatch[] = [];
	for (const m of fieldText.matchAll(CLOZE_RE)) {
		const numRaw = m[1];
		const innerRaw = m[2];
		if (numRaw === undefined || innerRaw === undefined) continue;
		const { hidden, hint } = parseInner(innerRaw);
		out.push({ number: Number(numRaw), hidden, hint });
	}
	return out;
}

/**
 * Distinct cloze numbers present in a field's raw text, sorted ascending. Used to determine
 * how many cards a cloze note generates (one card per ordinal).
 */
export function getClozeNumbers(fieldText: string): number[] {
	const seen = new Set<number>();
	for (const { number } of matches(fieldText)) seen.add(number);
	return [...seen].sort((a, b) => a - b);
}

/**
 * Renders `{{cloze:Field}}` for a single card ordinal.
 *
 * The active cloze number (`cardOrdinal + 1`) is blanked to `[...]` (or `[hint]`) on the
 * front and revealed, highlighted, on the back. Every other cloze number is revealed as
 * plain text on both sides, matching Anki's behaviour.
 */
export function renderCloze(fieldText: string, cardOrdinal: number, side: Side): string {
	const activeNumber = cardOrdinal + 1;
	return fieldText.replace(CLOZE_RE, (_full, numRaw: string, innerRaw: string) => {
		const { hidden, hint } = parseInner(innerRaw);
		const isActive = Number(numRaw) === activeNumber;
		if (!isActive) return hidden;
		if (side === 'front') return `<span class="cloze">[${hint ?? '...'}]</span>`;
		return `<span class="cloze">${hidden}</span>`;
	});
}
