/** Strips HTML tags, leaving plain text. Used by `{{text:Field}}` (e.g. for TTS). */
export function textFilter(html: string): string {
	return html.replace(/<[^>]*>/g, '');
}

/**
 * Converts Anki's furigana shorthand — `漢字[かんじ]` — into `<ruby>` markup. Matches a run
 * of non-whitespace, non-bracket characters immediately followed by a bracketed reading.
 */
export function furiganaFilter(text: string): string {
	return text.replace(/([^\s[\]]+)\[([^[\]]+)\]/g, '<ruby>$1<rt>$2</rt></ruby>');
}

function escapeAttr(value: string): string {
	return value.replace(/&/g, '&amp;').replace(/"/g, '&quot;');
}

/**
 * `{{type:Field}}` — Anki's type-in-the-answer prompt. This module only renders markup, so
 * grading (comparing what the user typed against the field) belongs to the reviewer, not
 * here; this emits the input element carrying the expected answer for that later step to
 * read.
 *
 * TODO(#6): `data-answer` carries raw field HTML/text. The sanitisation pass being built in
 * issue #6 must run on rendered output — including this attribute — before it is inserted
 * into the DOM.
 */
export function typeFilter(fieldValue: string, fieldName: string): string {
	return `<input id="typeans" type="text" autocomplete="off" data-field="${escapeAttr(
		fieldName
	)}" data-answer="${escapeAttr(fieldValue)}">`;
}
