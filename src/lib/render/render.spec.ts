import { describe, expect, it } from 'vitest';
import { renderCard, renderTemplate } from './render';
import { getClozeNumbers } from './cloze';
import { parseTemplate } from './ast';

describe('field substitution', () => {
	it('replaces {{Field}} with the field value', () => {
		const template = '<div>{{Front}}</div>';
		const fields = { Front: 'What is the capital of France?' };
		expect(renderTemplate(template, { fields, side: 'front' })).toBe(
			'<div>What is the capital of France?</div>'
		);
	});

	it('renders an empty string for a missing field', () => {
		const template = '{{Missing}}';
		expect(renderTemplate(template, { fields: {}, side: 'front' })).toBe('');
	});
});

describe('positive conditional {{#Field}}', () => {
	const template = 'before{{#Extra}}<i>{{Extra}}</i>{{/Extra}}after';

	it('renders the block when the field is non-empty', () => {
		expect(renderTemplate(template, { fields: { Extra: 'note' }, side: 'front' })).toBe(
			'before<i>note</i>after'
		);
	});

	it('omits the block when the field is empty', () => {
		expect(renderTemplate(template, { fields: { Extra: '' }, side: 'front' })).toBe('beforeafter');
	});

	it('treats a missing field the same as an explicitly empty one', () => {
		expect(renderTemplate(template, { fields: {}, side: 'front' })).toBe('beforeafter');
	});
});

describe('nested sections', () => {
	it('requires the parser stack, not a flat scan, to resolve correctly', () => {
		const template = '{{#A}}a-open{{#B}}b{{/B}}a-close{{/A}}';

		expect(renderTemplate(template, { fields: { A: 'yes', B: 'yes' }, side: 'front' })).toBe(
			'a-openba-close'
		);

		// Outer true, inner false: outer content renders, inner block is skipped.
		expect(renderTemplate(template, { fields: { A: 'yes', B: '' }, side: 'front' })).toBe(
			'a-opena-close'
		);

		// Outer false: nothing renders, regardless of B — proves {{/B}} closed the inner
		// frame rather than accidentally closing (or being confused with) the outer one.
		expect(renderTemplate(template, { fields: { A: '', B: 'yes' }, side: 'front' })).toBe('');
	});
});

describe('negative conditional {{^Field}}', () => {
	const template = 'before{{^Extra}}<i>no extra</i>{{/Extra}}after';

	it('renders the block when the field is empty', () => {
		expect(renderTemplate(template, { fields: { Extra: '' }, side: 'front' })).toBe(
			'before<i>no extra</i>after'
		);
	});

	it('omits the block when the field is non-empty', () => {
		expect(renderTemplate(template, { fields: { Extra: 'note' }, side: 'front' })).toBe(
			'beforeafter'
		);
	});
});

describe('malformed sections', () => {
	it('throws on an unmatched closing tag', () => {
		expect(() => parseTemplate('before{{/Foo}}after')).toThrow(/unmatched closing tag/);
	});

	it('throws on a mismatched section close', () => {
		expect(() => parseTemplate('{{#A}}text{{/B}}')).toThrow(/mismatched section close/);
	});

	it('throws on an unclosed section at end of template', () => {
		expect(() => parseTemplate('{{#A}}text')).toThrow(/unclosed section/);
	});
});

describe('{{FrontSide}}', () => {
	it('substitutes the rendered front side into the back template', () => {
		const { front, back } = renderCard('{{Front}}', '{{FrontSide}}<hr id="answer">{{Back}}', {
			Front: 'question',
			Back: 'answer'
		});
		expect(front).toBe('question');
		expect(back).toBe('question<hr id="answer">answer');
	});
});

describe('{{text:Field}} filter', () => {
	it('strips HTML tags, leaving plain text', () => {
		const template = '{{text:Front}}';
		const fields = { Front: '<b>bold</b> and <i>italic</i>' };
		expect(renderTemplate(template, { fields, side: 'front' })).toBe('bold and italic');
	});
});

describe('{{furigana:Field}} filter', () => {
	it('converts kanji[reading] shorthand into <ruby> markup', () => {
		const template = '{{furigana:Front}}';
		const fields = { Front: '漢字[かんじ]' };
		expect(renderTemplate(template, { fields, side: 'front' })).toBe(
			'<ruby>漢字<rt>かんじ</rt></ruby>'
		);
	});
});

describe('{{type:Field}} filter', () => {
	it('renders a type-answer input carrying the expected value', () => {
		const template = '{{type:Back}}';
		const fields = { Back: 'Paris' };
		expect(renderTemplate(template, { fields, side: 'front' })).toBe(
			'<input id="typeans" type="text" autocomplete="off" data-field="Back" data-answer="Paris">'
		);
	});
});

describe('cloze deletion {{cN::hidden::hint}}', () => {
	const fields = {
		Text: 'The capital of {{c1::France}} is {{c2::Paris::city}}.'
	};

	it('blanks the active ordinal and reveals the rest on the front', () => {
		const template = '{{cloze:Text}}';
		expect(renderTemplate(template, { fields, cardOrdinal: 0, side: 'front' })).toBe(
			'The capital of <span class="cloze">[...]</span> is Paris.'
		);
	});

	it('uses the hint in the blank when one is given', () => {
		const template = '{{cloze:Text}}';
		expect(renderTemplate(template, { fields, cardOrdinal: 1, side: 'front' })).toBe(
			'The capital of France is <span class="cloze">[city]</span>.'
		);
	});

	it('reveals the active ordinal, highlighted, on the back', () => {
		const template = '{{cloze:Text}}';
		expect(renderTemplate(template, { fields, cardOrdinal: 0, side: 'back' })).toBe(
			'The capital of <span class="cloze">France</span> is Paris.'
		);
	});

	it('lists distinct cloze ordinals for card generation', () => {
		expect(getClozeNumbers(fields.Text)).toEqual([1, 2]);
	});

	it('preserves skipped ordinals literally rather than re-indexing', () => {
		const text = 'The {{c1::first}} and the {{c3::third}}, no c2.';
		expect(getClozeNumbers(text)).toEqual([1, 3]);
	});

	it('returns an empty array for a field with no cloze markers', () => {
		expect(getClozeNumbers('plain text, no clozes here')).toEqual([]);
	});

	it('blanks/reveals every marker sharing the same cloze number together', () => {
		const text = '{{c1::A}} and {{c1::B}} are both hidden together.';

		expect(
			renderTemplate('{{cloze:Text}}', { fields: { Text: text }, cardOrdinal: 0, side: 'front' })
		).toBe(
			'<span class="cloze">[...]</span> and <span class="cloze">[...]</span> are both hidden together.'
		);
		expect(
			renderTemplate('{{cloze:Text}}', { fields: { Text: text }, cardOrdinal: 0, side: 'back' })
		).toBe(
			'<span class="cloze">A</span> and <span class="cloze">B</span> are both hidden together.'
		);
	});
});

describe('unknown filter', () => {
	it('falls back to the raw field value', () => {
		const template = '{{nonsense:Front}}';
		const fields = { Front: '<b>raw</b>' };
		expect(renderTemplate(template, { fields, side: 'front' })).toBe('<b>raw</b>');
	});
});

describe('filter combined with a section', () => {
	it('applies the filter only when the enclosing section is shown', () => {
		const template = '{{#Front}}{{text:Front}}{{/Front}}';
		expect(renderTemplate(template, { fields: { Front: '<b>bold</b>' }, side: 'front' })).toBe(
			'bold'
		);
		expect(renderTemplate(template, { fields: { Front: '' }, side: 'front' })).toBe('');
	});
});
