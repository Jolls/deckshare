/**
 * The intermediate representation between an Anki package and our schema.
 *
 * `import: apkg -> IR -> db`, `export: db -> IR -> apkg` (CLAUDE.md §4). Nothing in here is a
 * Drizzle row, and nothing in here still carries a format quirk: this is where schema 11 and
 * schema 18+ converge, where `cards.due`'s two meanings become one discriminated union, and
 * where `cards.ivl`'s sign convention becomes a plain duration. Everything downstream reads
 * the IR, so a quirk that survives to here survives into the database.
 *
 * Cross-references inside the IR use Anki's own numeric ids, which are unique within a
 * collection. Our UUIDv7 keys are minted by the database layer; `anki_id` columns keep these
 * for export fidelity (`docs/schema.md`).
 */

/** `notetypes` / the `models` JSON blob. */
export interface IrNoteType {
	ankiId: number;
	name: string;
	css: string;
	/** Cloze note types generate one card per `{{c<n>::…}}` ordinal, not one per template. */
	isCloze: boolean;
	sortFieldIdx: number;
	fields: IrField[];
	templates: IrTemplate[];
}

export interface IrField {
	ordinal: number;
	name: string;
	font: string;
	size: number;
	isRtl: boolean;
	sticky: boolean;
}

export interface IrTemplate {
	ordinal: number;
	name: string;
	qfmt: string;
	afmt: string;
	browserQfmt: string | null;
	browserAfmt: string | null;
}

export interface IrDeck {
	ankiId: number;
	/** Components joined by `::`, normalised from schema 18's `\x1f` separator. */
	name: string;
	description: string;
	/**
	 * Filtered ("dynamic") decks are a temporary study view, not a home for cards. Their cards
	 * carry a non-zero `originalDeckAnkiId` and belong to *that* deck; we have no equivalent
	 * concept, so the importer drops filtered decks and files their cards by origin.
	 */
	isFiltered: boolean;
}

export interface IrNote {
	ankiId: number;
	/** Anki's stable per-note id, and the idempotency key for re-import (CLAUDE.md §2.2). */
	guid: string;
	noteTypeAnkiId: number;
	/**
	 * The deck this note belongs to.
	 *
	 * Anki scopes decks to *cards*, so one note's cards can legitimately sit in several decks.
	 * Our schema scopes them to notes — `notes.deck_id` with `UNIQUE (deck_id, guid)` — because
	 * that unique index is the whole idempotency guarantee (CLAUDE.md §2.2), so the reader has to
	 * pick one.
	 *
	 * **Policy: the resolved home deck of the note's lowest-numbered card.** The rule has to be
	 * stable across re-imports of the same file or the upsert stops being a no-op and starts
	 * moving notes between decks, so it cannot depend on row iteration order, insertion order, or
	 * which deck happens to hold the most cards (ties). Anki card ids are creation timestamps and
	 * never change, which makes "lowest card id" both deterministic and the closest thing the
	 * collection has to "where this note was first filed".
	 *
	 * `null` only for a note with no cards at all — an orphan the database layer has nowhere to
	 * put and should drop.
	 */
	primaryDeckAnkiId: number | null;
	/** Field values in note-type field order, split from the `\x1f`-joined `flds` column. */
	fields: string[];
	tags: string[];
	/** Anki's truncated SHA-1 of the first field. Kept for export fidelity only. */
	checksum: number;
	createdAt: Date;
	modifiedAt: Date;
}

/**
 * `cards.due` means two unrelated things depending on the card's queue, and reading it wrong
 * produces a schedule that looks entirely plausible. Hence a union rather than a number:
 * downstream code cannot use a position as if it were a date without saying so.
 */
export type IrCardDue =
	/** New card: a *queue position*, not a time. Ordering only — it has no calendar meaning. */
	| { kind: 'position'; position: number }
	/** Intraday learning / preview: a wall-clock instant, stored as epoch seconds. */
	| { kind: 'timestamp'; at: Date }
	/** Review / day-learning: whole days since `col.crt`, resolved against `crt` into `at`. */
	| { kind: 'day'; dayOffset: number; at: Date };

/** How the card is held out of the queue. Orthogonal to `state` — Anki keeps it in `queue`. */
export type IrCardHold = 'none' | 'suspended' | 'buried-scheduler' | 'buried-user';

/** Anki's `cards.type`, which lines up with `ts-fsrs` `State`. */
export type IrCardState = 'new' | 'learning' | 'review' | 'relearning';

