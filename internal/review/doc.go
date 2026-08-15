// Package review builds batch previews, grades reviews, and replays review_log history
// (architecture.md §6). GradeBatch is the server-authoritative write path (CLAUDE.md §2.7): it
// owns the four concurrency mechanisms that let two overlapping batches for the same cards
// converge safely -- advisory locks acquired in ascending sorted-key order (deadlock avoidance),
// events applied in reviewed_at order, events already present in review_log skipped
// (idempotency), and a card whose stored last_review postdates an incoming event replayed from
// review_log instead of scheduled forward. This package maps DB rows to internal/fsrs.CardState
// and back; internal/fsrs itself stays pure (no DB, no HTTP -- CLAUDE.md §17).
package review
