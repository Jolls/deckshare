-- +goose Up
-- APPEND-ONLY. This is the optimiser's training set, not bookkeeping. No DELETE path without a
-- written decision (docs/schema.md, CLAUDE.md §2.5).
--
-- user_id is ON DELETE RESTRICT permanently: a review belongs to a live user, and this FK is what
-- blocks account deletion for anyone who has ever answered a card.
--
-- card_id deliberately has NO foreign key (#51, docs/plans/51-deletion-policy.md §0.4). RESTRICT
-- there would have made any deck that had ever been studied undeletable, which collides with
-- architecture.md §20 ("deleting a deck deletes its cards") and pressures a future change into
-- adding the one DELETE path §2.5 forbids. Decoupling is strictly stronger: no delete anywhere in
-- this schema can remove a review_log row, and none can be made to. card_id stays NOT NULL and
-- stays a sound grouping key -- UUIDv7 ids are never reused -- so history for a deleted card
-- remains replayable and remains in the optimiser's training set. This is also Anki's own shape:
-- revlog.cid is not a foreign key there either (CLAUDE.md §2.10).
CREATE TABLE review_log (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),  -- client-generated UUIDv7;
                                                    -- makes retry idempotent via
                                                    -- ON CONFLICT (id) DO NOTHING. The DB default
                                                    -- is a safety net only -- the client always
                                                    -- supplies this one explicitly. Every OTHER
                                                    -- column is computed server-side.
    user_id               uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    card_id               uuid        NOT NULL,   -- no FK, by decision -- see above
    rating                smallint    NOT NULL
        CONSTRAINT review_log_rating_check CHECK (rating BETWEEN 1 AND 4),
    reviewed_at           timestamptz NOT NULL,
    duration_ms           integer,                  -- NULL when unknown (some imported history);
                                                    -- never 0 as a stand-in for "unknown"
    state_before          smallint    NOT NULL
        CONSTRAINT review_log_state_before_check CHECK (state_before BETWEEN 0 AND 3),
    learning_steps_before smallint    NOT NULL,
    stability_before      double precision,         -- NULL for imported history
    difficulty_before     double precision,         -- NULL for imported history
    elapsed_days_before   integer     NOT NULL,
    scheduled_days_after  integer     NOT NULL,
    fsrs_version          smallint,                 -- NULL for imported history; what keeps a row
                                                    -- replayable across a refit or upgrade
    review_kind           smallint    NOT NULL
        CONSTRAINT review_log_review_kind_check CHECK (review_kind BETWEEN 0 AND 4),
    anki_id               bigint,                   -- revlog.id; NULL for rows our reviewer produced

    -- Re-import dedup. Plain UNIQUE is correct: Postgres treats NULLs as distinct by default,
    -- which is exactly what's wanted -- rows the live reviewer writes have anki_id NULL and so
    -- collide on nothing. Do NOT "fix" this to NULLS NOT DISTINCT. user_id leads because one
    -- user's imported history must never block another's.
    CONSTRAINT review_log_user_id_card_id_anki_id_key UNIQUE (user_id, card_id, anki_id)
);

-- The per-card replay path. No RI check to serve any more (card_id has no FK), but the ordering
-- is what replayReviews reads (architecture.md §6).
CREATE INDEX review_log_card_id_user_id_reviewed_at_idx
    ON review_log (card_id, user_id, reviewed_at);

CREATE INDEX review_log_user_id_reviewed_at_idx ON review_log (user_id, reviewed_at);

-- +goose Down
DROP TABLE review_log;
