/**
 * Anki note fields, keyed by field name, holding raw (already-HTML) field content.
 */
export type Fields = Record<string, string>;

export type Side = 'front' | 'back';

export interface RenderContext {
	/** Note field values, keyed by field name. */
	fields: Fields;
	/**
	 * Zero-based cloze ordinal for the card being rendered (card N shows cloze number N+1).
	 * Ignored by templates that don't use the `cloze:` filter.
	 */
	cardOrdinal?: number;
	/** Which side is being rendered — affects `{{cloze:Field}}` blanking. */
	side: Side;
	/** Rendered front-side HTML, substituted for `{{FrontSide}}` when rendering the back. */
	frontHtml?: string;
}
