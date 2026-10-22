-- +goose Up
-- #219: the study queue's two sort keys, defined once. reviews.sql used to copy the new-card
-- sentinel four times and the rev.order CASE three times (sqlc has no macro mechanism across query
-- bodies), and the copies are asymmetric -- two compute the sort key, two compute the daily cap's
-- boundary for that same order -- so a partial edit served an order and a cutoff that disagreed.
--
-- Functions, not a view (contrast card_release_days, 00020): both are pure expressions over their
-- own arguments, with no subquery, so the planner inlines them into the calling query -- no
-- per-row function call. Inlining depends on: LANGUAGE sql, a single SELECT, NOT STRICT (a NULL
-- argument must reach COALESCE), no SECURITY DEFINER, no SET clause, and declared volatility no
-- stricter than the body's. Do not add any of those. internal/db/sortkeys_test.go asserts the
-- study queries' plans contain no call to either function.

-- A never-seen card's queue key (#82): Anki's persisted new-card position when the card carries
-- one, else a sentinel past every real int32 position so position-less cards sort last, tie-broken
-- by card id at every call site.
-- +goose StatementBegin
CREATE FUNCTION deckshare_new_card_key(import_due_position integer) RETURNS double precision
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT COALESCE(import_due_position, 2147483647)::double precision
$$;
-- +goose StatementEnd

-- A review-state card's key under decks.preset "rev.order" (#116): md5-of-(id, per-day seed) for
-- 'random', scheduled_days for 'intervalAsc'/'intervalDesc', otherwise due as epoch seconds.
-- STABLE, not IMMUTABLE: extract(epoch from timestamptz) is STABLE.
-- +goose StatementBegin
CREATE FUNCTION deckshare_rev_key(rev_order text, due timestamptz, scheduled_days integer, card_id uuid, seed text)
RETURNS double precision
LANGUAGE sql STABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN rev_order = 'random' THEN
            ('x' || md5(card_id::text || seed))::bit(52)::bigint::double precision
        WHEN rev_order = 'intervalAsc' THEN scheduled_days::double precision
        WHEN rev_order = 'intervalDesc' THEN (-scheduled_days)::double precision
        ELSE extract(epoch from due)
    END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION deckshare_rev_key(text, timestamptz, integer, uuid, text);
DROP FUNCTION deckshare_new_card_key(integer);
