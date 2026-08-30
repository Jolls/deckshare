-- The reviewer's queue (architecture.md §6). Every query here takes user_id and joins deck_access
-- (CLAUDE.md §9) -- review routes require can_view AND can_study.

-- The day boundary (docs/schema.md): a per-user rollover hour in the user's own timezone, not
-- midnight UTC. The arithmetic runs on the LOCAL wall clock, so a DST transition makes the study day
-- 23 or 25 hours long instead of silently shifting the rollover. `now` is a parameter, not now(), so
-- handler tests can pin the clock.
-- name: GetStudyDayWindow :one
WITH l AS (
    SELECT u.timezone AS tz,
           u.day_start_hour::int AS h,
           (sqlc.arg(now)::timestamptz AT TIME ZONE u.timezone) AS local_now
    FROM users u
    WHERE u.id = sqlc.arg(user_id)
), s AS (
    SELECT tz,
           date_trunc('day', local_now - make_interval(hours => h)) + make_interval(hours => h) AS start_local
    FROM l
)
SELECT (start_local AT TIME ZONE tz)::timestamptz                        AS study_day_start,
       ((start_local + interval '1 day') AT TIME ZONE tz)::timestamptz   AS study_day_end
FROM s;

-- name: GetDeckForStudy :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE d.id = sqlc.arg(deck_id);

