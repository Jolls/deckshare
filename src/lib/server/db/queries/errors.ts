/**
 * Query-layer errors routes are expected to map, alongside `DeckAccessError` (`access.ts`)
 * and `NoteTypeAccessError` (`note-types.ts`).
 *
 * Decks and note types are unique on `(owner_id, name)` because that is what import dedups on
 * (`docs/schema.md`), which makes "you already have one called that" an ordinary user action
 * rather than a bug — so it becomes a 409, not an unhandled 500.
 */

/** Postgres unique-violation SQLSTATE, as Drizzle surfaces it: wrapped on `.cause`. */
const UNIQUE_VIOLATION = '23505';

type Kind = 'deck' | 'note type';

/**
 * The index each kind's name uniqueness lives in. Matching on the constraint — not on the
 * SQLSTATE alone — is what keeps an unrelated unique violation raised inside the same
 * transaction from being mislabelled as a name collision and answered with a 409 the client
 * can do nothing about.
 */
const NAME_INDEX: Record<Kind, string> = {
	deck: 'decks_owner_name_idx',
	'note type': 'note_types_owner_name_idx'
};

export class DuplicateNameError extends Error {
	constructor(
		public readonly kind: Kind,
		public readonly value: string
	) {
		super(`${kind} named "${value}" already exists for this user`);
		this.name = 'DuplicateNameError';
	}
}

export async function rethrowDuplicateName<T>(
	kind: Kind,
	value: string,
	run: () => Promise<T>
): Promise<T> {
	try {
		return await run();
	} catch (err) {
		const cause = (err as { cause?: { code?: string; constraint_name?: string } })?.cause;
		if (cause?.code === UNIQUE_VIOLATION && cause.constraint_name === NAME_INDEX[kind]) {
			throw new DuplicateNameError(kind, value);
		}
		throw err;
	}
}
