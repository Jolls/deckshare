/**
 * `.apkg` / `.colpkg` -> IR.
 *
 * The package is a zip holding a SQLite collection, a `media` index, and the media files
 * themselves named by index. This module unwraps that, reads the collection through whichever
 * of the two schemas it uses, and normalises the format's encodings away so that nothing
 * downstream has to know about them (CLAUDE.md §4: `apkg -> IR -> db`, never Drizzle rows).
 *
 * The encodings that silently produce plausible-but-wrong schedules — `cards.due`'s three
 * meanings, `cards.odue` shadowing `due` for displaced cards, and `cards.ivl`'s sign
 * convention — are handled in `normaliseDue` and `intervalSeconds` below, and nowhere else.
 *
 * **The input is untrusted.** Shared decks mean a package is other users' bytes (CLAUDE.md §8),
 * so the archive is read under explicit size ceilings rather than trusted to be well behaved.
 *
 * No Anki-derived code (CLAUDE.md §2.7): the format facts come from `docs/apkg-format.md` and
 * public format descriptions.
 */
import { readFileSync } from 'node:fs';
import Database from 'better-sqlite3';
import { unzipSync } from 'fflate';
import { decompress as zstdDecompress } from 'zstd-napi';
import {
	CARD_QUEUE,
	CARD_TYPE,
	FIELD_SEPARATOR,
	REVLOG_TYPE,
	readCardRows,
	readColRow,
	readDecksFromJson,
	readDecksFromTables,
	readNoteRows,
	readNoteTypesFromJson,
	readNoteTypesFromTables,
	readRevlogRows,
	hasRealNoteTypeTables,
	type CardRow,
	type RevlogRow
} from './anki-schema';
import type {
	IrCard,
	IrCardDue,
	IrCardHold,
	IrCardState,
	IrCollection,
	IrFsrsState,
	IrMedia,
	IrNote,
	IrReview,
	IrReviewKind,
	IrWarning
} from './ir';
import { buildMedia, parseMediaIndex } from './media';

/**
 * Collection member names, **newest first, and the order is load-bearing.**
 *
 * A package can carry more than one so that older Anki versions still find something they can
 * read. The others are not equivalent copies: a real 2025 shared-deck export inspected during
 * verification carried 319 notes in `collection.anki21` and a **one-note** "please upgrade"
 * placeholder in `collection.anki2`. Reading the wrong member imports one note out of hundreds
 * and reports success. Regression test: `buildDowngradeStubPackage()`.
 */
const COLLECTION_MEMBERS = ['collection.anki21b', 'collection.anki21', 'collection.anki2'];

const MEDIA_INDEX_MEMBER = 'media';

/** Zstandard frame magic, little-endian `0xFD2FB528`. */
const ZSTD_MAGIC = [0x28, 0xb5, 0x2f, 0xfd];

const SECONDS_PER_DAY = 86_400;

// ---------------------------------------------------------------------------
// Ceilings on an untrusted archive
// ---------------------------------------------------------------------------

/**
 * A zip or zstd bomb is a few kilobytes that expand to gigabytes, and both `unzipSync` and
 * `zstdDecompress` are eager, in-memory, and uninterruptible once entered. Without ceilings a
 * single uploaded file OOMs the server for everyone (CLAUDE.md §8: shared decks make packages
 * other users' input).
 *
 * The numbers are chosen to sit far above any real collection — a large shared deck with media
 * runs to a few hundred megabytes — and far below what would exhaust a server.
 */
export interface ArchiveLimits {
	/** Members in the zip, however small. Many tiny entries is the cheapest bomb to build. */
	maxMembers: number;
	/** Decompressed bytes for any one member, at the zip layer and again at the zstd layer. */
	maxMemberBytes: number;
	/** Decompressed bytes across the whole archive. */
	maxTotalBytes: number;
}

export const DEFAULT_ARCHIVE_LIMITS: ArchiveLimits = {
	maxMembers: 100_000,
	maxMemberBytes: 512 * 1024 * 1024,
	maxTotalBytes: 2 * 1024 * 1024 * 1024
};

