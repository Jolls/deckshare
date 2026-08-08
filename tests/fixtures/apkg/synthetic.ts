/**
 * Synthetic `.apkg` fixtures, built to the description in `docs/apkg-format.md`.
 *
 * > **These are not real Anki exports.** No Anki build was available when they were written,
 * > so every claim they encode — table DDL, JSON member names, protobuf field numbers, the
 * > `cards.data` key names — is the format doc's claim, and the format doc is explicitly
 * > unverified (CLAUDE.md §7). What these fixtures prove is that the reader handles the
 * > *described* format consistently: both schemas converging on one IR, the `due` and `ivl`
 * > encodings normalised, media deduplicated, zstd and protobuf containers unwrapped. They
 * > cannot prove the description is right. Replace them with real exports when one is
 * > available, and correct the doc in place when the two disagree.
 *
 * Both builders take the *same* logical collection, so a test can assert the schema-11 and
 * schema-18 readers produce the same IR — which is the whole point of having an IR.
 */
import DatabaseConstructor from 'better-sqlite3';
import { zipSync } from 'fflate';
import { compress as zstdCompress } from 'zstd-napi';

// ---------------------------------------------------------------------------
// The collection both builders encode
// ---------------------------------------------------------------------------

/** 2024-01-01T04:00:00Z — a 04:00 rollover, as Anki sets `col.crt`. */
export const CRT_SECONDS = 1_704_081_600;

export const BASIC_NOTETYPE_ID = 1_600_000_000_000;
export const CLOZE_NOTETYPE_ID = 1_600_000_000_001;
export const DECK_ID = 1_600_000_100_000;
export const SUBDECK_ID = 1_600_000_100_001;
export const FILTERED_DECK_ID = 1_600_000_100_002;

export const BASIC_NOTE_ID = 1_700_000_000_000;
export const CLOZE_NOTE_ID = 1_700_000_000_001;

export const CARD_NEW = 1_700_000_500_000;
export const CARD_REVIEW = 1_700_000_500_001;
export const CARD_LEARNING = 1_700_000_500_002;
export const CARD_SUSPENDED_REVIEW = 1_700_000_500_003;
export const CARD_DAY_LEARNING = 1_700_000_500_004;
/**
 * Deliberately the *lowest* card id of the cloze note, and the only one of its cards whose
 * resolved deck is the subdeck. That makes `IrNote.primaryDeckAnkiId` discriminating: the
 * lowest-card-id rule answers `SUBDECK_ID` while a majority-of-cards rule would answer
 * `DECK_ID`.
 */
export const CARD_CLOZE_1 = 1_700_000_499_000;
export const CARD_CLOZE_2 = 1_700_000_500_006;
export const CARD_SUSPENDED_LEARNING = 1_700_000_500_007;

export const REVLOG_REVIEW_ID = 1_704_090_000_000;
export const REVLOG_LEARN_ID = 1_704_090_500_000;

/**
 * `café.mp3` written in NFD (`e` + U+0301), which is what a macOS-produced package hands back.
 * The reader must normalise it to NFC so it matches the `[sound:café.mp3]` in the note field.
 */
export const NFD_FILENAME = 'cafe\u0301.mp3';
export const NFC_FILENAME = 'caf\u00e9.mp3';

/** Zip member holding the index-to-filename map. */
const MEDIA_MEMBER = 'media';

export const CAT_BYTES = new TextEncoder().encode('JPEG-ish cat bytes');
export const AUDIO_BYTES = new TextEncoder().encode('MP3-ish audio bytes');

interface NoteSpec {
	id: number;
	guid: string;
	mid: number;
	mod: number;
	tags: string;
	flds: string;
	csum: number;
}

interface CardSpec {
	id: number;
	nid: number;
	did: number;
	ord: number;
	mod: number;
	type: number;
	queue: number;
	due: number;
	ivl: number;
	factor: number;
	reps: number;
	lapses: number;
	odid: number;
	odue: number;
	flags: number;
	data: string;
}

interface RevlogSpec {
	id: number;
	cid: number;
	ease: number;
	ivl: number;
	lastIvl: number;
	factor: number;
	time: number;
	type: number;
}

