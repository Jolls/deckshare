/**
 * The package's media index, and the content-addressed blob store it turns into.
 *
 * Inside the zip, media files are named by a numeric index — `0`, `1`, `2` — and a member
 * called `media` maps those indices to real filenames. Our schema does not store files by
 * index or by name: `media_blobs` is keyed by SHA-256 and `media_refs` maps
 * `(deck_id, filename)` onto a hash, so two decks shipping the same image store one blob
 * (`docs/schema.md`). This module performs that translation and nothing else — it never
 * touches a filesystem or an object store, because the storage backend for self-hosters is
 * still open (CLAUDE.md §12) and content addressing is what keeps it swappable.
 */
import { createHash } from 'node:crypto';
import type { IrMedia, IrMediaBlob, IrMediaRef, IrWarning } from './ir';
import { decodeMessage, pbRepeatedMessages, pbString } from './protobuf';

/**
 * `MediaEntries` / `MediaEntry` are message names from Anki's separately-published `.proto`
 * interface files, not from its source code (CLAUDE.md §2.7). The **field numbers** below are
 * unverified guesses (issue #25) — the one real export inspected so far carried the legacy
 * *JSON* index, which is verified, and never reached this branch. Two different confidence
 * levels, deliberately called out.
 */
const MEDIA_ENTRIES = { entries: 1 } as const;
const MEDIA_ENTRY = { name: 1 } as const;

const MIME_BY_EXTENSION = new Map<string, string>([
	['jpg', 'image/jpeg'],
	['jpeg', 'image/jpeg'],
	['png', 'image/png'],
	['gif', 'image/gif'],
	['webp', 'image/webp'],
	['svg', 'image/svg+xml'],
	['avif', 'image/avif'],
	['mp3', 'audio/mpeg'],
	['ogg', 'audio/ogg'],
	['oga', 'audio/ogg'],
	['opus', 'audio/ogg'],
	['wav', 'audio/wav'],
	['flac', 'audio/flac'],
	['m4a', 'audio/mp4'],
	['mp4', 'video/mp4'],
	['webm', 'video/webm'],
	['mov', 'video/quicktime']
]);

const DEFAULT_MIME = 'application/octet-stream';

/**
 * Parse the `media` member into `zip member name -> filename`.
 *
 * Two encodings exist and both are in circulation:
 *
 * - the legacy one, a JSON object `{"0":"cat.jpg","1":"音声.mp3"}` keyed by the zip member name —
 *   **verified** against a real 2025 export whose 546 entries resolved one-to-one against the
 *   filenames its note content referenced, with nothing dangling either way;
 * - the modern one, a protobuf list where an entry's *position* is its zip member name —
 *   **unverified** (issue #25).
 *
 * Sniffing the encoding rather than deriving it from the package version keeps this working
 * for a package whose `meta` member is missing or unreadable.
 */
export function parseMediaIndex(raw: Uint8Array): Map<string, string> {
	if (raw.length === 0) return new Map();
	return looksLikeJson(raw) ? parseJsonMediaIndex(raw) : parseProtobufMediaIndex(raw);
}

function looksLikeJson(raw: Uint8Array): boolean {
	for (const byte of raw) {
		// Skip leading ASCII whitespace, then decide on the first real byte.
		if (byte === 0x20 || byte === 0x09 || byte === 0x0a || byte === 0x0d) continue;
		return byte === 0x7b; // '{'
	}
	return false;
}

function parseJsonMediaIndex(raw: Uint8Array): Map<string, string> {
	let parsed: unknown;
	try {
		parsed = JSON.parse(new TextDecoder().decode(raw));
	} catch (cause) {
		throw new Error('The package `media` member is neither valid JSON nor a protobuf list.', {
			cause
		});
	}
	if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
		throw new Error('The package `media` member is not a JSON object.');
	}
	const out = new Map<string, string>();
	for (const [member, filename] of Object.entries(parsed)) {
		if (typeof filename !== 'string') {
			throw new Error(`The package \`media\` member maps ${member} to a non-string filename.`);
		}
		out.set(member, normaliseFilename(filename));
	}
	return out;
}

function parseProtobufMediaIndex(raw: Uint8Array): Map<string, string> {
	const entries = pbRepeatedMessages(decodeMessage(raw), MEDIA_ENTRIES.entries);
	const out = new Map<string, string>();
	entries.forEach((entry, index) => {
		const name = pbString(entry, MEDIA_ENTRY.name);
		if (name === undefined || name === '') return;
		// The zip member name is the entry's ordinal position in the list, exactly as the legacy
		// JSON's key is.
		out.set(String(index), normaliseFilename(name));
	});
	return out;
}

/**
 * Anki stores media filenames in Unicode NFC, and a note field references them by that exact
 * string. Normalising on read is what makes a package produced on macOS — where the
 * filesystem hands back NFD — resolve against its own note content.
 */
export function normaliseFilename(filename: string): string {
	return filename.normalize('NFC');
}

/**
 * Turn `filename -> bytes` into the deduplicated blob/ref pair the schema wants.
 *
 * Deduplication is by content hash, so a package containing the same image under two names
 * yields one blob and two refs.
 *
 * A filename collision — one name, two different hashes — is **survivable, not fatal**. It
 * arises honestly (two files genuinely named the same in different source decks) and it arises
 * from our own NFC normalisation collapsing two distinct original spellings onto one key.
 * Aborting a several-hundred-file import over one of them would be the wrong trade, so the
 * first-seen mapping wins, the later one is dropped, and the caller gets a warning. First-seen
 * is stable because the media index is walked in index order, which is fixed by the package.
 */
export function buildMedia(
	files: Iterable<{ filename: string; bytes: Uint8Array }>,
	warnings: IrWarning[]
): IrMedia {
	const blobs = new Map<string, IrMediaBlob>();
	const refs = new Map<string, IrMediaRef>();

	for (const { filename, bytes } of files) {
		const name = normaliseFilename(filename);
		const sha256 = createHash('sha256').update(bytes).digest('hex');

		const existing = refs.get(name);
		if (existing !== undefined) {
			// Same bytes twice is a harmless duplicate and needs no warning; different bytes means
			// one of the two files is not going to be there when a card renders.
			if (existing.sha256 !== sha256) {
				warnings.push({
					code: 'media-duplicate-filename',
					message:
						`The package contains more than one file named "${name}" with different ` +
						`contents. Keeping the first and dropping the rest; cards referencing the ` +
						`others will show the first file.`
				});
			}
			continue;
		}

		// Only recorded once the ref is accepted, so a dropped duplicate does not leave an
		// unreferenced blob behind for the database layer to store for nothing.
		if (!blobs.has(sha256)) {
			blobs.set(sha256, { sha256, sizeBytes: bytes.length, mime: mimeForFilename(name), bytes });
		}
		refs.set(name, { filename: name, sha256 });
	}

	return { blobs: [...blobs.values()], refs: [...refs.values()] };
}

export function mimeForFilename(filename: string): string {
	const dot = filename.lastIndexOf('.');
	if (dot < 0) return DEFAULT_MIME;
	return MIME_BY_EXTENSION.get(filename.slice(dot + 1).toLowerCase()) ?? DEFAULT_MIME;
}