/**
 * Above this, a `cards.due` value can only be epoch seconds (2001-09-09); below it, it can
 * only be a day offset or a queue position. Used solely for the one genuinely ambiguous case —
 * see `normaliseDue`.
 */
const EPOCH_SECONDS_THRESHOLD = 1_000_000_000;

export function readApkgFile(path: string, limits?: Partial<ArchiveLimits>): IrCollection {
	return readApkg(readFileSync(path), limits);
}

export function readApkg(
	packageBytes: Uint8Array,
	limits: Partial<ArchiveLimits> = {}
): IrCollection {
	const resolved: ArchiveLimits = { ...DEFAULT_ARCHIVE_LIMITS, ...limits };

	// `unzipSync` needs a plain Uint8Array view; a Node Buffer is one, so this is a no-op there.
	const members = unzipArchive(
		packageBytes instanceof Uint8Array ? packageBytes : new Uint8Array(packageBytes),
		resolved
	);

	const collectionBytes = findCollection(members, resolved);
	const db = new Database(Buffer.from(collectionBytes), { readonly: true });
	try {
		return readCollection(db, members, resolved);
	} finally {
		db.close();
	}
}

/**
 * Extract the archive under the ceilings above.
 *
 * The declared sizes checked in the filter come from the zip's own headers and a hostile
 * archive is free to understate them, so the extracted lengths are re-checked afterwards. The
 * filter is still worth having: it is the only chance to refuse a member *before* `fflate`
 * allocates its output.
 */
function unzipArchive(packageBytes: Uint8Array, limits: ArchiveLimits): Record<string, Uint8Array> {
	let memberCount = 0;
	let declaredTotal = 0;

	const members = unzipSync(packageBytes, {
		filter: (file) => {
			memberCount += 1;
			if (memberCount > limits.maxMembers) {
				throw new Error(
					`Refusing to read the package: more than ${limits.maxMembers} archive members.`
				);
			}
			if (file.originalSize > limits.maxMemberBytes) {
				throw new Error(
					`Refusing to read the package: member "${file.name}" declares ` +
						`${file.originalSize} bytes, over the ${limits.maxMemberBytes}-byte limit.`
				);
			}
			declaredTotal += file.originalSize;
			if (declaredTotal > limits.maxTotalBytes) {
				throw new Error(
					`Refusing to read the package: declared contents exceed the ` +
						`${limits.maxTotalBytes}-byte limit.`
				);
			}
			return true;
		}
	});

	let actualTotal = 0;
	for (const [name, bytes] of Object.entries(members)) {
		if (bytes.length > limits.maxMemberBytes) {
			throw new Error(
				`Refusing to read the package: member "${name}" expanded to ${bytes.length} bytes, ` +
					`over the ${limits.maxMemberBytes}-byte limit.`
			);
		}
		actualTotal += bytes.length;
		if (actualTotal > limits.maxTotalBytes) {
			throw new Error(
				`Refusing to read the package: contents expanded past the ` +
					`${limits.maxTotalBytes}-byte limit.`
			);
		}
	}

	return members;
}

function findCollection(members: Record<string, Uint8Array>, limits: ArchiveLimits): Uint8Array {
	for (const name of COLLECTION_MEMBERS) {
		const bytes = members[name];
		if (bytes !== undefined) return maybeDecompress(bytes, name, limits);
	}
	throw new Error(
		`Not an Anki package: no ${COLLECTION_MEMBERS.join(', ')} member in the archive.`
	);
}

/**
 * Zstd-decompress a member if it is a zstd frame.
 *
 * **Only ever called for the collection and the media index.** Those are the two members the
 * modern package format compresses; media files are stored as-is. Sniffing every member would
 * be worse than useless — a legitimate media file whose first four bytes happen to be the zstd
 * magic would be mangled or rejected, and media is exactly the member type whose bytes are
 * arbitrary.
 *
 * The sniff is on the frame magic rather than on the package version, because `meta` may be
 * absent and the format doc is explicitly unverified (CLAUDE.md §7); deriving the container
 * shape from the bytes cannot be wrong about what a version number means.
 */