const NOTES: NoteSpec[] = [
	{
		id: BASIC_NOTE_ID,
		guid: 'guid-basic-001',
		mid: BASIC_NOTETYPE_ID,
		mod: 1_704_000_000,
		// Anki stores tags space-separated *and* space-surrounded.
		tags: ' kanji jlpt-n5 ',
		// Three field values joined by the unit separator; the third is deliberately empty.
		flds: `犬<img src="cat.jpg">\x1fdog [sound:${NFC_FILENAME}]\x1f`,
		csum: 1_234_567
	},
	{
		id: CLOZE_NOTE_ID,
		guid: 'guid-cloze-001',
		mid: CLOZE_NOTETYPE_ID,
		mod: 1_704_000_100,
		tags: '',
		flds: 'The capital of {{c1::France}} is {{c2::Paris}}\x1fa hint',
		csum: 7_654_321
	}
];

const CARDS: CardSpec[] = [
	// Trap 1, case A: a NEW card. `due = 3` is the fourth position in the new queue, not a date.
	card({ id: CARD_NEW, nid: BASIC_NOTE_ID, ord: 0, type: 0, queue: 0, due: 3, ivl: 0 }),
	// Trap 1, case B: a REVIEW card. `due = 5` is five days after `col.crt`.
	// Trap 2, case A: `ivl = 21` is twenty-one *days*.
	card({
		id: CARD_REVIEW,
		nid: BASIC_NOTE_ID,
		ord: 1,
		type: 2,
		queue: 2,
		due: 5,
		ivl: 21,
		factor: 2500,
		reps: 4,
		lapses: 1,
		flags: 2,
		// FSRS memory state, as modern exports carry it.
		data: '{"pos":3,"s":21.5,"d":5.25,"dr":0.9}'
	}),
	// Trap 1, case C: an intraday LEARNING card. `due` is an epoch-seconds timestamp.
	// Trap 2, case B: `ivl = -600` is ten *minutes*, not six hundred days.
	card({
		id: CARD_LEARNING,
		nid: CLOZE_NOTE_ID,
		ord: 0,
		type: 1,
		queue: 1,
		due: CRT_SECONDS + 3600,
		ivl: -600,
		reps: 1
	}),
	// Trap 1, case D: SUSPENDED. `queue = -1` has overwritten the queue it came from, so only
	// the magnitude of `due` distinguishes a day offset from a timestamp.
	card({
		id: CARD_SUSPENDED_REVIEW,
		nid: BASIC_NOTE_ID,
		ord: 2,
		type: 2,
		queue: -1,
		due: 10,
		ivl: 45,
		factor: 2300,
		reps: 9,
		lapses: 2
	}),
	// Trap 1, case E: DAY-LEARNING. `type = 3` (relearning) with `queue = 3` — `type` alone
	// would not tell you how to read `due`.
	card({
		id: CARD_DAY_LEARNING,
		nid: CLOZE_NOTE_ID,
		ord: 1,
		type: 3,
		queue: 3,
		due: 2,
		ivl: -1800,
		factor: 1900,
		reps: 12,
		lapses: 3
	}),
	// Trap 1, case F: a card parked in a FILTERED deck. `did` is the filtered deck and `odid` its
	// real home — and, critically, `due` has been overwritten with the filtered deck's ordering
	// value while the card's real due day sits in `odue`. Reading `due` here imports a schedule
	// that was never this card's. The two values are deliberately far apart so a reader that
	// takes the wrong column cannot accidentally pass.
	card({
		id: CARD_CLOZE_1,
		nid: CLOZE_NOTE_ID,
		ord: 2,
		did: FILTERED_DECK_ID,
		odid: SUBDECK_ID,
		type: 2,
		queue: 2,
		due: -12_345,
		odue: 7,
		ivl: 12,
		factor: 2500,
		reps: 3
	}),
	// A buried card in the subdeck.
	card({
		id: CARD_CLOZE_2,
		nid: CLOZE_NOTE_ID,
		ord: 3,
		did: SUBDECK_ID,
		type: 0,
		queue: -3,
		due: 11,
		ivl: 0
	}),
	// Trap 1, case G: SUSPENDED while in intraday learning. `queue = -1` has erased the fact that
	// this card was in `queue = 1`, and `type = 1` does not distinguish intraday learning from
	// day-learning — so only the magnitude of `due` says this is an epoch-seconds timestamp and
	// not a day offset. This is the fallback branch's other half.
	card({
		id: CARD_SUSPENDED_LEARNING,
		nid: CLOZE_NOTE_ID,
		ord: 4,
		type: 1,
		queue: -1,
		due: CRT_SECONDS + 600,
		ivl: -300,
		reps: 2
	})
];

