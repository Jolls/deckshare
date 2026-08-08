/**
 * Anki's SQLite shapes, for both collection schemas, plus the readers that flatten each of
 * them into the shared IR.
 *
 * Everything here is a statement about an external file format, so it earns comments in a way
 * a route handler does not (CLAUDE.md §9). Facts came from `docs/apkg-format.md` and public
 * format descriptions — never from `ankitects/anki` source (CLAUDE.md §2.7).
 *
 * Two layouts must both be readable:
 *
 * - **Schema 11 and below — verified.** `notetypes` and `decks` do not exist; the same data
 *   lives as JSON blobs in the single-row `col` table (`col.models`, `col.decks`). Checked
 *   against a real 2025 shared-deck export (319 notes, 1 note type of 8 fields and 4
 *   templates); see the verification section of `docs/apkg-format.md`. Still the layout a
 *   widely distributed deck ships in, so this is not a legacy path.
 * - **Schema 18 and above — unverified.** Real `notetypes` / `fields` / `templates` / `decks`
 *   tables, whose configuration columns are protobuf BLOBs rather than JSON. `col.models` and
 *   `col.decks` still exist as columns but are emptied. No real schema-18 file has been
 *   inspected.
 *
 * **Two different confidence levels, do not conflate them.** The protobuf *message names*
 * below (`NotetypeConfig`, `FieldConfig`, `CardTemplateConfig`, `DeckKindContainer`) come from
 * Anki's separately-published `.proto` interface files — interface definitions, not source code
 * (CLAUDE.md §2.7). The *field numbers* are unverified guesses.
 *
 * > **Issue #25 is still open.** The protobuf field numbers below remain **unverified against a
 * > real Anki build**. The one real export inspected so far (2026-08-07) turned out to be
 * > schema 11, so it exercised the JSON readers below and none of the protobuf ones. These
 * > constants are the one part of this module a synthetic fixture cannot validate, because the
 * > fixture is written with the same constants it is read with — a wrong number passes every
 * > test we have and yields a garbled or empty note type against a real file. Getting any
 * > schema-18 export (a `.colpkg`, or an `.apkg` exported without "support older Anki
 * > versions") is what closes this.
 */
import type Database from 'better-sqlite3';
import type { IrDeck, IrField, IrNoteType, IrTemplate } from './ir';
import { decodeMessage, pbBool, pbBytes, pbMessageField, pbString, pbVarint } from './protobuf';

// ---------------------------------------------------------------------------
// Format constants
// ---------------------------------------------------------------------------

/** `col.ver` at and above which note types and decks are real tables rather than JSON blobs. */
export const SCHEMA_18 = 18;

/** Note fields are joined by the unit separator, not a printable delimiter. */
export const FIELD_SEPARATOR = '\x1f';

/** `cards.type`, in `ts-fsrs` `State` order. */
export const CARD_TYPE = { new: 0, learning: 1, review: 2, relearning: 3 } as const;

/**
 * `cards.queue`. Negative values are holds (suspension, burial) and say nothing about the
 * card's `type`; positive values say which queue the card sits in, which is what decides how
 * `cards.due` must be read.
 */
export const CARD_QUEUE = {
	userBuried: -3,
	schedulerBuried: -2,
	suspended: -1,
	new: 0,
	learning: 1,
	review: 2,
	dayLearning: 3,
	preview: 4
} as const;

/** `revlog.type`, matching `review_log.review_kind`'s 0–4 check constraint. */
export const REVLOG_TYPE = { learn: 0, review: 1, relearn: 2, filtered: 3, manual: 4 } as const;

/**
 * Field numbers in schema 18's `notetypes.config` protobuf. Unverified — see the module note.
 * `kind` is 0 for a standard note type and 1 for cloze.
 */
const NOTETYPE_CONFIG = { kind: 1, sortFieldIdx: 2, css: 3 } as const;

/** Field numbers in schema 18's `fields.config` protobuf. Unverified. */
const FIELD_CONFIG = { sticky: 1, rtl: 2, fontName: 3, fontSize: 4 } as const;

/** Field numbers in schema 18's `templates.config` protobuf. Unverified. */
const TEMPLATE_CONFIG = {
	qFormat: 1,
	aFormat: 2,
	qFormatBrowser: 3,
	aFormatBrowser: 4
} as const;

/**
 * Field numbers in schema 18's `decks.kind` protobuf. The container holds either a `normal`
 * or a `filtered` submessage, and which one is present is how a filtered deck is recognised.
 * Unverified.
 */