function maybeDecompress(bytes: Uint8Array, memberName: string, limits: ArchiveLimits): Uint8Array {
	const isZstd = ZSTD_MAGIC.every((byte, i) => bytes[i] === byte);
	if (!isZstd) return bytes;

	// Checked before decompressing, not after: `zstdDecompress` allocates the whole output in one
	// uninterruptible native call, so a post-hoc length check would already have lost.
	const declared = zstdContentSize(bytes);
	if (declared !== null && declared > limits.maxMemberBytes) {
		throw new Error(
			`Refusing to read the package: member "${memberName}" declares ${declared} ` +
				`decompressed bytes, over the ${limits.maxMemberBytes}-byte limit.`
		);
	}

	// `zstd-napi` hands back a Buffer. Returning a plain `Uint8Array` view of it keeps the IR's
	// media bytes one type regardless of whether the package was compressed.
	const out = zstdDecompress(Buffer.from(bytes));
	if (out.length > limits.maxMemberBytes) {
		throw new Error(
			`Refusing to read the package: member "${memberName}" expanded to ${out.length} bytes, ` +
				`over the ${limits.maxMemberBytes}-byte limit.`
		);
	}
	return new Uint8Array(out.buffer, out.byteOffset, out.byteLength);
}

/**
 * Read `Frame_Content_Size` out of a zstd frame header (RFC 8878 §3.1.1), or `null` when the
 * frame does not declare one — it is optional, and a streaming writer omits it.
 *
 * Deliberately total: anything unexpected returns `null` (unknown) rather than throwing, so a
 * frame shape we misread can only cost us the early rejection, never a false one.
 */
function zstdContentSize(bytes: Uint8Array): number | null {
	const descriptor = bytes[4];
	if (descriptor === undefined) return null;

	const fcsFlag = descriptor >> 6;
	const singleSegment = (descriptor & 0b0010_0000) !== 0;
	const dictIdFlag = descriptor & 0b11;

	// Field widths, both indexed by their flag. An FCS flag of 0 means one byte when the frame is
	// single-segment and no field at all otherwise.
	const fcsSize = [singleSegment ? 1 : 0, 2, 4, 8][fcsFlag] ?? 0;
	const dictIdSize = [0, 1, 2, 4][dictIdFlag] ?? 0;
	if (fcsSize === 0) return null;

	// Magic (4) + descriptor (1) + window descriptor (absent when single-segment) + dictionary id.
	const start = 4 + 1 + (singleSegment ? 0 : 1) + dictIdSize;
	if (start + fcsSize > bytes.length) return null;

	let value = 0n;
	for (let i = fcsSize - 1; i >= 0; i -= 1) {
		const byte = bytes[start + i];
		if (byte === undefined) return null;
		value = (value << 8n) | BigInt(byte);
	}
	// The two-byte form is stored with 256 subtracted.
	if (fcsSize === 2) value += 256n;
	return Number(value);
}

function readCollection(
	db: Database.Database,
	members: Record<string, Uint8Array>,
	limits: ArchiveLimits
): IrCollection {
	const col = readColRow(db);
	const warnings: IrWarning[] = [];

	// Table presence decides, `col.ver` does not. A repacked or downgraded package can claim
	// version 18 while carrying the JSON blobs, and trusting the number over the tables turns
	// that into `no such table` instead of a clean read. The reverse case — real tables under a
	// version below 18 — is covered by the same rule.
	const useTables = hasRealNoteTypeTables(db);

	const cards = readCards(db, col.crt);

	return {
		schemaVersion: col.ver,
		createdAt: new Date(col.crt * 1000),
		noteTypes: useTables ? readNoteTypesFromTables(db) : readNoteTypesFromJson(col.models),
		decks: useTables ? readDecksFromTables(db) : readDecksFromJson(col.decks),
		notes: readNotes(db, cards, warnings),
		cards,
		reviews: readReviews(db),
		media: readMedia(members, warnings, limits),
		warnings
	};
}

// ---------------------------------------------------------------------------
// Notes
// ---------------------------------------------------------------------------