const REVLOG: RevlogSpec[] = [
	// `ivl`/`lastIvl` here use the same days-or-negative-seconds encoding as `cards.ivl`.
	{
		id: REVLOG_REVIEW_ID,
		cid: CARD_REVIEW,
		ease: 3,
		ivl: 21,
		lastIvl: 10,
		factor: 2500,
		time: 4200,
		type: 1
	},
	{
		id: REVLOG_LEARN_ID,
		cid: CARD_LEARNING,
		ease: 1,
		ivl: -600,
		lastIvl: -60,
		factor: 0,
		time: 8000,
		type: 0
	}
];

/**
 * Zip member name -> bytes. `cat.jpg` appears twice under different names, so the reader must
 * emit two refs and one blob.
 */
const MEDIA: { filename: string; bytes: Uint8Array }[] = [
	{ filename: 'cat.jpg', bytes: CAT_BYTES },
	{ filename: NFD_FILENAME, bytes: AUDIO_BYTES },
	{ filename: 'cat-copy.jpg', bytes: CAT_BYTES }
];

function card(spec: {
	id: number;
	nid: number;
	ord: number;
	type: number;
	queue: number;
	due: number;
	ivl: number;
	did?: number;
	odid?: number;
	odue?: number;
	factor?: number;
	reps?: number;
	lapses?: number;
	flags?: number;
	data?: string;
}): CardSpec {
	return {
		id: spec.id,
		nid: spec.nid,
		did: spec.did ?? DECK_ID,
		ord: spec.ord,
		mod: 1_704_000_200,
		type: spec.type,
		queue: spec.queue,
		due: spec.due,
		ivl: spec.ivl,
		factor: spec.factor ?? 0,
		reps: spec.reps ?? 0,
		lapses: spec.lapses ?? 0,
		odid: spec.odid ?? 0,
		odue: spec.odue ?? 0,
		flags: spec.flags ?? 0,
		data: spec.data ?? ''
	};
}

// ---------------------------------------------------------------------------
// Shared SQLite DDL
// ---------------------------------------------------------------------------

const SHARED_DDL = `
CREATE TABLE col (
  id integer primary key, crt integer not null, mod integer not null, scm integer not null,
  ver integer not null, dty integer not null, usn integer not null, ls integer not null,
  conf text not null, models text not null, decks text not null, dconf text not null,
  tags text not null);
CREATE TABLE notes (
  id integer primary key, guid text not null, mid integer not null, mod integer not null,
  usn integer not null, tags text not null, flds text not null, sfld integer not null,
  csum integer not null, flags integer not null, data text not null);
CREATE TABLE cards (
  id integer primary key, nid integer not null, did integer not null, ord integer not null,
  mod integer not null, usn integer not null, type integer not null, queue integer not null,
  due integer not null, ivl integer not null, factor integer not null, reps integer not null,
  lapses integer not null, left integer not null, odue integer not null, odid integer not null,
  flags integer not null, data text not null);
CREATE TABLE revlog (
  id integer primary key, cid integer not null, usn integer not null, ease integer not null,
  ivl integer not null, lastIvl integer not null, factor integer not null, time integer not null,
  type integer not null);
CREATE TABLE graves (usn integer not null, oid integer not null, type integer not null);
`;

/**
 * Schema 18+ moves note types and decks into real tables. Real collections declare
 * `COLLATE unicase` on the name columns; that collation is registered by the writing
 * application and cannot be registered through `better-sqlite3`, so it is omitted here.
 * Our readers never sort or compare by name, which is what keeps that safe — see the
 * open question in the reader's report.
 */
const SCHEMA_18_DDL = `
CREATE TABLE notetypes (
  id integer NOT NULL PRIMARY KEY, name text NOT NULL, mtime_secs integer NOT NULL,
  usn integer NOT NULL, config blob NOT NULL);
CREATE TABLE fields (
  ntid integer NOT NULL, ord integer NOT NULL, name text NOT NULL, config blob NOT NULL,
  PRIMARY KEY (ntid, ord));
CREATE TABLE templates (
  ntid integer NOT NULL, ord integer NOT NULL, name text NOT NULL, mtime_secs integer NOT NULL,
  usn integer NOT NULL, config blob NOT NULL, PRIMARY KEY (ntid, ord));
CREATE TABLE decks (
  id integer NOT NULL PRIMARY KEY, name text NOT NULL, mtime_secs integer NOT NULL,
  usn integer NOT NULL, common blob NOT NULL, kind blob NOT NULL);
`;

