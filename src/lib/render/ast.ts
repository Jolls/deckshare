export type TemplateNode = TextNode | FrontSideNode | FieldNode | SectionNode;

export interface TextNode {
	type: 'text';
	value: string;
}

export interface FrontSideNode {
	type: 'frontside';
}

export interface FieldNode {
	type: 'field';
	field: string;
	/** e.g. "text", "furigana", "type", "cloze" — from `{{filter:Field}}`. Absent for `{{Field}}`. */
	filter?: string;
}

export interface SectionNode {
	type: 'section';
	field: string;
	/** true for `{{^Field}}` (render when the field is empty), false for `{{#Field}}`. */
	negate: boolean;
	children: TemplateNode[];
}

interface OpenSection {
	field: string;
	negate: boolean;
	children: TemplateNode[];
}

const TAG_RE = /\{\{([^{}]+)\}\}/g;

/**
 * Parses an Anki-style template into an AST. Template tags don't nest braces (cloze markers
 * live inside field *content*, not template text), so a single non-nested regex pass over
 * `{{...}}` is enough to tokenize.
 */
export function parseTemplate(template: string): TemplateNode[] {
	const root: TemplateNode[] = [];
	const stack: OpenSection[] = [];
	const currentChildren = (): TemplateNode[] =>
		stack.length ? stack[stack.length - 1]!.children : root;

	let lastIndex = 0;
	TAG_RE.lastIndex = 0;
	let m: RegExpExecArray | null;
	while ((m = TAG_RE.exec(template))) {
		const text = template.slice(lastIndex, m.index);
		if (text) currentChildren().push({ type: 'text', value: text });
		lastIndex = TAG_RE.lastIndex;

		const raw = m[1]!.trim();
		if (raw === 'FrontSide') {
			currentChildren().push({ type: 'frontside' });
		} else if (raw.startsWith('#')) {
			stack.push({ negate: false, field: raw.slice(1).trim(), children: [] });
		} else if (raw.startsWith('^')) {
			stack.push({ negate: true, field: raw.slice(1).trim(), children: [] });
		} else if (raw.startsWith('/')) {
			const closed = stack.pop();
			if (!closed) throw new Error(`unmatched closing tag {{${raw}}}`);
			const closedField = raw.slice(1).trim();
			if (closedField !== closed.field) {
				throw new Error(
					`mismatched section close: expected {{/${closed.field}}}, got {{/${closedField}}}`
				);
			}
			currentChildren().push({
				type: 'section',
				field: closed.field,
				negate: closed.negate,
				children: closed.children
			});
		} else {
			const colonIdx = raw.indexOf(':');
			if (colonIdx === -1) {
				currentChildren().push({ type: 'field', field: raw });
			} else {
				currentChildren().push({
					type: 'field',
					filter: raw.slice(0, colonIdx).trim(),
					field: raw.slice(colonIdx + 1).trim()
				});
			}
		}
	}

	const tail = template.slice(lastIndex);
	if (tail) currentChildren().push({ type: 'text', value: tail });

	if (stack.length) {
		throw new Error(`unclosed section {{#${stack[stack.length - 1]!.field}}}`);
	}

	return root;
}