function readNotes(
	db: Database.Database,
	cards: readonly IrCard[],
	warnings: IrWarning[]
): IrNote[] {
	const primaryDecks = resolvePrimaryDecks(cards);

	return readNoteRows(db).map((row) => {
		const primaryDeckAnkiId = primaryDecks.get(row.id) ?? null;
		if (primaryDeckAnkiId === null) {
			warnings.push({
				code: 'note-without-cards',
				message: `Note ${row.guid} has no cards and no deck to file it under; it will be skipped.`
			});
		}
		return {
			ankiId: row.id,
			// The idempotency key for re-import. `notes` is `UNIQUE (deck_id, guid)`, so the database
			// layer upserts on it rather than inserting; the reader's job is only to carry it through
			// unchanged, which is also why this function is deterministic — reading the same package
			// twice must yield byte-identical notes for that upsert to be a no-op (CLAUDE.md §7).
			guid: row.guid,
			noteTypeAnkiId: row.mid,
			primaryDeckAnkiId,
			// Empty trailing fields are meaningful, so the split is unconditional — a note type with
			// five fields always yields five entries even when the last three are blank.
			fields: row.flds.split(FIELD_SEPARATOR),
			// Stored space-separated *and* space-surrounded, so a plain split leaves empty entries.
			tags: row.tags.split(/\s+/).filter((tag) => tag !== ''),
			checksum: row.csum,
			// `notes.id` is epoch milliseconds; `notes.mod` is epoch seconds. Mixing them up shifts
			// every imported note's timestamps by three orders of magnitude.
			createdAt: new Date(row.id),
			modifiedAt: new Date(row.mod * 1000)
		};
	});
}

/**
 * Note id -> the deck of its lowest-numbered card. See `IrNote.primaryDeckAnkiId` for why that
 * rule and not another; the short version is that it is the only candidate that cannot move a
 * note between decks on re-import of the same file.
 */
function resolvePrimaryDecks(cards: readonly IrCard[]): Map<number, number> {
	const lowestCardId = new Map<number, number>();
	const deckByNote = new Map<number, number>();

	for (const card of cards) {
		const current = lowestCardId.get(card.noteAnkiId);
		if (current === undefined || card.ankiId < current) {
			lowestCardId.set(card.noteAnkiId, card.ankiId);
			deckByNote.set(card.noteAnkiId, card.deckAnkiId);
		}
	}
	return deckByNote;
}

// ---------------------------------------------------------------------------
// Cards — the traps live here
// ---------------------------------------------------------------------------

function readCards(db: Database.Database, crtSeconds: number): IrCard[] {
	return readCardRows(db).map((row) => {
		const data = readCardData(row.data);
		return {
			ankiId: row.id,
			noteAnkiId: row.nid,
			// A non-zero `odid` means the card is temporarily sitting in a filtered deck: `did` is
			// that filtered deck and `odid` is the card's real home. We have no filtered decks, so
			// the home deck is the one that matters.
			deckAnkiId: row.odid !== 0 ? row.odid : row.did,
			filteredDeckAnkiId: row.odid !== 0 ? row.did : null,
			ordinal: row.ord,
			state: cardState(row.type),
			hold: cardHold(row.queue),
			due: normaliseDue(row, crtSeconds),
			intervalSeconds: intervalSeconds(row.ivl),
			reps: row.reps,
			lapses: row.lapses,
			sm2Factor: row.factor,
			originalPosition: data.position,
			fsrs: data.fsrs,
			// Only the low three bits are the flag colour; the rest of the column is unrelated.
			flag: row.flags & 0b111,
			modifiedAt: new Date(row.mod * 1000)
		};
	});
}