const DECK_KIND = { normal: 1, filtered: 2 } as const;
/** Field number of the description inside the `normal` deck submessage. Unverified. */
const DECK_NORMAL_DESCRIPTION = 4;

/** Anki's own default field font, applied when a package omits one. */
const DEFAULT_FONT = 'Arial';
const DEFAULT_FONT_SIZE = 20;

// ---------------------------------------------------------------------------
// Row shapes
// ---------------------------------------------------------------------------

/** The single-row `col` table. Present in both schemas; the JSON columns are empty in 18+. */
export interface ColRow {
	crt: number;
	ver: number;
	models: string | null;
	decks: string | null;
}

export interface NoteRow {
	id: number;
	guid: string;
	mid: number;
	mod: number;
	tags: string;
	flds: string;
	csum: number;
}

export interface CardRow {
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
	/**
	 * The card's **real** due value, stashed here while `odid` is non-zero. Moving a card into a
	 * filtered deck overwrites `due` with the filtered deck's ordering value, so for a displaced
	 * card `due` is not the schedule and `odue` is. Meaningless when `odid` is 0.
	 */
	odue: number;
	flags: number;
	data: string | null;
}

export interface RevlogRow {
	id: number;
	cid: number;
	ease: number;
	ivl: number;
	lastIvl: number;
	factor: number;
	time: number;
	type: number;
}

/** Schema 18+ only. */
interface NotetypeRow {
	id: number;
	name: string;
	config: Uint8Array | null;
}
interface FieldRow {
	ntid: number;
	ord: number;
	name: string;
	config: Uint8Array | null;
}
interface TemplateRow {
	ntid: number;
	ord: number;
	name: string;
	config: Uint8Array | null;
}
interface DeckRow {
	id: number;
	name: string;
	kind: Uint8Array | null;
}

// ---------------------------------------------------------------------------
// Shared tables
// ---------------------------------------------------------------------------

export function readColRow(db: Database.Database): ColRow {
	const row = db.prepare('SELECT crt, ver, models, decks FROM col LIMIT 1').get() as
		ColRow | undefined;
	if (row === undefined) throw new Error('Not an Anki collection: the `col` table is empty.');
	return row;
}

export function readNoteRows(db: Database.Database): NoteRow[] {
	return db
		.prepare('SELECT id, guid, mid, mod, tags, flds, csum FROM notes ORDER BY id')
		.all() as NoteRow[];
}

export function readCardRows(db: Database.Database): CardRow[] {
	return db
		.prepare(
			`SELECT id, nid, did, ord, mod, type, queue, due, ivl, factor, reps, lapses, odid, odue,
			        flags, data
			 FROM cards ORDER BY id`
		)
		.all() as CardRow[];
}

export function readRevlogRows(db: Database.Database): RevlogRow[] {
	// `lastIvl` is one of the few camelCase column names in the collection; quoting keeps it
	// legible next to the snake_case-free neighbours.
	return db
		.prepare('SELECT id, cid, ease, ivl, lastIvl, factor, time, type FROM revlog ORDER BY id')
		.all() as RevlogRow[];
}

/**
 * Schema 18+ moved note types and decks into their own tables; 11 has none of them.
 *
 * All four are checked, not just `notetypes`: the table readers query every one of them, so a
 * package carrying a partial set — repacked, downgraded, hand-built — must fall back to the
 * JSON blobs rather than get halfway in and hit `no such table`.
 */
export function hasRealNoteTypeTables(db: Database.Database): boolean {
	const row = db
		.prepare(
			`SELECT count(*) AS present FROM sqlite_master
			 WHERE type = 'table' AND name IN ('notetypes', 'fields', 'templates', 'decks')`
		)
		.get() as { present: number } | undefined;
	return row?.present === 4;
}

// ---------------------------------------------------------------------------
// Note types and decks — schema 11 (JSON blobs in `col`)
// ---------------------------------------------------------------------------

/** The subset of the `col.models` JSON we map. Unknown members are ignored, not rejected. */
interface JsonModel {
	id?: number;
	name?: string;
	css?: string;
	type?: number;
	sortf?: number;
	flds?: JsonModelField[];
	tmpls?: JsonModelTemplate[];
}
interface JsonModelField {
	name?: string;
	ord?: number;
	font?: string;
	size?: number;
	rtl?: boolean;
	sticky?: boolean;
}
interface JsonModelTemplate {
	name?: string;
	ord?: number;
	qfmt?: string;
	afmt?: string;
	bqfmt?: string;
	bafmt?: string;
}
interface JsonDeck {
	id?: number;
	name?: string;
	desc?: string;
	dyn?: number;
}

