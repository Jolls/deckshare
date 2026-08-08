/**
 * Reader tests against the synthetic fixtures in `tests/fixtures/apkg/synthetic.ts`.
 *
 * The fixtures are built to `docs/apkg-format.md`, which is unverified against a real Anki
 * build (CLAUDE.md §7). These tests therefore prove the reader is *self-consistent* with the
 * documented format — the two schemas converge, the encodings are normalised, the containers
 * unwrap — not that the documented format is right. Re-run them against a real export as soon
 * as one exists.
 */
import { createHash } from 'node:crypto';
import { zipSync } from 'fflate';
import { compress as zstdCompress } from 'zstd-napi';
import { describe, expect, it } from 'vitest';
import {
	AUDIO_BYTES,
	BASIC_NOTETYPE_ID,
	BASIC_NOTE_ID,
	CARD_CLOZE_1,
	CARD_CLOZE_2,
	CARD_DAY_LEARNING,
	CARD_LEARNING,
	CARD_NEW,
	CARD_REVIEW,
	CARD_SUSPENDED_LEARNING,
	CARD_SUSPENDED_REVIEW,
	CAT_BYTES,
	CLOZE_NOTETYPE_ID,
	CLOZE_NOTE_ID,
	CRT_SECONDS,
	DECK_ID,
	FILTERED_DECK_ID,
	NFC_FILENAME,
	REVLOG_LEARN_ID,
	REVLOG_REVIEW_ID,
	SUBDECK_ID,
	buildDowngradeStubPackage,
	buildSchema11Package,
	buildSchema18ClaimWithoutTablesPackage,
	buildSchema18Package
} from '../../../../tests/fixtures/apkg/synthetic';
import { readApkg } from './read';
import type { IrCard, IrCollection } from './ir';

const schema11 = buildSchema11Package();
const schema18 = buildSchema18Package();

const SECONDS_PER_DAY = 86_400;

function cardById(collection: IrCollection, ankiId: number): IrCard {
	const card = collection.cards.find((c) => c.ankiId === ankiId);
	if (card === undefined) throw new Error(`No card ${ankiId} in the IR.`);
	return card;
}

/** Both fixtures encode the same logical collection, so every shared assertion runs twice. */
const packages: [label: string, bytes: Uint8Array, schemaVersion: number][] = [
	['schema 11 (JSON blobs, uncompressed container)', schema11, 11],
	['schema 18 (real tables, zstd container, protobuf media index)', schema18, 18]
];

