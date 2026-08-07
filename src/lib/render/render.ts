/**
 * Anki-style note template rendering: `(template, fields) -> html`.
 *
 * Pure and isomorphic — no DB, no `fetch`, no browser globals (CLAUDE.md §8, §4). Runs
 * identically on the client and server.
 *
 * TODO(#6): this module's output is raw field HTML, verbatim. Field content is
 * user-authored and, for shared decks, other users' — untrusted input. Callers MUST run the
 * result through the sanitisation pass landing in issue #6 before inserting it into the DOM.
 * Nothing here does that; it's out of scope for the template engine.
 */
import { parseTemplate, type TemplateNode } from './ast';
import { renderCloze } from './cloze';
import { furiganaFilter, textFilter, typeFilter } from './filters';
import type { RenderContext } from './types';

function isFieldTruthy(field: string, ctx: RenderContext): boolean {
	return (ctx.fields[field] ?? '').trim() !== '';
}

function renderField(node: Extract<TemplateNode, { type: 'field' }>, ctx: RenderContext): string {
	const value = ctx.fields[node.field] ?? '';
	switch (node.filter) {
		case undefined:
			return value;
		case 'text':
			return textFilter(value);
		case 'furigana':
			return furiganaFilter(value);
		case 'type':
			return typeFilter(value, node.field);
		case 'cloze':
			return renderCloze(value, ctx.cardOrdinal ?? 0, ctx.side);
		default:
			// Unknown filter: Anki would render nothing useful either. Fall back to the raw
			// value rather than silently dropping content.
			return value;
	}
}

function renderNode(node: TemplateNode, ctx: RenderContext): string {
	switch (node.type) {
		case 'text':
			return node.value;
		case 'frontside':
			return ctx.frontHtml ?? '';
		case 'field':
			return renderField(node, ctx);
		case 'section': {
			const truthy = isFieldTruthy(node.field, ctx);
			const show = node.negate ? !truthy : truthy;
			return show ? renderNodes(node.children, ctx) : '';
		}
	}
}

function renderNodes(nodes: TemplateNode[], ctx: RenderContext): string {
	return nodes.map((n) => renderNode(n, ctx)).join('');
}

/** Renders a single template (qfmt or afmt) against field values and card context. */
export function renderTemplate(template: string, ctx: RenderContext): string {
	return renderNodes(parseTemplate(template), ctx);
}

/** Convenience wrapper: renders both sides of a card, wiring up `{{FrontSide}}`. */
export function renderCard(
	qfmt: string,
	afmt: string,
	fields: RenderContext['fields'],
	cardOrdinal = 0
): { front: string; back: string } {
	const front = renderTemplate(qfmt, { fields, cardOrdinal, side: 'front' });
	const back = renderTemplate(afmt, { fields, cardOrdinal, side: 'back', frontHtml: front });
	return { front, back };
}
