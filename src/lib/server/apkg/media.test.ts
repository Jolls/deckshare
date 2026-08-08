/**
 * Media index parsing and the content-addressed blob store.
 *
 * The collision cases below are the reason `buildMedia` warns rather than throws: a package
 * with hundreds of good files should not be rejected because two of them share a name.
 */
import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import type { IrWarning } from './ir';
import { buildMedia, mimeForFilename, normaliseFilename, parseMediaIndex } from './media';

const encoder = new TextEncoder();
const sha = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex');

describe('parseMediaIndex', () => {
	it('reads the legacy JSON map', () => {
		const raw = encoder.encode(JSON.stringify({ '0': 'cat.jpg', '1': 'audio.mp3' }));
		expect([...parseMediaIndex(raw)]).toEqual([
			['0', 'cat.jpg'],
			['1', 'audio.mp3']
		]);
	});

	it('normalises filenames to NFC as it reads them', () => {
		// 'cafe' + combining acute, as a macOS-produced package hands it back.
		const raw = encoder.encode(JSON.stringify({ '0': 'café.mp3' }));
		expect(parseMediaIndex(raw).get('0')).toBe('café.mp3');
	});

	it('treats an empty member as no media', () => {
		expect(parseMediaIndex(new Uint8Array())).toEqual(new Map());
	});

	it('rejects a `media` member that is neither JSON nor protobuf', () => {
		// Leading `{` commits it to the JSON branch, so this is a genuinely broken index rather
		// than a container we failed to recognise.
		expect(() => parseMediaIndex(encoder.encode('{not json'))).toThrow(/neither valid JSON/);
	});

	it('rejects a JSON index that maps a member to a non-string', () => {
		expect(() => parseMediaIndex(encoder.encode('{"0": 7}'))).toThrow(/non-string filename/);
	});
});

describe('buildMedia', () => {
	it('deduplicates by content hash and keeps one ref per filename', () => {
		const cat = encoder.encode('cat');
		const warnings: IrWarning[] = [];
		const media = buildMedia(
			[
				{ filename: 'a.jpg', bytes: cat },
				{ filename: 'b.jpg', bytes: cat }
			],
			warnings
		);

		expect(media.blobs).toHaveLength(1);
		expect(media.refs).toEqual([
			{ filename: 'a.jpg', sha256: sha(cat) },
			{ filename: 'b.jpg', sha256: sha(cat) }
		]);
		expect(warnings).toEqual([]);
	});

	it('keeps the first of a colliding filename and warns instead of aborting the import', () => {
		const warnings: IrWarning[] = [];
		const first = encoder.encode('first');
		const media = buildMedia(
			[
				{ filename: 'dup.jpg', bytes: first },
				{ filename: 'dup.jpg', bytes: encoder.encode('second') },
				{ filename: 'fine.jpg', bytes: encoder.encode('fine') }
			],
			warnings
		);

		// First-seen wins, and the rest of the package still imports.
		expect(media.refs.map((r) => r.filename)).toEqual(['dup.jpg', 'fine.jpg']);
		expect(media.refs[0]?.sha256).toBe(sha(first));
		expect(warnings).toHaveLength(1);
		expect(warnings[0]?.code).toBe('media-duplicate-filename');

		// The dropped file's bytes are not left behind as an unreferenced blob.
		expect(media.blobs).toHaveLength(2);
		expect(media.blobs.map((b) => b.sha256)).not.toContain(sha(encoder.encode('second')));
	});

	it('warns when NFC normalisation collapses two distinct original spellings', () => {
		// The package genuinely contained two differently-spelled names; normalising makes them one
		// key. Aborting the whole import over that would be the wrong trade.
		const warnings: IrWarning[] = [];
		const media = buildMedia(
			[
				{ filename: 'café.mp3', bytes: encoder.encode('nfd') },
				{ filename: 'café.mp3', bytes: encoder.encode('nfc') }
			],
			warnings
		);

		expect(media.refs).toHaveLength(1);
		expect(media.refs[0]?.filename).toBe('café.mp3');
		expect(warnings[0]?.code).toBe('media-duplicate-filename');
	});

	it('records size and mime per blob', () => {
		const warnings: IrWarning[] = [];
		const bytes = encoder.encode('12345');
		const media = buildMedia([{ filename: 'x.png', bytes }], warnings);
		expect(media.blobs[0]).toMatchObject({ sizeBytes: 5, mime: 'image/png' });
	});
});

describe('mimeForFilename', () => {
	it('maps known extensions case-insensitively and falls back otherwise', () => {
		expect(mimeForFilename('a.JPG')).toBe('image/jpeg');
		expect(mimeForFilename('a.ogg')).toBe('audio/ogg');
		expect(mimeForFilename('a.unknown')).toBe('application/octet-stream');
		expect(mimeForFilename('no-extension')).toBe('application/octet-stream');
	});
});

describe('normaliseFilename', () => {
	it('is idempotent', () => {
		const once = normaliseFilename('café.mp3');
		expect(normaliseFilename(once)).toBe(once);
	});
});
