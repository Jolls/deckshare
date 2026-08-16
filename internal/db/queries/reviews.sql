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

-- The queue. Keyset over (COALESCE(due,'infinity'), card_id): due reviews first by due date, then
-- never-seen cards by id. 'infinity' (not NULL) keeps the sort key total -- a NULL inside the row
-- comparison below would silently drop every new card from every refill. Never-seen cards have no
-- user_card_state row at all, hence the LEFT JOIN and the COALESCEd columns.
-- name: ListDueCardsForStudy :many
SELECT c.id                                        AS card_id,
       c.ordinal                                   AS card_ordinal,
       COALESCE(ucs.due, 'infinity'::timestamptz)  AS queue_key,
       (ucs.user_id IS NULL)::boolean              AS unseen,
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
       t.afmt                                      AS afmt
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
JOIN notes n       ON n.id = c.note_id
JOIN note_types nt ON nt.id = n.note_type_id
JOIN templates t   ON t.id = c.template_id
LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
WHERE c.deck_id = sqlc.arg(deck_id)
  AND NOT COALESCE(ucs.suspended, false)
  AND (ucs.buried_until IS NULL OR ucs.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
  AND (ucs.due IS NULL OR ucs.due < sqlc.arg(study_day_end)::timestamptz)
  AND (ucs.last_review IS NULL OR ucs.last_review < sqlc.arg(study_day_start)::timestamptz)
  AND (COALESCE(ucs.due, 'infinity'::timestamptz), c.id)
      > (sqlc.arg(cursor_due)::timestamptz, sqlc.arg(cursor_card_id)::uuid)
ORDER BY queue_key, c.id
LIMIT sqlc.arg(batch_size);

-- Queue summary (New/Learning/Due) for one deck's study page (#80). Same eligibility filters as
-- ListDueCardsForStudy -- suspended, buried, due-before-window-end, not already reviewed today --
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
  AND (ucs.due IS NULL OR ucs.due < sqlc.arg(study_day_end)::timestamptz)
  AND (ucs.last_review IS NULL OR ucs.last_review < sqlc.arg(study_day_start)::timestamptz);

-- Same queue summary, grouped by deck, for the /decks list (#80). One query for every deck the
-- user can view rather than one CountQueueForDeck call per row.
-- name: CountQueueForUser :many
SELECT c.deck_id                                     AS deck_id,
       count(*) FILTER (WHERE ucs.user_id IS NULL)   AS new_count,
       count(*) FILTER (WHERE ucs.state IN (1, 3))   AS learning_count,
       count(*) FILTER (WHERE ucs.state = 2)         AS due_count
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
WHERE NOT COALESCE(ucs.suspended, false)
  AND (ucs.buried_until IS NULL OR ucs.buried_until <= (sqlc.arg(study_day_start)::timestamptz)::date)
  AND (ucs.due IS NULL OR ucs.due < sqlc.arg(study_day_end)::timestamptz)
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