/**
 * **Trap 1 — `cards.due` means three unrelated things, and `cards.odue` can shadow it
 * entirely** (`docs/apkg-format.md`).
 *
 * - New cards: `due` is a **queue position**, a small ordering integer with no calendar
 *   meaning. A card with `due = 3` is the fourth new card, not a card due three days after the
 *   collection was created.
 * - Review and day-learning cards: `due` is **whole days since `col.crt`**, so it cannot be
 *   resolved into an instant without `crt`. With `crt = 2024-01-01T04:00Z`, `due = 3` is
 *   2024-01-04T04:00Z.
 * - Intraday learning and preview cards: `due` is an **epoch-seconds timestamp**, because a
 *   learning step is minutes long and a day offset cannot express it.
 *
 * `queue`, not `type`, is what distinguishes these, and that matters: a card can be `type =
 * review` while sitting in `queue = dayLearning` mid-relearn. The union this returns is the
 * point — the position case cannot be handed to a `Date` constructor by accident.
 *
 * **`odue` shadows `due` whenever `odid` is non-zero.** Moving a card into a filtered deck
 * overwrites `due` with the filtered deck's own ordering value and stashes the card's real due
 * value in `odue`. Reading `due` for such a card imports a schedule that was never the card's,
 * which is the same class of silent wrongness as the two unit traps — and it survives the
 * filtered deck being deleted, because these cards keep `odid` until Anki next rebuilds them.
 * How the value is *interpreted* is unchanged; only which column it comes from.
 *
 * Suspended and buried cards are the one ambiguous case: their `queue` is negative and has
 * overwritten the queue they came from, so only the magnitude can separate a day offset from a
 * timestamp. Day offsets are counted from the collection's creation and stay in the thousands;
 * epoch seconds have been ten digits since 2001.
 */
function normaliseDue(row: CardRow, crtSeconds: number): IrCardDue {
	const value = row.odid !== 0 ? row.odue : row.due;

	switch (row.queue) {
		case CARD_QUEUE.new:
			return { kind: 'position', position: value };
		case CARD_QUEUE.learning:
		case CARD_QUEUE.preview:
			return { kind: 'timestamp', at: new Date(value * 1000) };
		case CARD_QUEUE.review:
		case CARD_QUEUE.dayLearning:
			return dayDue(value, crtSeconds);
		default: {
			// Suspended (-1) or buried (-2, -3), or an unrecognised queue from a future version.
			if (row.type === CARD_TYPE.new) return { kind: 'position', position: value };
			if (value >= EPOCH_SECONDS_THRESHOLD) {
				return { kind: 'timestamp', at: new Date(value * 1000) };
			}
			return dayDue(value, crtSeconds);
		}
	}
}

function dayDue(dayOffset: number, crtSeconds: number): IrCardDue {
	return {
		kind: 'day',
		dayOffset,
		at: new Date((crtSeconds + dayOffset * SECONDS_PER_DAY) * 1000)
	};
}

/**
 * **Trap 2 — `cards.ivl` (and `revlog.ivl` / `revlog.lastIvl`) changes unit with its sign**
 * (`docs/apkg-format.md`).
 *
 * A positive value is **days**; a negative value is **seconds**, which is how a sub-day
 * learning step is represented. `ivl = 21` is three weeks; `ivl = -600` is ten minutes. Read
 * naively, that ten-minute step becomes a six-hundred-day interval with the sign attached, and
 * nothing downstream would notice.
 *
 * The IR carries seconds throughout so the distinction cannot come back.
 */
function intervalSeconds(ivl: number): number {
	return ivl < 0 ? -ivl : ivl * SECONDS_PER_DAY;
}

function cardState(type: number): IrCardState {
	switch (type) {
		case CARD_TYPE.new:
			return 'new';
		case CARD_TYPE.learning:
			return 'learning';
		case CARD_TYPE.review:
			return 'review';
		case CARD_TYPE.relearning:
			return 'relearning';
		default:
			throw new Error(`Unrecognised cards.type ${type}.`);
	}
}

/**
 * Suspension and burial live in `queue` as negatives and are a separate concern from `type`,
 * which keeps describing the card's scheduling state underneath the hold.
 */
function cardHold(queue: number): IrCardHold {
	switch (queue) {
		case CARD_QUEUE.suspended:
			return 'suspended';
		case CARD_QUEUE.schedulerBuried:
			return 'buried-scheduler';
		case CARD_QUEUE.userBuried:
			return 'buried-user';
		default:
			return 'none';
	}
}

