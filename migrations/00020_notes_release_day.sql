-- +goose Up
-- #242 (part of #238): the teacher's lesson map -- which class meeting a note's content belongs
-- to. Layer 1 of three (issue #238): content -> lesson here, lesson -> class day is identity, and
-- class day -> date lives in decks.preset.calendar (no storage of its own).
--
-- GATE-ONLY. It never orders anything: new-card ordering stays entirely cards.import_due_position
-- (#82). That separation is the reason this is its own column rather than a reinterpretation of
-- import_due_position -- UpsertImportedCard's ON CONFLICT ... DO UPDATE SET import_due_position =
-- EXCLUDED... (internal/db/queries/import.sql) would silently erase a term's pacing on the next
-- re-import, and export would push coarsened tie-heavy numbers back into Anki's `due`.
--
-- 0, the default, means "no lesson assigned, available immediately" -- a permissive failure mode:
-- a note the teacher forgets to assign shows up rather than silently never unlocking. NOT NULL
-- with that default rather than a nullable column, because "unassigned" and "day 0" are the same
-- state; two representations of one state would have every reader (the gate, the counts, the
-- template, the form) carrying a branch to collapse them again.
--
-- The upper bound belongs here, not only in the handler that writes it (internal/review.
-- MaxReleaseDay, which must match): a bound enforced at one write path is a bound the next write
-- path -- an import, a fixture script -- silently doesn't have.
ALTER TABLE notes ADD COLUMN release_day integer NOT NULL DEFAULT 0
    CONSTRAINT notes_release_day_range CHECK (release_day BETWEEN 0 AND 999);

-- Which lesson each CARD belongs to, in one place. reviews.sql needs this at six sites -- the two
-- study queries, both new-card cutoff subqueries, and both queue counts -- and sqlc has no macro
-- mechanism across query bodies; reviews.sql already carries the new-card sentinel and the
-- rev_order expression four times each, which is what #219 is open about, so a seventh copied
-- expression is the thing to avoid.
--
-- A view rather than a function, deliberately. The obvious `released(card_id, class_day) boolean`
-- function cannot be inlined by the planner -- Postgres refuses to inline any SQL function whose
-- body contains a subquery, and looking a card's note up needs one -- so it stays an opaque call
-- evaluated once per row (measured at ~7.7us/row, versus ~16ns/row for the join this view
-- produces). A view is rewritten into the calling query instead, so the planner sees an ordinary
-- join and can hash it.
--
-- Keyed on the card, not the note, so the deferred cards.release_day override (issue #242's
-- "clean additive follow-up", for splitting a many-deletion cloze note across days) is a one-line
-- edit HERE -- COALESCE(c.release_day, n.release_day), both on the same class-day scale -- and
-- touches no call site at all.
CREATE VIEW card_release_days AS
SELECT c.id AS card_id, n.release_day
FROM cards c
JOIN notes n ON n.id = c.note_id;

-- +goose Down
DROP VIEW card_release_days;
ALTER TABLE notes DROP COLUMN release_day;
