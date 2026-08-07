/**
 * HTML sanitisation for rendered card content.
 *
 * Card content is untrusted input in the multiuser model — a shared or public deck means the
 * HTML being inserted into the DOM was authored by someone other than the viewer (CLAUDE.md §8).
 * Everything the template engine produces goes through `sanitiseCardHtml` before it reaches
 * `{@html ...}` or an `innerHTML` assignment.
 *
 * Isomorphic: `sanitize-html` is a pure-JS tokenising allowlist, so the same code path runs on
 * the server during SSR and in the browser during review. That matters more than raw pedigree —
 * a DOM-based sanitiser would need `jsdom` on the server, i.e. two parsers that can disagree.
 *
 * The allowlist deliberately contains **no element with a non-HTML parsing mode** — no
 * `<svg>`/`<math>` (foreign content), no `<style>`/`<script>`/`<noscript>`/`<template>`/
 * `<textarea>`/`<title>`/`<xmp>` (raw-text or escapable-raw-text). Those are where a tokeniser
 * and a browser's HTML parser diverge, and that divergence is what mutation XSS exploits. With
 * none of them in the output there is no context in which the browser re-parses text as markup.
 */
import sanitizeHtml from 'sanitize-html';

/**
 * Conservative CSS value grammar. Either a plain token run — sizes, keywords, hex colours,
 * quoted font names — or one of the four colour functions. Notably it admits no `(` outside
 * those functions, which is what keeps `url(...)`, `expression(...)` and `image-set(...)` out
 * even if a URL-accepting property were ever added below by mistake.
 */
const SAFE_CSS_VALUE = /^(?:[-#\w\s.,%!'"]+|(?:rgb|rgba|hsl|hsla)\([\d\s.,%]+\))$/i;

const CSS_PROPERTIES = [
	'color',
	'background-color',
	'font',
	'font-family',
	'font-size',
	'font-style',
	'font-variant',
	'font-weight',
	'line-height',
	'letter-spacing',
	'word-spacing',
	'text-align',
	'text-decoration',
	'text-decoration-color',
	'text-decoration-line',
	'text-decoration-style',
	'text-indent',
	'text-transform',
	'white-space',
	'word-break',
	'vertical-align',
	'direction',
	'display',
	'margin',
	'margin-top',
	'margin-right',
	'margin-bottom',
	'margin-left',
	'padding',
	'padding-top',
	'padding-right',
	'padding-bottom',
	'padding-left',
	'width',
	'height',
	'max-width',
	'max-height',
	'min-width',
	'min-height',
	'border',
	'border-color',
	'border-radius',
	'border-style',
	'border-width',
	'opacity',
	'list-style-type',
	'ruby-align',
	'ruby-position'
] as const;

const allowedStyles = Object.fromEntries(CSS_PROPERTIES.map((p) => [p, [SAFE_CSS_VALUE]]));

const options: sanitizeHtml.IOptions = {
	allowedTags: [
		// inline text
		'a',
		'abbr',
		'b',
		'bdi',
		'bdo',
		'br',
		'cite',
		'code',
		'del',
		'em',
		'i',
		'ins',
		'kbd',
		'mark',
		'q',
		's',
		'samp',
		'small',
		'span',
		'strike',
		'strong',
		'sub',
		'sup',
		'u',
		'var',
		'wbr',
		// furigana
		'ruby',
		'rb',
		'rp',
		'rt',
		'rtc',
		// block structure
		'blockquote',
		'div',
		'dd',
		'dl',
		'dt',
		'h1',
		'h2',
		'h3',
		'h4',
		'h5',
		'h6',
		'hr',
		'li',
		'ol',
		'p',
		'pre',
		'ul',
		// tables
		'caption',
		'col',
		'colgroup',
		'table',
		'tbody',
		'td',
		'tfoot',
		'th',
		'thead',
		'tr',
		// media
		'audio',
		'img',
		'source',
		'video'
	],
	allowedAttributes: {
		'*': ['class', 'dir', 'lang', 'style', 'title'],
		a: ['href'],
		audio: ['autoplay', 'controls', 'loop', 'muted', 'preload', 'src'],
		col: ['span'],
		colgroup: ['span'],
		img: ['alt', 'height', 'src', 'width'],
		ol: ['reversed', 'start'],
		source: ['src', 'type'],
		td: ['colspan', 'headers', 'rowspan'],
		th: ['abbr', 'colspan', 'headers', 'rowspan', 'scope'],
		video: [
			'autoplay',
			'controls',
			'height',
			'loop',
			'muted',
			'playsinline',
			'poster',
			'preload',
			'src',
			'width'
		]
	},
	allowedStyles: { '*': allowedStyles },
	// Relative URLs are still permitted — that is how media files resolve. These are the
	// absolute schemes we will follow; `javascript:`, `data:`, `vbscript:` and `file:` are not
	// among them, so such attributes are dropped entirely.
	allowedSchemes: ['http', 'https', 'mailto'],
	allowedSchemesByTag: {
		audio: ['http', 'https'],
		img: ['http', 'https'],
		source: ['http', 'https'],
		video: ['http', 'https']
	},
	allowProtocolRelative: false,
	// The void elements in the allowlist above, so they serialise without a bogus closing tag.
	selfClosing: ['br', 'col', 'hr', 'img', 'source', 'wbr'],
	// Drop the *contents* of these, not just the tags — otherwise script bodies and stylesheets
	// would survive as visible text.
	nonTextTags: ['script', 'style', 'template', 'textarea', 'noscript', 'title', 'xmp'],
	// The allowlists are keyed by lower-case names; `<IMG ONERROR=...>` must normalise to match.
	parser: { lowerCaseTags: true, lowerCaseAttributeNames: true },
	disallowedTagsMode: 'discard'
};

/** Sanitise rendered card HTML into a form safe to insert into the DOM. */
export function sanitiseCardHtml(html: string): string {
	return sanitizeHtml(html, options);
}