/**
 * `withTables` is separate from `ver` on purpose: a repacked or downgraded package can claim
 * `col.ver = 18` while carrying only the JSON blobs, and the reader is supposed to believe the
 * tables rather than the version number.
 */
function newCollection(
	ver: number,
	models: string,
	decks: string,
	withTables = ver >= 18
): DatabaseConstructor.Database {
	const db = new DatabaseConstructor(':memory:');
	db.exec(SHARED_DDL);
	if (withTables) db.exec(SCHEMA_18_DDL);

	db.prepare(
		`INSERT INTO col (id, crt, mod, scm, ver, dty, usn, ls, conf, models, decks, dconf, tags)
		 VALUES (1, ?, 0, 0, ?, 0, 0, 0, '{}', ?, ?, '{}', '{}')`
	).run(CRT_SECONDS, ver, models, decks);

	const insertNote = db.prepare(
		`INSERT INTO notes (id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
		 VALUES (?, ?, ?, ?, -1, ?, ?, '', ?, 0, '')`
	);
	for (const n of NOTES) insertNote.run(n.id, n.guid, n.mid, n.mod, n.tags, n.flds, n.csum);

	const insertCard = db.prepare(
		`INSERT INTO cards (id, nid, did, ord, mod, usn, type, queue, due, ivl, factor, reps,
		                    lapses, left, odue, odid, flags, data)
		 VALUES (?, ?, ?, ?, ?, -1, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`
	);
	for (const c of CARDS) {
		insertCard.run(
			c.id,
			c.nid,
			c.did,
			c.ord,
			c.mod,
			c.type,
			c.queue,
			c.due,
			c.ivl,
			c.factor,
			c.reps,
			c.lapses,
			c.odue,
			c.odid,
			c.flags,
			c.data
		);
	}

	const insertRevlog = db.prepare(
		`INSERT INTO revlog (id, cid, usn, ease, ivl, lastIvl, factor, time, type)
		 VALUES (?, ?, -1, ?, ?, ?, ?, ?, ?)`
	);
	for (const r of REVLOG) {
		insertRevlog.run(r.id, r.cid, r.ease, r.ivl, r.lastIvl, r.factor, r.time, r.type);
	}

	return db;
}

// ---------------------------------------------------------------------------
// Schema 11 — note types and decks as JSON blobs in `col`
// ---------------------------------------------------------------------------

const MODELS_JSON = JSON.stringify({
	[String(BASIC_NOTETYPE_ID)]: {
		id: BASIC_NOTETYPE_ID,
		name: 'Basic (JP)',
		type: 0,
		sortf: 1,
		css: '.card { font-size: 24px; }',
		flds: [
			{ name: 'Front', ord: 0, font: 'Noto Sans JP', size: 24, rtl: false, sticky: false },
			{ name: 'Back', ord: 1, font: 'Arial', size: 20, rtl: false, sticky: true },
			{ name: 'Notes', ord: 2, font: 'Arial', size: 20, rtl: true, sticky: false }
		],
		// Deliberately listed out of `ord` order. `ord` is authoritative — it is what `notes.flds`
		// is indexed by — and the schema-18 tables are read `ORDER BY ntid, ord`, so a JSON reader
		// that trusted array order would disagree with the table reader here. Leaving the fixture
		// pre-sorted would make the convergence test unable to notice.
		tmpls: [
			{ name: 'Typing', ord: 2, qfmt: '{{type:Front}}', afmt: '{{Front}}' },
			{ name: 'Recognition', ord: 0, qfmt: '{{Front}}', afmt: '{{FrontSide}}<hr>{{Back}}' },
			{
				name: 'Recall',
				ord: 1,
				qfmt: '{{Back}}',
				afmt: '{{FrontSide}}<hr>{{Front}}',
				bqfmt: '{{Back}}',
				bafmt: ''
			}
		]
	},
	[String(CLOZE_NOTETYPE_ID)]: {
		id: CLOZE_NOTETYPE_ID,
		name: 'Cloze',
		type: 1,
		sortf: 0,
		css: '.cloze { font-weight: bold; }',
		// Also out of `ord` order, for the same reason as the templates above.
		flds: [
			{ name: 'Extra', ord: 1, font: 'Arial', size: 20 },
			{ name: 'Text', ord: 0, font: 'Arial', size: 20 }
		],
		tmpls: [{ name: 'Cloze', ord: 0, qfmt: '{{cloze:Text}}', afmt: '{{cloze:Text}}<br>{{Extra}}' }]
	}
});

