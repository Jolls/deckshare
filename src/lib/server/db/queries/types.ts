/**
 * The shared "database or transaction" type every query module accepts. Queries take this
 * instead of importing the `db` singleton directly, so a caller can compose several queries
 * inside one `db.transaction(...)` (e.g. note creation + card fan-out) atomically.
 */
import type { ExtractTablesWithRelations } from 'drizzle-orm';
import type { PgTransaction } from 'drizzle-orm/pg-core';
import type { PostgresJsDatabase, PostgresJsQueryResultHKT } from 'drizzle-orm/postgres-js';
import type * as schema from '../schema';

/**
 * Either the shared `db` singleton or the `tx` handed to `db.transaction(async (tx) => ...)`.
 * Query functions accept this instead of importing `db` directly, so a caller can compose
 * several queries inside one transaction (e.g. note creation + card fan-out) atomically —
 * and so a test can point them at an isolated, freshly migrated database instead.
 *
 * `ExtractTablesWithRelations` is what strips the schema module's non-table exports (the
 * `deckVisibility` / `deckRole` enums) down to the shape drizzle's transaction type expects.
 */
export type DbClient =
	| PostgresJsDatabase<typeof schema>
	| PgTransaction<
			PostgresJsQueryResultHKT,
			typeof schema,
			ExtractTablesWithRelations<typeof schema>
	  >;