describe.each(packages)('%s', (_label, bytes, schemaVersion) => {
	const ir = readApkg(bytes);

	it('reports the collection schema and creation instant', () => {
		expect(ir.schemaVersion).toBe(schemaVersion);
		expect(ir.createdAt).toEqual(new Date(CRT_SECONDS * 1000));
	});

	it('reads note types, their fields and their templates', () => {
		expect(ir.noteTypes.map((nt) => nt.ankiId)).toEqual([BASIC_NOTETYPE_ID, CLOZE_NOTETYPE_ID]);

		const basic = ir.noteTypes[0];
		expect(basic).toMatchObject({
			name: 'Basic (JP)',
			css: '.card { font-size: 24px; }',
			isCloze: false,
			sortFieldIdx: 1
		});
		expect(basic?.fields).toEqual([
			{ ordinal: 0, name: 'Front', font: 'Noto Sans JP', size: 24, isRtl: false, sticky: false },
			{ ordinal: 1, name: 'Back', font: 'Arial', size: 20, isRtl: false, sticky: true },
			{ ordinal: 2, name: 'Notes', font: 'Arial', size: 20, isRtl: true, sticky: false }
		]);
		expect(basic?.templates[1]).toEqual({
			ordinal: 1,
			name: 'Recall',
			qfmt: '{{Back}}',
			afmt: '{{FrontSide}}<hr>{{Front}}',
			browserQfmt: '{{Back}}',
			// An empty browser format is absence, not an empty template.
			browserAfmt: null
		});

		expect(ir.noteTypes[1]?.isCloze).toBe(true);
	});

	it('orders fields and templates by ordinal, not by the order the package lists them', () => {
		// The fixture lists the basic note type's templates as [Typing(2), Recognition(0),
		// Recall(1)] and the cloze note type's fields as [Extra(1), Text(0)]. `ord` is what
		// `notes.flds` is indexed by, so array order must not win.
		expect(ir.noteTypes[0]?.templates.map((t) => t.name)).toEqual([
			'Recognition',
			'Recall',
			'Typing'
		]);
		expect(ir.noteTypes[0]?.templates.map((t) => t.ordinal)).toEqual([0, 1, 2]);
		expect(ir.noteTypes[1]?.fields.map((f) => f.name)).toEqual(['Text', 'Extra']);
		expect(ir.noteTypes[1]?.fields.map((f) => f.ordinal)).toEqual([0, 1]);
	});

	it('normalises deck names to `::` and flags filtered decks', () => {
		expect(ir.decks).toEqual([
			{ ankiId: DECK_ID, name: 'Japanese', description: 'A synthetic deck', isFiltered: false },
			{ ankiId: SUBDECK_ID, name: 'Japanese::Kanji', description: '', isFiltered: false },
			{ ankiId: FILTERED_DECK_ID, name: 'Cram', description: '', isFiltered: true }
		]);
	});

	it('splits note fields on the unit separator and keeps empty trailing fields', () => {
		const note = ir.notes.find((n) => n.ankiId === BASIC_NOTE_ID);
		expect(note?.guid).toBe('guid-basic-001');
		expect(note?.noteTypeAnkiId).toBe(BASIC_NOTETYPE_ID);
		expect(note?.fields).toEqual([
			'犬<img src="cat.jpg">',
			`dog [sound:${NFC_FILENAME}]`,
			// The note type has three fields; the third is empty and must still be present.
			''
		]);
		// Stored as ' kanji jlpt-n5 ' — surrounding spaces must not become empty tags.
		expect(note?.tags).toEqual(['kanji', 'jlpt-n5']);
		// `notes.id` is epoch milliseconds, `notes.mod` epoch seconds.
		expect(note?.createdAt).toEqual(new Date(BASIC_NOTE_ID));
		expect(note?.modifiedAt).toEqual(new Date(1_704_000_000 * 1000));

		// A cloze note: one note, four cards, and an empty tag string that must not become [''].
		const cloze = ir.notes.find((n) => n.ankiId === CLOZE_NOTE_ID);
		expect(cloze?.noteTypeAnkiId).toBe(CLOZE_NOTETYPE_ID);
		expect(cloze?.tags).toEqual([]);
		expect(
			ir.cards
				.filter((c) => c.noteAnkiId === CLOZE_NOTE_ID)
				.map((c) => c.ordinal)
				.sort((a, b) => a - b)
		).toEqual([0, 1, 2, 3, 4]);
	});

	it('assigns each note a deterministic primary deck from its lowest-numbered card', () => {
		// The basic note's cards are all in the top-level deck.
		expect(ir.notes.find((n) => n.ankiId === BASIC_NOTE_ID)?.primaryDeckAnkiId).toBe(DECK_ID);

		// The cloze note's cards span two decks, so the rule has to actually choose. Its lowest
		// card id is CARD_CLOZE_1, whose *home* deck is the subdeck — while three of its five
		// cards sit in DECK_ID, so a majority rule would answer differently.
		const clozeDecks = ir.cards
			.filter((c) => c.noteAnkiId === CLOZE_NOTE_ID)
			.map((c) => c.deckAnkiId);
		expect(new Set(clozeDecks).size).toBe(2);
		expect(clozeDecks.filter((d) => d === DECK_ID)).toHaveLength(3);
		expect(ir.notes.find((n) => n.ankiId === CLOZE_NOTE_ID)?.primaryDeckAnkiId).toBe(SUBDECK_ID);
	});

	it('reads no warnings from a well-formed package', () => {
		expect(ir.warnings).toEqual([]);
	});

	it('files a card in a filtered deck under its home deck', () => {
		expect(cardById(ir, CARD_CLOZE_1)).toMatchObject({
			deckAnkiId: SUBDECK_ID,
			filteredDeckAnkiId: FILTERED_DECK_ID
		});
		expect(cardById(ir, CARD_NEW)).toMatchObject({
			deckAnkiId: DECK_ID,
			filteredDeckAnkiId: null
		});
	});

	it('carries FSRS state from `cards.data` and never derives it from the SM-2 factor', () => {
		const review = cardById(ir, CARD_REVIEW);
		expect(review.fsrs).toEqual({ stability: 21.5, difficulty: 5.25, desiredRetention: 0.9 });
		expect(review.originalPosition).toBe(3);
		// SM-2 ease × 1000, preserved verbatim and unrelated to `fsrs.difficulty`.
		expect(review.sm2Factor).toBe(2500);
		// A card Anki never scheduled with FSRS has no memory state to import.
		expect(cardById(ir, CARD_NEW).fsrs).toBeNull();
	});

	it('records suspension and burial separately from the card state', () => {
		expect(cardById(ir, CARD_SUSPENDED_REVIEW)).toMatchObject({
			state: 'review',
			hold: 'suspended'
		});
		expect(cardById(ir, CARD_CLOZE_2)).toMatchObject({ state: 'new', hold: 'buried-user' });
		expect(cardById(ir, CARD_REVIEW)).toMatchObject({ state: 'review', hold: 'none', flag: 2 });
	});

	// -----------------------------------------------------------------------
	// Trap 1 — `cards.due`
	// -----------------------------------------------------------------------

	describe('cards.due', () => {
		it('reads a new card`s due as a queue position, not a date', () => {
			// due = 3 in the file. Wrong reading: 1970-01-01T00:00:03Z, or crt + 3 days.
			expect(cardById(ir, CARD_NEW).due).toEqual({ kind: 'position', position: 3 });
		});

		it('reads a review card`s due as days since col.crt', () => {
			// due = 5 in the file, crt = 2024-01-01T04:00:00Z -> 2024-01-06T04:00:00Z.
			expect(cardById(ir, CARD_REVIEW).due).toEqual({
				kind: 'day',
				dayOffset: 5,
				at: new Date((CRT_SECONDS + 5 * SECONDS_PER_DAY) * 1000)
			});
			expect((cardById(ir, CARD_REVIEW).due as { at: Date }).at.toISOString()).toBe(
				'2024-01-06T04:00:00.000Z'
			);
		});

		it('reads an intraday learning card`s due as an epoch-seconds timestamp', () => {
			// due = crt + 3600 in the file -> 2024-01-01T05:00:00Z, one hour after the rollover.
			expect(cardById(ir, CARD_LEARNING).due).toEqual({
				kind: 'timestamp',
				at: new Date((CRT_SECONDS + 3600) * 1000)
			});
		});

		it('reads a day-learning card`s due as days since col.crt even though type is relearning', () => {
			// type = 3 (relearning), queue = 3 (day learning), due = 2. `type` alone would not say.
			expect(cardById(ir, CARD_DAY_LEARNING).due).toEqual({
				kind: 'day',
				dayOffset: 2,
				at: new Date((CRT_SECONDS + 2 * SECONDS_PER_DAY) * 1000)
			});
		});

		it('falls back to type and magnitude when a hold has overwritten the queue', () => {
			// Suspended (queue = -1) but type = review and due = 10 -> a day offset.
			expect(cardById(ir, CARD_SUSPENDED_REVIEW).due).toEqual({
				kind: 'day',
				dayOffset: 10,
				at: new Date((CRT_SECONDS + 10 * SECONDS_PER_DAY) * 1000)
			});
			// Buried (queue = -3) but type = new and due = 11 -> still a queue position.
			expect(cardById(ir, CARD_CLOZE_2).due).toEqual({ kind: 'position', position: 11 });
		});

		it('resolves a held learning card by magnitude, as an epoch-seconds timestamp', () => {
			// The other half of the fallback: suspended (queue = -1), type = 1 (learning), and
			// due = crt + 600. `type` cannot distinguish intraday learning from day-learning, so
			// only `due >= 1_000_000_000` says this is a timestamp. Read as a day offset it would
			// land roughly 4.6 million years from now.
			const card = cardById(ir, CARD_SUSPENDED_LEARNING);
			expect(card).toMatchObject({ state: 'learning', hold: 'suspended' });
			expect(card.due).toEqual({
				kind: 'timestamp',
				at: new Date((CRT_SECONDS + 600) * 1000)
			});
			expect((card.due as { at: Date }).at.toISOString()).toBe('2024-01-01T04:10:00.000Z');
		});

		it('reads a filtered-deck card`s due from odue, which shadows due entirely', () => {
			// The card is parked in a filtered deck (odid != 0). Anki overwrote `due` with the
			// filtered deck's ordering value (-12345 in the fixture) and stashed the real value in
			// `odue` (7). Reading `due` would import a schedule that was never this card's.
			const card = cardById(ir, CARD_CLOZE_1);
			expect(card.filteredDeckAnkiId).toBe(FILTERED_DECK_ID);
			expect(card.due).toEqual({
				kind: 'day',
				dayOffset: 7,
				at: new Date((CRT_SECONDS + 7 * SECONDS_PER_DAY) * 1000)
			});
			// Specifically not the -12345 sitting in `due`.
			expect((card.due as { dayOffset: number }).dayOffset).not.toBe(-12_345);
		});
	});

	// -----------------------------------------------------------------------
	// Trap 2 — `cards.ivl` / `revlog.ivl`
	// -----------------------------------------------------------------------

	describe('cards.ivl', () => {
		it('reads a positive interval as days', () => {
			// ivl = 21 -> 21 days. Wrong reading: 21 seconds.
			expect(cardById(ir, CARD_REVIEW).intervalSeconds).toBe(21 * SECONDS_PER_DAY);
			expect(cardById(ir, CARD_SUSPENDED_REVIEW).intervalSeconds).toBe(45 * SECONDS_PER_DAY);
		});

		it('reads a negative interval as seconds', () => {
			// ivl = -600 -> ten minutes. Wrong reading: 600 days, i.e. 86,400× too long.
			expect(cardById(ir, CARD_LEARNING).intervalSeconds).toBe(600);
			// ivl = -1800 -> thirty minutes.
			expect(cardById(ir, CARD_DAY_LEARNING).intervalSeconds).toBe(1800);
		});

		it('applies the same rule to both revlog interval columns', () => {
			const review = ir.reviews.find((r) => r.ankiId === REVLOG_REVIEW_ID);
			expect(review).toMatchObject({
				cardAnkiId: CARD_REVIEW,
				ease: 3,
				kind: 'review',
				intervalSeconds: 21 * SECONDS_PER_DAY,
				lastIntervalSeconds: 10 * SECONDS_PER_DAY,
				durationMs: 4200
			});

			const learn = ir.reviews.find((r) => r.ankiId === REVLOG_LEARN_ID);
			expect(learn).toMatchObject({
				cardAnkiId: CARD_LEARNING,
				ease: 1,
				kind: 'learn',
				intervalSeconds: 600,
				lastIntervalSeconds: 60
			});
		});
	});

	it('takes the review instant from revlog.id, which is epoch milliseconds', () => {
		expect(ir.reviews.map((r) => r.reviewedAt)).toEqual([
			new Date(REVLOG_REVIEW_ID),
			new Date(REVLOG_LEARN_ID)
		]);
	});

	// -----------------------------------------------------------------------
	// Media
	// -----------------------------------------------------------------------

	describe('media', () => {
		it('deduplicates blobs by content hash while keeping every filename ref', () => {
			expect(ir.media.blobs).toHaveLength(2);
			expect(ir.media.refs).toHaveLength(3);

			const catHash = createHash('sha256').update(CAT_BYTES).digest('hex');
			const audioHash = createHash('sha256').update(AUDIO_BYTES).digest('hex');
			expect(ir.media.refs).toEqual([
				{ filename: 'cat.jpg', sha256: catHash },
				{ filename: NFC_FILENAME, sha256: audioHash },
				{ filename: 'cat-copy.jpg', sha256: catHash }
			]);
			expect(ir.media.blobs.map((b) => b.sha256).sort()).toEqual([catHash, audioHash].sort());
		});

		it('normalises non-ASCII filenames to NFC so note content resolves against them', () => {
			const ref = ir.media.refs[1];
			expect(ref?.filename).toBe(NFC_FILENAME);
			// The package spelled it NFD; without normalisation this would not match the
			// `[sound:…]` reference in the note field.
			expect(ref?.filename.normalize('NFD')).not.toBe(ref?.filename);
			const note = ir.notes.find((n) => n.ankiId === BASIC_NOTE_ID);
			expect(note?.fields[1]).toContain(ref?.filename);
		});

		it('records size and mime alongside the bytes', () => {
			const audio = ir.media.blobs.find((b) => b.sizeBytes === AUDIO_BYTES.length);
			expect(audio?.mime).toBe('audio/mpeg');
			expect(audio?.bytes).toEqual(AUDIO_BYTES);
			const image = ir.media.blobs.find((b) => b.sizeBytes === CAT_BYTES.length);
			expect(image?.mime).toBe('image/jpeg');
		});
	});

	it('is deterministic, so re-reading the same package is a no-op upsert', () => {
		// The reader's half of import idempotency (CLAUDE.md §7): the database layer upserts on
		// `(deck_id, guid)`, which is only a no-op if the same bytes produce the same IR.
		expect(readApkg(bytes)).toEqual(ir);
		expect(ir.notes.map((n) => n.guid)).toEqual(['guid-basic-001', 'guid-cloze-001']);
		expect(new Set(ir.notes.map((n) => n.guid)).size).toBe(ir.notes.length);
	});
});