const DECKS_JSON = JSON.stringify({
	[String(DECK_ID)]: { id: DECK_ID, name: 'Japanese', desc: 'A synthetic deck', dyn: 0 },
	// Schema 11 spells the hierarchy with `::`.
	[String(SUBDECK_ID)]: { id: SUBDECK_ID, name: 'Japanese::Kanji', desc: '', dyn: 0 },
	[String(FILTERED_DECK_ID)]: { id: FILTERED_DECK_ID, name: 'Cram', desc: '', dyn: 1 }
});

export function buildSchema11Package(): Uint8Array {
	const db = newCollection(11, MODELS_JSON, DECKS_JSON);
	const collection = db.serialize();
	db.close();

	// Legacy container: uncompressed members, a JSON media index, media named by index.
	const mediaIndex: Record<string, string> = {};
	const members: Record<string, Uint8Array> = { 'collection.anki2': new Uint8Array(collection) };
	MEDIA.forEach((file, i) => {
		mediaIndex[String(i)] = file.filename;
		members[String(i)] = file.bytes;
	});
	members[MEDIA_MEMBER] = new TextEncoder().encode(JSON.stringify(mediaIndex));

	return zipSync(members);
}

// ---------------------------------------------------------------------------
// Schema 18+ — real tables, protobuf configs, zstd container, protobuf media index
// ---------------------------------------------------------------------------

export function buildSchema18Package(): Uint8Array {
	// The JSON blobs are emptied in schema 18; the tables are authoritative.
	const db = newCollection(18, '', '');

	const insertNotetype = db.prepare(
		'INSERT INTO notetypes (id, name, mtime_secs, usn, config) VALUES (?, ?, 0, -1, ?)'
	);
	const insertField = db.prepare(
		'INSERT INTO fields (ntid, ord, name, config) VALUES (?, ?, ?, ?)'
	);
	const insertTemplate = db.prepare(
		'INSERT INTO templates (ntid, ord, name, mtime_secs, usn, config) VALUES (?, ?, ?, 0, -1, ?)'
	);
	const insertDeck = db.prepare(
		'INSERT INTO decks (id, name, mtime_secs, usn, common, kind) VALUES (?, ?, 0, -1, ?, ?)'
	);

	for (const model of Object.values(JSON.parse(MODELS_JSON) as Record<string, SchemaElevenModel>)) {
		insertNotetype.run(
			model.id,
			model.name,
			// NotetypeConfig: 1 kind, 2 sort_field_idx, 3 css.
			Buffer.from(concat(pbVarint(1, model.type), pbVarint(2, model.sortf), pbString(3, model.css)))
		);
		for (const f of model.flds) {
			insertField.run(
				model.id,
				f.ord,
				f.name,
				// FieldConfig: 1 sticky, 2 rtl, 3 font_name, 4 font_size.
				Buffer.from(
					concat(
						pbVarint(1, f.sticky === true ? 1 : 0),
						pbVarint(2, f.rtl === true ? 1 : 0),
						pbString(3, f.font),
						pbVarint(4, f.size)
					)
				)
			);
		}
		for (const t of model.tmpls) {
			insertTemplate.run(
				model.id,
				t.ord,
				t.name,
				// CardTemplateConfig: 1 q_format, 2 a_format, 3 q_format_browser, 4 a_format_browser.
				Buffer.from(
					concat(
						pbString(1, t.qfmt),
						pbString(2, t.afmt),
						pbString(3, t.bqfmt ?? ''),
						pbString(4, t.bafmt ?? '')
					)
				)
			);
		}
	}

	for (const deck of Object.values(JSON.parse(DECKS_JSON) as Record<string, SchemaElevenDeck>)) {
		// DeckKindContainer: 1 normal, 2 filtered. DeckNormal: 4 description.
		const kind =
			deck.dyn === 1 ? pbSubMessage(2, new Uint8Array()) : pbSubMessage(1, pbString(4, deck.desc));
		insertDeck.run(
			deck.id,
			// Schema 18 spells the hierarchy with the unit separator, not `::`.
			deck.name.split('::').join('\x1f'),
			Buffer.alloc(0),
			Buffer.from(kind)
		);
	}

	const collection = db.serialize();
	db.close();

	// Modern container: the collection and the media index are zstd-compressed; the media files
	// themselves are stored as-is. Only those two members are compressed, which is why the reader
	// sniffs for a zstd frame on exactly those two and not on media — a media file whose first
	// four bytes happened to be the zstd magic would otherwise be mangled.
	const members: Record<string, Uint8Array> = {
		'collection.anki21b': zstdCompress(Buffer.from(collection)),
		// Meta { version = 3 }, present but unread — the reader sniffs the zstd frame magic
		// instead of trusting a version number the format doc has not verified.
		meta: pbVarint(1, 3)
	};
	const entries: Uint8Array[] = [];
	MEDIA.forEach((file, i) => {
		// MediaEntries { repeated MediaEntry entries = 1 }, MediaEntry { name = 1, size = 2 }.
		entries.push(
			pbSubMessage(1, concat(pbString(1, file.filename), pbVarint(2, file.bytes.length)))
		);
		members[String(i)] = file.bytes;
	});
	members[MEDIA_MEMBER] = zstdCompress(Buffer.from(concat(...entries)));

	return zipSync(members);
}