-- The queue, configurable per deck (#116, #118): decks.preset "rev.order" picks the review-state
-- sort key ('due' -- the original and Anki's classic default -- 'random', 'intervalAsc',
-- 'intervalDesc'), "priority" picks whether never-seen cards sort as a group before or after
-- everything else ('due' -- the original default -- or 'new'; 'mixed' bypasses this query
-- entirely, see ListReviewCardsForStudy/ListNewCardsForStudy below).
--
-- `scored` computes each row's raw_key once: a never-seen row sorts by its persisted Anki
-- new-card queue position when the import carried one, else after every real position (#82; the
-- rest of Anki's gather-mode config -- random/other-field ordering, deck-tree limits -- stays out
-- of scope, #117), otherwise the rev_order-selected expression. Recomputing it a second time per
-- row (once for the CASE, once for a plain column reference) would risk the two copies drifting;
-- a CTE lets the outer query reference it as an ordinary column instead.
--
-- The outer query also computes group_bit -- 0/1 depending on priority -- so the two groups
-- (never-seen vs. everything else) always sort as a whole ahead of/behind each other. group_bit
-- is its own ORDER BY/cursor column rather than an offset folded into sort_key by arithmetic: see
-- the doc comment on group_bit's SELECT expression below for why. Keyset cursor and ORDER BY both
-- key on (group_bit, sort_key, card_id).
-- name: ListDueCardsForStudy :many
WITH scored AS (
    SELECT c.id                                        AS card_id,
           c.ordinal                                   AS card_ordinal,
           (ucs.user_id IS NULL)::boolean               AS unseen,
           COALESCE(ucs.due, now())                    AS due,
           COALESCE(ucs.stability, 0)::double precision  AS stability,
           COALESCE(ucs.difficulty, 0)::double precision AS difficulty,
           COALESCE(ucs.state, 0)::smallint            AS state,
           COALESCE(ucs.reps, 0)::int                  AS reps,
           COALESCE(ucs.lapses, 0)::int                AS lapses,
           COALESCE(ucs.scheduled_days, 0)::int        AS scheduled_days,
           COALESCE(ucs.learning_steps, 0)::smallint   AS learning_steps,
           ucs.last_review                             AS last_review,
           n.fields                                    AS note_fields,
           n.tags                                      AS note_tags,
           nt.id                                       AS note_type_id,
           nt.name                                     AS note_type_name,
           nt.is_cloze                                 AS is_cloze,
           t.name                                      AS template_name,
           t.qfmt                                      AS qfmt,
           t.afmt                                      AS afmt,
           COALESCE(ucs.suspended, false)               AS suspended,
           ucs.buried_until                            AS buried_until,
           CASE
               -- New-card gather order (#82): Anki's persisted new-card queue position when the
               -- import carried one, else the sentinel below -- pushes position-less cards (never
               -- imported, manually created) after every real position, falling back to the
               -- card_id tie-break among themselves (unchanged behavior for that subset). The
               -- 2147483647 (int32 max) sentinel is repeated, not shared -- sqlc has no macro
               -- mechanism across query bodies -- at 3 other sites in this file (search for it);
               -- change all 4 together or the new-card sort order and its own cap boundary desync.
               WHEN ucs.user_id IS NULL THEN COALESCE(c.import_due_position, 2147483647)::double precision
               WHEN sqlc.arg(rev_order)::text = 'random' THEN
                   ('x' || md5(c.id::text || sqlc.arg(hash_seed)::text))
                   ::bit(52)::bigint::double precision
               WHEN sqlc.arg(rev_order)::text = 'intervalAsc' THEN ucs.scheduled_days::double precision
               WHEN sqlc.arg(rev_order)::text = 'intervalDesc' THEN (-ucs.scheduled_days)::double precision
               ELSE extract(epoch from ucs.due)
           END                                          AS raw_key
    FROM cards c
    JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                       AND da.can_view AND da.can_study
    JOIN notes n       ON n.id = c.note_id
    JOIN note_types nt ON nt.id = n.note_type_id
    JOIN templates t   ON t.id = c.template_id
    LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
    WHERE c.deck_id = sqlc.arg(deck_id)
)
SELECT scored.card_id, scored.card_ordinal, scored.unseen, scored.due, scored.stability,
       scored.difficulty, scored.state, scored.reps, scored.lapses, scored.scheduled_days,
       scored.learning_steps, scored.last_review, scored.note_fields, scored.note_tags,
       scored.note_type_id, scored.note_type_name, scored.is_cloze, scored.template_name,
       scored.qfmt, scored.afmt,
       -- group_bit is its own ORDER BY/cursor column, not folded into sort_key by arithmetic --
       -- float8 has ~15-17 significant decimal digits total, so "add a big constant to select
       -- the group" (an earlier version of this query) silently loses raw_key's low digits once
       -- the constant is large enough to dominate, corrupting the ordering within the offset
       -- group. priority='new' puts never-seen first (group_bit 0), reviews second (group_bit 1);
       -- priority='due' (the default) is the reverse. Whichever group sorts first is also the one
       -- ORDER BY ... LIMIT batch_size fills first when the combined daily total (rev_remaining
       -- below) binds tighter than batch_size -- the caller (BuildBatch, #118) is responsible for
       -- passing a batch_size already clamped to that remaining total, which is what turns this
       -- ordering into "fill the priority side, backfill the rest from the other side" rather
       -- than just display order.
       (CASE WHEN sqlc.arg(priority)::text = 'new' THEN (NOT scored.unseen)::int ELSE scored.unseen::int END)
           AS group_bit,
       scored.raw_key::double precision AS sort_key
FROM scored
-- The cutoff for the deck's combined new+due daily total (#118, rev_remaining -- formerly an
-- independent due-only cap, #115): the (raw_key, id) of the review-state card ranked last within
-- that remaining total, in the same rev_order this query serves review-state cards in. LATERAL +
-- "ON true" makes it an uncorrelated one-row subquery Postgres evaluates once per fetch, same
-- InitPlan shape as the new-card cutoff below; a LEFT JOIN so a deck with fewer review-state cards
-- than the allowance still returns a row (cutoff_key NULL).
LEFT JOIN LATERAL (
    SELECT (CASE
                WHEN sqlc.arg(rev_order)::text = 'random' THEN
                    ('x' || md5(c2.id::text || sqlc.arg(hash_seed)::text))
                    ::bit(52)::bigint::double precision
                WHEN sqlc.arg(rev_order)::text = 'intervalAsc' THEN u2.scheduled_days::double precision
                WHEN sqlc.arg(rev_order)::text = 'intervalDesc' THEN (-u2.scheduled_days)::double precision
                ELSE extract(epoch from u2.due)
            END) AS cutoff_key,
           c2.id AS cutoff_id
    FROM cards c2
    JOIN user_card_state u2 ON u2.user_id = sqlc.arg(user_id) AND u2.card_id = c2.id
    WHERE c2.deck_id = sqlc.arg(deck_id) AND u2.state = 2
      AND NOT u2.suspended
      AND (u2.buried_until IS NULL OR u2.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
      AND u2.due <= sqlc.arg(now)::timestamptz + make_interval(mins => sqlc.arg(look_ahead_minutes)::int)
      AND (u2.last_review IS NULL OR u2.last_review < sqlc.arg(study_day_start)::timestamptz)
    ORDER BY cutoff_key, c2.id
    OFFSET GREATEST(sqlc.arg(rev_remaining)::int - 1, 0)
    LIMIT 1
) rev_cutoff ON true
-- The new-card cutoff for the per-deck daily new-card cap (#101): the (position, id) of the card
-- ranked last within the deck's remaining new-card allowance, in the same (import_due_position,
-- id) gather order raw_key uses for never-seen rows (#82) -- so the cap boundary agrees with the
-- order it's capping, same InitPlan/LEFT JOIN shape as rev_cutoff above. Sentinel: keep in sync
-- with the scored CTE's raw_key CASE above (see its doc comment).
LEFT JOIN LATERAL (
    SELECT COALESCE(c2.import_due_position, 2147483647)::double precision AS cutoff_key,
           c2.id AS cutoff_id
    FROM cards c2
    LEFT JOIN user_card_state u2
           ON u2.user_id = sqlc.arg(user_id) AND u2.card_id = c2.id
    WHERE c2.deck_id = sqlc.arg(deck_id) AND u2.user_id IS NULL
    ORDER BY cutoff_key, c2.id
    OFFSET GREATEST(sqlc.arg(new_remaining)::int - 1, 0)
    LIMIT 1
) new_cutoff ON true
WHERE NOT scored.suspended
  AND (scored.buried_until IS NULL OR scored.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
  -- look_ahead_minutes (#154, decks.preset "due.lookAheadMinutes", default 0) widens the due
  -- cutoff past "now" by an explicit opt-in amount; the rev_cutoff subquery above must widen it
  -- identically, or the daily review cap's boundary would be computed over a different-sized
  -- eligible set than this filter admits.
  AND (scored.unseen OR scored.due <= sqlc.arg(now)::timestamptz + make_interval(mins => sqlc.arg(look_ahead_minutes)::int))
  AND (scored.last_review IS NULL OR scored.last_review < sqlc.arg(study_day_start)::timestamptz)
  -- The per-deck daily new-card cap (#101). new_remaining is the deck's configured limit minus what
  -- has already been introduced today; the caller computes it. Capping by POSITION (raw_key/id),
  -- not by how many rows this fetch returns, is what makes the cap hold across refills: a card
  -- introduced earlier today has a user_card_state row and has left this set, so the ranking
  -- restarts at 1, while a card already served this session but not yet graded is still in it and
  -- still occupies its rank. cutoff_key NULL means fewer never-seen cards exist than the
  -- allowance -- all of them pass. Suspended/buried are not re-checked here because a card with no
  -- user_card_state row can be neither.
  AND (NOT scored.unseen
       OR (sqlc.arg(new_remaining)::int > 0
           AND (new_cutoff.cutoff_key IS NULL
                OR (scored.raw_key, scored.card_id) <= (new_cutoff.cutoff_key, new_cutoff.cutoff_id))))
  -- The deck's combined new+due daily total cutoff (#118; rev_remaining is RevRemaining's return
  -- value, formerly an independent per-deck review cap, #115). The caller (BuildBatch) has already
  -- clamped batch_size below to this same remaining total, so this cutoff and that LIMIT agree on
  -- the same boundary; this cutoff alone is what stops a scarce priority side from starving the
  -- backfill side within one page. Row comparison against rev_cutoff (the (raw_key,id) at the
  -- allowance boundary, computed above) keeps a review-state card in only if it ranks at or before
  -- that boundary; cutoff_key NULL means fewer review-state cards exist than the allowance, so
  -- nothing is excluded. Never-seen and learning/relearning cards (state 0, 1, 3) are unaffected
  -- by this cutoff (never-seen has its own new_remaining cutoff above; learning/relearning is
  -- never capped).
  AND (scored.state IS DISTINCT FROM 2
       OR (sqlc.arg(rev_remaining)::int > 0
           AND (rev_cutoff.cutoff_key IS NULL
                OR (scored.raw_key, scored.card_id) <= (rev_cutoff.cutoff_key, rev_cutoff.cutoff_id))))
  AND ((CASE WHEN sqlc.arg(priority)::text = 'new' THEN (NOT scored.unseen)::int ELSE scored.unseen::int END),
        scored.raw_key, scored.card_id)
      > (sqlc.arg(cursor_group_bit)::int, sqlc.arg(cursor_key)::double precision, sqlc.arg(cursor_card_id)::uuid)
ORDER BY group_bit, sort_key, scored.card_id
LIMIT sqlc.arg(batch_size);

-- The review-state half of mixed new/review interleaving (#116, decks.preset "priority" =
-- "mixed", #118): the same review-side filters and rev_order as ListDueCardsForStudy above, minus
-- never-seen cards entirely (those come from ListNewCardsForStudy) and minus the group-prefix
-- bit (there is only one group here). BuildBatch interleaves the two result sets in Go.
-- name: ListReviewCardsForStudy :many
WITH scored AS (
    SELECT c.id                                        AS card_id,
           c.ordinal                                   AS card_ordinal,
           COALESCE(ucs.due, now())                    AS due,
           ucs.stability::double precision              AS stability,
           ucs.difficulty::double precision             AS difficulty,
           ucs.state::smallint                         AS state,
           ucs.reps::int                                AS reps,
           ucs.lapses::int                              AS lapses,
           ucs.scheduled_days::int                      AS scheduled_days,
           ucs.learning_steps::smallint                AS learning_steps,
           ucs.last_review                             AS last_review,
           n.fields                                    AS note_fields,
           n.tags                                      AS note_tags,
           nt.id                                       AS note_type_id,
           nt.name                                     AS note_type_name,
           nt.is_cloze                                 AS is_cloze,
           t.name                                      AS template_name,
           t.qfmt                                      AS qfmt,
           t.afmt                                      AS afmt,
           ucs.suspended                                AS suspended,
           ucs.buried_until                            AS buried_until,
           (CASE
               WHEN sqlc.arg(rev_order)::text = 'random' THEN
                   ('x' || md5(c.id::text || sqlc.arg(hash_seed)::text))
                   ::bit(52)::bigint::double precision
               WHEN sqlc.arg(rev_order)::text = 'intervalAsc' THEN ucs.scheduled_days::double precision
               WHEN sqlc.arg(rev_order)::text = 'intervalDesc' THEN (-ucs.scheduled_days)::double precision
               ELSE extract(epoch from ucs.due)
           END)                                         AS raw_key
    FROM cards c
    JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                       AND da.can_view AND da.can_study
    JOIN notes n       ON n.id = c.note_id
    JOIN note_types nt ON nt.id = n.note_type_id
    JOIN templates t   ON t.id = c.template_id
    JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
    WHERE c.deck_id = sqlc.arg(deck_id)
)
SELECT scored.card_id, scored.card_ordinal, scored.due, scored.stability, scored.difficulty,
       scored.state, scored.reps, scored.lapses, scored.scheduled_days, scored.learning_steps,
       scored.last_review, scored.note_fields, scored.note_tags, scored.note_type_id,
       scored.note_type_name, scored.is_cloze, scored.template_name, scored.qfmt, scored.afmt,
       scored.raw_key::double precision AS sort_key
FROM scored
LEFT JOIN LATERAL (
    SELECT (CASE
                WHEN sqlc.arg(rev_order)::text = 'random' THEN
                    ('x' || md5(c2.id::text || sqlc.arg(hash_seed)::text))
                    ::bit(52)::bigint::double precision
                WHEN sqlc.arg(rev_order)::text = 'intervalAsc' THEN u2.scheduled_days::double precision
                WHEN sqlc.arg(rev_order)::text = 'intervalDesc' THEN (-u2.scheduled_days)::double precision
                ELSE extract(epoch from u2.due)
            END) AS cutoff_key,
           c2.id AS cutoff_id
    FROM cards c2
    JOIN user_card_state u2 ON u2.user_id = sqlc.arg(user_id) AND u2.card_id = c2.id
    WHERE c2.deck_id = sqlc.arg(deck_id) AND u2.state = 2
      AND NOT u2.suspended
      AND (u2.buried_until IS NULL OR u2.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
      AND u2.due <= sqlc.arg(now)::timestamptz + make_interval(mins => sqlc.arg(look_ahead_minutes)::int)
      AND (u2.last_review IS NULL OR u2.last_review < sqlc.arg(study_day_start)::timestamptz)
    ORDER BY cutoff_key, c2.id
    OFFSET GREATEST(sqlc.arg(rev_remaining)::int - 1, 0)
    LIMIT 1
) rev_cutoff ON true
WHERE NOT scored.suspended
  AND (scored.buried_until IS NULL OR scored.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
  -- look_ahead_minutes (#154): same widening and same reason as ListDueCardsForStudy's doc comment
  -- above -- this filter and its rev_cutoff subquery must agree on the cutoff.
  AND scored.due <= sqlc.arg(now)::timestamptz + make_interval(mins => sqlc.arg(look_ahead_minutes)::int)
  AND (scored.last_review IS NULL OR scored.last_review < sqlc.arg(study_day_start)::timestamptz)
  AND (scored.state IS DISTINCT FROM 2
       OR (sqlc.arg(rev_remaining)::int > 0
           AND (rev_cutoff.cutoff_key IS NULL
                OR (scored.raw_key, scored.card_id) <= (rev_cutoff.cutoff_key, rev_cutoff.cutoff_id))))
  AND (scored.raw_key, scored.card_id) > (sqlc.arg(cursor_key)::double precision, sqlc.arg(cursor_card_id)::uuid)
ORDER BY sort_key, scored.card_id
LIMIT sqlc.arg(batch_size);

-- The never-seen half of mixed new/review interleaving (#116): identical to ListDueCardsForStudy's
-- never-seen branch, ordered by (import_due_position, id) -- Anki's persisted new-card queue
-- position when the import carried one, else every position-less card after every real one,
-- falling back to id (#82; full gather-mode config -- random/other-field ordering -- stays out of
-- scope, #117). Keyset over the same (position, id) pair. BuildBatch interleaves the two result
-- sets in Go.
-- name: ListNewCardsForStudy :many
WITH scored AS (
    -- Sentinel: keep 2147483647 in sync with the other 3 occurrences in this file (search for
    -- it) -- see ListDueCardsForStudy's raw_key CASE doc comment (#82).
    SELECT c.id                                                            AS card_id,
           c.ordinal                                                       AS card_ordinal,
           COALESCE(c.import_due_position, 2147483647)::double precision   AS sort_key,
           n.fields                                                       AS note_fields,
           n.tags                                                         AS note_tags,
           nt.id                                                          AS note_type_id,
           nt.name                                                        AS note_type_name,
           nt.is_cloze                                                    AS is_cloze,
           t.name                                                         AS template_name,
           t.qfmt                                                         AS qfmt,
           t.afmt                                                         AS afmt
    FROM cards c
    JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                       AND da.can_view AND da.can_study
    JOIN notes n       ON n.id = c.note_id
    JOIN note_types nt ON nt.id = n.note_type_id
    JOIN templates t   ON t.id = c.template_id
    LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
    WHERE c.deck_id = sqlc.arg(deck_id)
      AND ucs.user_id IS NULL
)
SELECT scored.card_id, scored.card_ordinal,
       now()::timestamptz                          AS due,
       0::double precision                          AS stability,
       0::double precision                          AS difficulty,
       0::smallint                                  AS state,
       0::int                                       AS reps,
       0::int                                       AS lapses,
       0::int                                       AS scheduled_days,
       0::smallint                                  AS learning_steps,
       scored.note_fields, scored.note_tags, scored.note_type_id, scored.note_type_name,
       scored.is_cloze, scored.template_name, scored.qfmt, scored.afmt,
       scored.sort_key
FROM scored
-- Same InitPlan shape and (position, id) ranking as ListDueCardsForStudy's new_cutoff -- see its
-- doc comment (#82, #101).
LEFT JOIN LATERAL (
    SELECT COALESCE(c2.import_due_position, 2147483647)::double precision AS cutoff_key,
           c2.id AS cutoff_id
    FROM cards c2
    LEFT JOIN user_card_state u2
           ON u2.user_id = sqlc.arg(user_id) AND u2.card_id = c2.id
    WHERE c2.deck_id = sqlc.arg(deck_id) AND u2.user_id IS NULL
    ORDER BY cutoff_key, c2.id
    OFFSET GREATEST(sqlc.arg(new_remaining)::int - 1, 0)
    LIMIT 1
) new_cutoff ON true
WHERE sqlc.arg(new_remaining)::int > 0
  AND (new_cutoff.cutoff_key IS NULL
       OR (scored.sort_key, scored.card_id) <= (new_cutoff.cutoff_key, new_cutoff.cutoff_id))
  AND (scored.sort_key, scored.card_id) > (sqlc.arg(cursor_key)::double precision, sqlc.arg(cursor_card_id)::uuid)
ORDER BY scored.sort_key, scored.card_id
LIMIT sqlc.arg(batch_size);

-- New-card introductions inside the current study day, for one deck (#101). A card is introduced
-- by the one review that takes it out of FSRS state New, so review_log.state_before = 0 is the
-- exact marker: a lapse carries 2, a relearning step 3, a same-day learning-step repeat 1, and
-- none of them can be mistaken for an introduction. count(DISTINCT card_id), not count(*): the
-- out-of-order replay path (architecture.md §6) can leave two state_before = 0 rows for one card,
-- and a card is introduced once.
-- name: CountNewIntroducedToday :one
SELECT count(DISTINCT rl.card_id)::bigint AS introduced_count
FROM review_log rl
JOIN cards c       ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND c.deck_id = sqlc.arg(deck_id)
  AND rl.state_before = 0
  AND rl.reviewed_at >= sqlc.arg(study_day_start)::timestamptz
  AND rl.reviewed_at <  sqlc.arg(study_day_end)::timestamptz;

-- Review-state (state=2) cards answered inside the current study day, for one deck (#115). Summed
-- with CountNewIntroducedToday's result by the caller (BuildBatch) to net against rev.perDay's
-- combined new+due total (#118). rl.state_before = 2 marks "this card was already in review state
-- when answered" -- the same marker ListDueCardsForStudy's rev_cutoff excludes past the allowance.
-- count(DISTINCT card_id), not count(*), for the same out-of-order replay reason as
-- CountNewIntroducedToday.
-- name: CountReviewedToday :one
SELECT count(DISTINCT rl.card_id)::bigint AS reviewed_count
FROM review_log rl
JOIN cards c       ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND c.deck_id = sqlc.arg(deck_id)
  AND rl.state_before = 2
  AND rl.reviewed_at >= sqlc.arg(study_day_start)::timestamptz
  AND rl.reviewed_at <  sqlc.arg(study_day_end)::timestamptz;

-- Same as CountNewIntroducedToday, grouped by deck, for the /decks list (#137). One query for
-- every deck the user can view rather than one CountNewIntroducedToday call per row.
-- name: CountNewIntroducedTodayForUser :many
SELECT c.deck_id                                     AS deck_id,
       count(DISTINCT rl.card_id)::bigint            AS introduced_count
FROM review_log rl
JOIN cards c       ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND rl.state_before = 0
  AND rl.reviewed_at >= sqlc.arg(study_day_start)::timestamptz
  AND rl.reviewed_at <  sqlc.arg(study_day_end)::timestamptz
GROUP BY c.deck_id;

-- Same as CountReviewedToday, grouped by deck, for the /decks list (#137). One query for every
-- deck the user can view rather than one CountReviewedToday call per row.
-- name: CountReviewedTodayForUser :many
SELECT c.deck_id                                     AS deck_id,
       count(DISTINCT rl.card_id)::bigint            AS reviewed_count
FROM review_log rl
JOIN cards c       ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND rl.state_before = 2
  AND rl.reviewed_at >= sqlc.arg(study_day_start)::timestamptz
  AND rl.reviewed_at <  sqlc.arg(study_day_end)::timestamptz
GROUP BY c.deck_id;

-- Queue summary (New/Learning/Due) for one deck's study page (#80). Same eligibility filters as
-- ListDueCardsForStudy -- suspended, buried, due now or earlier, not already reviewed today --
-- so the counts agree with what /decks/{id}/review actually serves. Learning folds together
-- state 1 (learning) and 3 (relearning); Due is state 2 (review).
-- name: CountQueueForDeck :one
SELECT count(*) FILTER (WHERE ucs.user_id IS NULL)   AS new_count,
       count(*) FILTER (WHERE ucs.state IN (1, 3))   AS learning_count,
       count(*) FILTER (WHERE ucs.state = 2)         AS due_count
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
WHERE c.deck_id = sqlc.arg(deck_id)
  AND NOT COALESCE(ucs.suspended, false)
  AND (ucs.buried_until IS NULL OR ucs.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
  AND (ucs.due IS NULL OR ucs.due <= sqlc.arg(now)::timestamptz + make_interval(mins => sqlc.arg(look_ahead_minutes)::int))
  AND (ucs.last_review IS NULL OR ucs.last_review < sqlc.arg(study_day_start)::timestamptz);

-- Same queue summary, grouped by deck, for the /decks list (#80). One query for every deck the
-- user can view rather than one CountQueueForDeck call per row.
--
-- look_ahead_minutes (#154) varies per deck, but this query spans every deck the user can view in
-- one grouped result, so it can't take a single scalar arg the way CountQueueForDeck does. The
-- caller passes two parallel arrays (deck_ids, look_ahead_minutes -- each deck's value already
-- parsed from its own preset in Go, same as everywhere else preset fields are read) and `la`
-- unnests them into one row per deck, joined on deck_id, so each deck's due filter widens by its
-- own configured amount. A deck_id with no matching row in either array (there won't be one, since
-- the caller derives both from the same deck list) would simply drop out of the INNER JOIN.
-- name: CountQueueForUser :many
WITH la AS (
    SELECT d.deck_id, l.look_ahead_minutes
    FROM unnest(sqlc.arg(deck_ids)::uuid[]) WITH ORDINALITY AS d(deck_id, ord)
    JOIN unnest(sqlc.arg(look_ahead_minutes)::int[]) WITH ORDINALITY AS l(look_ahead_minutes, ord)
      ON d.ord = l.ord
)
SELECT c.deck_id                                     AS deck_id,
       count(*) FILTER (WHERE ucs.user_id IS NULL)   AS new_count,
       count(*) FILTER (WHERE ucs.state IN (1, 3))   AS learning_count,
       count(*) FILTER (WHERE ucs.state = 2)         AS due_count
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
JOIN la ON la.deck_id = c.deck_id
LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
WHERE NOT COALESCE(ucs.suspended, false)
  AND (ucs.buried_until IS NULL OR ucs.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
  AND (ucs.due IS NULL OR ucs.due <= sqlc.arg(now)::timestamptz + make_interval(mins => la.look_ahead_minutes))
  AND (ucs.last_review IS NULL OR ucs.last_review < sqlc.arg(study_day_start)::timestamptz)
GROUP BY c.deck_id;

-- Note-type CSS for every card in the deck: sanitised once per page, never per card (#55's doc
-- comment), so a refilled card can never arrive before its styles.
-- name: ListNoteTypeCSSForDeck :many
SELECT DISTINCT nt.id, nt.css
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
JOIN notes n       ON n.id = c.note_id
JOIN note_types nt ON nt.id = n.note_type_id
WHERE c.deck_id = sqlc.arg(deck_id);

-- Per-card authorisation for a grade batch, which may span decks. A card missing from the result is
-- absent, invisible, or not studyable -- deliberately indistinguishable (docs/schema.md).
-- name: ListStudyableCards :many
SELECT c.id AS card_id, c.deck_id
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE c.id = ANY(sqlc.arg(card_ids)::uuid[]);

-- The per-(user,card) advisory lock, held to commit (architecture.md §6). Advisory rather than
-- SELECT ... FOR UPDATE because a never-seen card has no user_card_state row to lock, and two
-- concurrent first grades are exactly that case. The key is derived in Go -- see internal/review/
-- lock.go for the derivation and for why a batch's keys are acquired in ascending key order.
-- name: LockCardForGrade :exec
SELECT pg_advisory_xact_lock(sqlc.arg(key)::bigint);