describe('schema convergence', () => {
	it('produces the same IR from the schema 11 and schema 18 packages', () => {
		// The reason the IR exists: format differences must not survive past the reader.
		const ir11 = readApkg(schema11);
		const ir18 = readApkg(schema18);
		expect({ ...ir18, schemaVersion: ir11.schemaVersion }).toEqual(ir11);
	});
});

describe('container handling', () => {
	it('rejects a zip that holds no collection', () => {
		const notAPackage = zipSync({ 'readme.txt': new TextEncoder().encode('hello') });
		expect(() => readApkg(notAPackage)).toThrow(/Not an Anki package/);
	});

	it('prefers the newest collection member over the downgrade stub beside it', () => {
		// A real 2025 shared-deck export carries `collection.anki21` with the whole deck and
		// `collection.anki2` with a one-note "please upgrade" placeholder. Reading the wrong member
		// imports one note out of hundreds and looks like a success.
		const ir = readApkg(buildDowngradeStubPackage());
		expect(ir.notes).toHaveLength(2);
		expect(ir.notes.map((n) => n.guid)).toEqual(['guid-basic-001', 'guid-cloze-001']);
		expect(ir.notes.map((n) => n.guid)).not.toContain('stub-note');
	});

	it('refuses an archive with an implausible number of members', () => {
		// A zip bomb's cheapest shape: many tiny members. The package is untrusted input
		// (CLAUDE.md §8), so the ceiling is enforced before `fflate` allocates.
		const members: Record<string, Uint8Array> = {};
		for (let i = 0; i < 20; i += 1) members[`f${i}`] = new Uint8Array(1);
		expect(() => readApkg(zipSync(members), { maxMembers: 10 })).toThrow(
			/more than 10 archive members/
		);
	});

	it('refuses a member that expands beyond the per-member ceiling', () => {
		const big = zipSync({ 'collection.anki2': new Uint8Array(4096) });
		expect(() => readApkg(big, { maxMemberBytes: 1024 })).toThrow(/over the 1024-byte limit/);
	});

	it('refuses a zstd member that declares an oversized payload before decompressing it', () => {
		// The declared frame content size is checked *before* the decompress call, because
		// `zstd-napi` allocates the whole output in one uninterruptible native call. Two megabytes
		// of zeroes compress to a few dozen bytes, so nothing at the zip layer would have caught it.
		const bomb = zstdCompress(Buffer.alloc(2 * 1024 * 1024));
		expect(bomb.length).toBeLessThan(4096);
		expect(() =>
			readApkg(zipSync({ 'collection.anki21b': bomb }), { maxMemberBytes: 4096 })
		).toThrow(/declares 2097152 decompressed bytes/);
	});
});

describe('schema detection', () => {
	it('believes the tables, not col.ver, when a package claims 18 without them', () => {
		// A repacked or downgraded package: `col.ver = 18`, but the note types and decks are still
		// JSON blobs. Trusting the version number here means `no such table: notetypes`.
		const ir = readApkg(buildSchema18ClaimWithoutTablesPackage());
		expect(ir.schemaVersion).toBe(18);
		expect(ir.noteTypes.map((nt) => nt.name)).toEqual(['Basic (JP)', 'Cloze']);
		expect(ir.decks.map((d) => d.name)).toEqual(['Japanese', 'Japanese::Kanji', 'Cram']);
	});
});