/**
 * A package that *claims* schema 18 but carries the schema-11 JSON blobs and none of the real
 * tables — what a repack, a downgrade, or a third-party exporter can produce.
 *
 * The reader must decide from table presence, not from `col.ver`: trusting the version number
 * sends it to `readNoteTypesFromTables` and straight into `no such table: notetypes`.
 */
export function buildSchema18ClaimWithoutTablesPackage(): Uint8Array {
	const db = newCollection(18, MODELS_JSON, DECKS_JSON, false);
	const collection = db.serialize();
	db.close();

	return zipSync({
		'collection.anki2': new Uint8Array(collection),
		[MEDIA_MEMBER]: new TextEncoder().encode('{}')
	});
}

interface SchemaElevenModel {
	id: number;
	name: string;
	type: number;
	sortf: number;
	css: string;
	flds: {
		name: string;
		ord: number;
		font: string;
		size: number;
		rtl?: boolean;
		sticky?: boolean;
	}[];
	tmpls: {
		name: string;
		ord: number;
		qfmt: string;
		afmt: string;
		bqfmt?: string;
		bafmt?: string;
	}[];
}

interface SchemaElevenDeck {
	id: number;
	name: string;
	desc: string;
	dyn: number;
}

// ---------------------------------------------------------------------------
// A minimal protobuf *encoder*, deliberately separate from the reader's decoder
// ---------------------------------------------------------------------------

function encodeVarint(value: number): Uint8Array {
	const bytes: number[] = [];
	let v = value;
	do {
		const byte = v & 0x7f;
		v >>>= 7;
		bytes.push(v > 0 ? byte | 0x80 : byte);
	} while (v > 0);
	return new Uint8Array(bytes);
}

function pbVarint(fieldNumber: number, value: number): Uint8Array {
	return concat(encodeVarint((fieldNumber << 3) | 0), encodeVarint(value));
}

function pbBytes(fieldNumber: number, value: Uint8Array): Uint8Array {
	return concat(encodeVarint((fieldNumber << 3) | 2), encodeVarint(value.length), value);
}

function pbString(fieldNumber: number, value: string): Uint8Array {
	return pbBytes(fieldNumber, new TextEncoder().encode(value));
}

function pbSubMessage(fieldNumber: number, value: Uint8Array): Uint8Array {
	return pbBytes(fieldNumber, value);
}

function concat(...parts: Uint8Array[]): Uint8Array {
	const total = parts.reduce((n, p) => n + p.length, 0);
	const out = new Uint8Array(total);
	let offset = 0;
	for (const part of parts) {
		out.set(part, offset);
		offset += part.length;
	}
	return out;
}