export function readNoteTypesFromJson(models: string | null): IrNoteType[] {
	const parsed = parseJsonMap<JsonModel>(models, 'col.models');
	// Sorted by id so the JSON reader and the schema-18 reader (which is `ORDER BY id`) produce
	// the same IR for the same collection. Object key order is not something to rely on.
	return sortByAnkiId(
		Object.entries(parsed).map(([key, model]) => {
			// The map key is the note-type id as a string; the `id` member repeats it, but a
			// hand-edited or older collection can omit the member while the key is always present.
			const ankiId = model.id ?? Number(key);
			return {
				ankiId,
				name: model.name ?? '',
				css: model.css ?? '',
				// `type` is 0 for a standard note type and 1 for cloze.
				isCloze: model.type === 1,
				sortFieldIdx: model.sortf ?? 0,
				// Sorted by ordinal, not left in array order. `ord` is authoritative — it is what
				// `notes.flds` is indexed by and what a cloze marker names — and the schema-18 reader
				// is `ORDER BY ntid, ord`. A collection whose JSON array order disagrees with its
				// `ord` values would otherwise map every field value onto the wrong field name, and
				// the two readers would silently disagree.
				fields: sortByOrdinal(
					(model.flds ?? []).map((f, i): IrField => ({
						ordinal: f.ord ?? i,
						name: f.name ?? '',
						font: f.font ?? DEFAULT_FONT,
						size: f.size ?? DEFAULT_FONT_SIZE,
						isRtl: f.rtl === true,
						sticky: f.sticky === true
					}))
				),
				templates: sortByOrdinal(
					(model.tmpls ?? []).map((t, i): IrTemplate => ({
						ordinal: t.ord ?? i,
						name: t.name ?? '',
						qfmt: t.qfmt ?? '',
						afmt: t.afmt ?? '',
						browserQfmt: emptyToNull(t.bqfmt),
						browserAfmt: emptyToNull(t.bafmt)
					}))
				)
			};
		})
	);
}

export function readDecksFromJson(decks: string | null): IrDeck[] {
	const parsed = parseJsonMap<JsonDeck>(decks, 'col.decks');
	return sortByAnkiId(
		Object.entries(parsed).map(([key, deck]) => ({
			ankiId: deck.id ?? Number(key),
			name: normaliseDeckName(deck.name ?? ''),
			description: deck.desc ?? '',
			// `dyn` is 1 for a filtered ("dynamic") deck.
			isFiltered: deck.dyn === 1
		}))
	);
}

// ---------------------------------------------------------------------------
// Note types and decks — schema 18+ (real tables, protobuf configs)
// ---------------------------------------------------------------------------

export function readNoteTypesFromTables(db: Database.Database): IrNoteType[] {
	const notetypes = db
		.prepare('SELECT id, name, config FROM notetypes ORDER BY id')
		.all() as NotetypeRow[];
	const fieldRows = db
		.prepare('SELECT ntid, ord, name, config FROM fields ORDER BY ntid, ord')
		.all() as FieldRow[];
	const templateRows = db
		.prepare('SELECT ntid, ord, name, config FROM templates ORDER BY ntid, ord')
		.all() as TemplateRow[];

	const fieldsByNoteType = groupBy(fieldRows, (r) => r.ntid);
	const templatesByNoteType = groupBy(templateRows, (r) => r.ntid);

	return notetypes.map((nt): IrNoteType => {
		const config = nt.config === null ? undefined : decodeMessage(nt.config);
		return {
			ankiId: nt.id,
			name: nt.name,
			css: config === undefined ? '' : (pbString(config, NOTETYPE_CONFIG.css) ?? ''),
			isCloze: config !== undefined && pbVarint(config, NOTETYPE_CONFIG.kind) === 1,
			sortFieldIdx:
				config === undefined ? 0 : (pbVarint(config, NOTETYPE_CONFIG.sortFieldIdx) ?? 0),
			fields: (fieldsByNoteType.get(nt.id) ?? []).map((f): IrField => {
				const c = f.config === null ? undefined : decodeMessage(f.config);
				return {
					ordinal: f.ord,
					name: f.name,
					font:
						c === undefined ? DEFAULT_FONT : (pbString(c, FIELD_CONFIG.fontName) ?? DEFAULT_FONT),
					size:
						c === undefined
							? DEFAULT_FONT_SIZE
							: (pbVarint(c, FIELD_CONFIG.fontSize) ?? DEFAULT_FONT_SIZE),
					isRtl: c !== undefined && pbBool(c, FIELD_CONFIG.rtl) === true,
					sticky: c !== undefined && pbBool(c, FIELD_CONFIG.sticky) === true
				};
			}),
			templates: (templatesByNoteType.get(nt.id) ?? []).map((t): IrTemplate => {
				const c = t.config === null ? undefined : decodeMessage(t.config);
				return {
					ordinal: t.ord,
					name: t.name,
					qfmt: c === undefined ? '' : (pbString(c, TEMPLATE_CONFIG.qFormat) ?? ''),
					afmt: c === undefined ? '' : (pbString(c, TEMPLATE_CONFIG.aFormat) ?? ''),
					browserQfmt:
						c === undefined ? null : emptyToNull(pbString(c, TEMPLATE_CONFIG.qFormatBrowser)),
					browserAfmt:
						c === undefined ? null : emptyToNull(pbString(c, TEMPLATE_CONFIG.aFormatBrowser))
				};
			})
		};
	});
}