/**
 * `cards.data` carries the FSRS memory state on collections Anki scheduled with FSRS. Cards
 * without it were never scheduled by FSRS and have to be seeded — preferably by replaying
 * `revlog` through `ts-fsrs` rather than from defaults (`docs/apkg-format.md`).
 *
 * `cards.factor` is deliberately not consulted: it is SM-2 ease × 1000 and has no relationship
 * to FSRS difficulty. Mapping one onto the other is the kind of plausible-looking wrong that
 * this module exists to prevent.
 *
 * Every value is type-checked rather than cast. SQLite is dynamically typed and this column is
 * free-form JSON from an untrusted package, so `"dr": "0.9"` or `"dr": null` is reachable; a
 * string reaching `desiredRetention` would flow into a `double precision` column and a
 * scheduler parameter. Absent beats mistyped — the seeding path handles a null, and it has no
 * defence against a plausible-looking wrong number.
 *
 * The key names are short (`s`, `d`, `dr`) and, like the rest of `docs/apkg-format.md`,
 * unverified against a real build. Anything unparseable is treated as absent rather than
 * fatal: a malformed `data` blob must not make an otherwise-good collection unimportable.
 */
function readCardData(raw: string | null): { position: number | null; fsrs: IrFsrsState | null } {
	const absent = { position: null, fsrs: null };
	if (raw === null || raw === '' || raw === '{}') return absent;

	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return absent;
	}
	if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return absent;

	const data = parsed as Record<string, unknown>;
	const stability = finiteNumber(data.s);
	const difficulty = finiteNumber(data.d);

	return {
		position: finiteNumber(data.pos),
		fsrs:
			stability === null || difficulty === null
				? null
				: { stability, difficulty, desiredRetention: finiteNumber(data.dr) }
	};
}

/** `NaN` and `Infinity` survive `typeof === 'number'` and both poison a scheduler parameter. */
function finiteNumber(value: unknown): number | null {
	return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

// ---------------------------------------------------------------------------
// Review history
// ---------------------------------------------------------------------------

/**
 * `revlog` becomes `review_log`, and that is the difference between a cold start and a warm
 * one: it is the user's training data, and the optimiser fits against it (CLAUDE.md §2.5).
 */
function readReviews(db: Database.Database): IrReview[] {
	return readRevlogRows(db).map((row) => ({
		ankiId: row.id,
		cardAnkiId: row.cid,
		// `revlog.id` is the review time in epoch milliseconds, and doubles as the primary key.
		reviewedAt: new Date(row.id),
		ease: row.ease,
		// Trap 2 again: both interval columns use the days-or-negative-seconds encoding.
		intervalSeconds: intervalSeconds(row.ivl),
		lastIntervalSeconds: intervalSeconds(row.lastIvl),
		sm2Factor: row.factor,
		durationMs: row.time,
		kind: reviewKind(row)
	}));
}

function reviewKind(row: RevlogRow): IrReviewKind {
	switch (row.type) {
		case REVLOG_TYPE.learn:
			return 'learn';
		case REVLOG_TYPE.review:
			return 'review';
		case REVLOG_TYPE.relearn:
			return 'relearn';
		case REVLOG_TYPE.filtered:
			return 'filtered';
		case REVLOG_TYPE.manual:
			return 'manual';
		default:
			throw new Error(`Unrecognised revlog.type ${row.type} on review ${row.id}.`);
	}
}

// ---------------------------------------------------------------------------
// Media
// ---------------------------------------------------------------------------

function readMedia(
	members: Record<string, Uint8Array>,
	warnings: IrWarning[],
	limits: ArchiveLimits
): IrMedia {
	const index = members[MEDIA_INDEX_MEMBER];
	if (index === undefined) return { blobs: [], refs: [] };

	const filenamesByMember = parseMediaIndex(maybeDecompress(index, MEDIA_INDEX_MEMBER, limits));
	const files: { filename: string; bytes: Uint8Array }[] = [];

	for (const [member, filename] of filenamesByMember) {
		const bytes = members[member];
		// A package can list a file it does not carry — `.apkg` exports of a deck selection
		// occasionally do. Skipping is right: the ref would dangle, and `media_refs.sha256` is a
		// NOT NULL foreign key. Media members are stored uncompressed, so no zstd sniff here.
		if (bytes === undefined) {
			warnings.push({
				code: 'media-missing-member',
				message: `The package indexes "${filename}" as member ${member} but does not contain it.`
			});
			continue;
		}
		files.push({ filename, bytes });
	}

	return buildMedia(files, warnings);
}