/** FSRS memory state from `cards.data`. Absent on cards FSRS never scheduled. */
export interface IrFsrsState {
	stability: number;
	difficulty: number;
	desiredRetention: number | null;
}

export interface IrCard {
	ankiId: number;
	noteAnkiId: number;
	/** Home deck: `odid` when the card is sitting in a filtered deck, otherwise `did`. */
	deckAnkiId: number;
	/** Set only while the card is in a filtered deck; the deck it is temporarily shown in. */
	filteredDeckAnkiId: number | null;
	/** Template ordinal, or cloze ordinal for cloze note types. */
	ordinal: number;
	state: IrCardState;
	hold: IrCardHold;
	due: IrCardDue;
	/**
	 * Current interval as a duration in seconds — the normalised form of `cards.ivl`'s
	 * days-or-negative-seconds encoding. Zero for cards with no interval yet.
	 */
	intervalSeconds: number;
	reps: number;
	lapses: number;
	/**
	 * SM-2 ease × 1000. Preserved for export fidelity and **never mapped to FSRS difficulty** —
	 * they are unrelated quantities (`docs/apkg-format.md`).
	 */
	sm2Factor: number;
	/** New-card position preserved in `cards.data` after the card leaves the new queue. */
	originalPosition: number | null;
	fsrs: IrFsrsState | null;
	flag: number;
	modifiedAt: Date;
}

/** Anki's `revlog.type`, matching `review_log.review_kind`. */
export type IrReviewKind = 'learn' | 'review' | 'relearn' | 'filtered' | 'manual';

export interface IrReview {
	/** `revlog.id` — epoch milliseconds, and unique per collection. */
	ankiId: number;
	cardAnkiId: number;
	reviewedAt: Date;
	/**
	 * Anki's `ease`: 1–4 for a graded review. `manual`/reschedule rows carry 0, which is not a
	 * rating; `review_log.rating` has a 1–4 check constraint, so those rows are the database
	 * layer's to drop or translate.
	 */
	ease: number;
	/** Interval after this review, normalised out of the days-or-negative-seconds encoding. */
	intervalSeconds: number;
	/** Interval before this review, same normalisation. */
	lastIntervalSeconds: number;
	sm2Factor: number;
	/** Time the answer took. Anki caps this at its per-deck maximum. */
	durationMs: number;
	kind: IrReviewKind;
}

/** One media file, keyed by the content hash `media_blobs` uses (`docs/schema.md`). */
export interface IrMediaBlob {
	/** Lowercase hex SHA-256. Also the storage path of the bytes. */
	sha256: string;
	sizeBytes: number;
	mime: string;
	bytes: Uint8Array;
}

/** The `media_refs` row: what a filename inside a note field resolves to. */
export interface IrMediaRef {
	/** NFC-normalised, as Anki stores it. */
	filename: string;
	sha256: string;
}

/**
 * Media is **package-scoped, not deck-scoped**, and deliberately so.
 *
 * `media_refs` is keyed `(deck_id, filename)`, which raises the same question `IrNote` answers
 * for notes — except that here there is nothing to disambiguate. A package carries one flat
 * media directory shared by every deck in it, and a filename's bytes do not vary by deck, so
 * there is no ordering-dependent choice to make and no stability risk. The database layer
 * writes one `media_refs` row per (deck the import created × filename), all pointing at the
 * same blob; `media_blobs` deduplicates globally by hash, so the repetition costs a row, not a
 * copy of the file.
 */
export interface IrMedia {
	/** Deduplicated by hash — two identical images in one package are one blob. */
	blobs: IrMediaBlob[];
	refs: IrMediaRef[];
}

/** Something survivable the reader had to decide about. Never thrown — the import continues. */
export interface IrWarning {
	code: 'media-duplicate-filename' | 'media-missing-member' | 'note-without-cards';
	message: string;
}

export interface IrCollection {
	/** `col.ver`: 11 and below is the JSON-blob layout, 18 and above the real-table layout. */
	schemaVersion: number;
	/**
	 * `col.crt` as an instant. The anchor every day-offset `due` is measured from, and the
	 * reason a review card's due date cannot be computed from the card row alone.
	 */
	createdAt: Date;
	noteTypes: IrNoteType[];
	decks: IrDeck[];
	notes: IrNote[];
	cards: IrCard[];
	reviews: IrReview[];
	media: IrMedia;
	/**
	 * Non-fatal oddities found while reading. A package with a colliding media filename or a
	 * cardless note is still worth importing; the user deserves to be told, not blocked.
	 */
	warnings: IrWarning[];
}