export function readDecksFromTables(db: Database.Database): IrDeck[] {
	const rows = db.prepare('SELECT id, name, kind FROM decks ORDER BY id').all() as DeckRow[];
	return rows.map((row): IrDeck => {
		const kind = row.kind === null ? undefined : decodeMessage(row.kind);
		// The container carries exactly one of `normal` or `filtered`; presence of the latter is
		// the whole signal. `pbBytes` rather than a decode, because an empty `normal` submessage
		// encodes as a zero-length value and must still count as present.
		const normal = kind === undefined ? undefined : pbMessageField(kind, DECK_KIND.normal);
		const isFiltered = kind !== undefined && pbBytes(kind, DECK_KIND.filtered) !== undefined;
		return {
			ankiId: row.id,
			name: normaliseDeckName(row.name),
			description: normal === undefined ? '' : (pbString(normal, DECK_NORMAL_DESCRIPTION) ?? ''),
			isFiltered
		};
	});
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Deck hierarchy separators differ between the two layouts: schema 11's JSON name uses `::`,
 * schema 18's `decks.name` column uses the unit separator. Normalising here is what lets the
 * IR carry one deck-name convention regardless of which reader produced it.
 */
function normaliseDeckName(name: string): string {
	return name.split(FIELD_SEPARATOR).join('::');
}

function parseJsonMap<T>(raw: string | null, label: string): Record<string, T> {
	if (raw === null || raw === '') return {};
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch (cause) {
		throw new Error(`${label} is not valid JSON.`, { cause });
	}
	if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
		throw new Error(`${label} is not a JSON object.`);
	}
	return parsed as Record<string, T>;
}

function emptyToNull(value: string | undefined): string | null {
	return value === undefined || value === '' ? null : value;
}

/**
 * `ankiId` can be `NaN` here: it falls back to `Number(key)` when the JSON member omits `id`,
 * and a hand-edited collection can carry a non-numeric key. `NaN` makes a subtractive
 * comparator return `NaN`, which `Array.prototype.sort` treats as "equal" — an inconsistent
 * comparator whose result is implementation-defined and, worse, unstable between runs. Sorting
 * `NaN`s to the end deterministically keeps the reader's output reproducible, which is what
 * import idempotency rests on.
 */
function sortByAnkiId<T extends { ankiId: number }>(rows: T[]): T[] {
	return rows.sort((a, b) => {
		const aIsNumber = Number.isFinite(a.ankiId);
		const bIsNumber = Number.isFinite(b.ankiId);
		if (!aIsNumber && !bIsNumber) return 0;
		if (!aIsNumber) return 1;
		if (!bIsNumber) return -1;
		return a.ankiId - b.ankiId;
	});
}

function sortByOrdinal<T extends { ordinal: number }>(rows: T[]): T[] {
	return rows.sort((a, b) => a.ordinal - b.ordinal);
}

function groupBy<T>(rows: T[], key: (row: T) => number): Map<number, T[]> {
	const out = new Map<number, T[]>();
	for (const row of rows) {
		const k = key(row);
		const bucket = out.get(k);
		if (bucket) bucket.push(row);
		else out.set(k, [row]);
	}
	return out;
}
