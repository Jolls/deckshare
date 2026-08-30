package media

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Jolls/enshu/internal/db"
)

// mediaRefsSha256FK is the constraint that makes a referenced blob undeletable, and so the one
// whose violation the sweep reads as "re-referenced mid-sweep" rather than as a failure.
const mediaRefsSha256FK = "media_refs_sha256_fkey"

// GC reclaims orphaned media (#91, docs/plans/91-orphaned-media-blob-gc.md). Two classes, neither
// reachable by the other's method: media_blobs rows whose last media_refs row cascaded away with a
// deleted deck, and files a rolled-back import left on disk with no row at all (Put writes bytes
// before the import transaction commits, and no rollback can take them back).
type GC struct {
	q     *db.Queries
	blobs *Store
	// grace is how long a file with no media_blobs row must sit untouched before it counts as an
	// orphan. Inside that window "orphan" and "import still in flight" look identical on disk.
	grace time.Duration
}

// NewGC builds a GC over any DBTX -- a *pgxpool.Pool in production, a pgx.Tx in tests -- the way
// auth.New does.
func NewGC(dbtx db.DBTX, blobs *Store, grace time.Duration) *GC {
	return &GC{q: db.New(dbtx), blobs: blobs, grace: grace}
}

// Run sweeps orphaned media until ctx is cancelled. A failed sweep is logged and retried on the
// next tick: nothing here is worth taking the server down for, and both classes are re-detected
// from scratch every time.
func (g *GC) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.Sweep(ctx); err != nil {
				log.Printf("media gc: %v", err)
			}
		}
	}
}

// Sweep reclaims both orphan classes once. Per-blob failures are collected rather than returned
// immediately, so one unreadable file cannot stop the rest of the sweep.
func (g *GC) Sweep(ctx context.Context) error {
	return errors.Join(g.sweepUnreferencedRows(ctx), g.sweepOrphanFiles(ctx))
}

// sweepUnreferencedRows handles class 1: rows with no media_refs. It unlinks the file before
// deleting the row, never the reverse (plan §2) -- a committed row delete lets a concurrent import
// adopt the still-present file and re-reference it, and the unlink then strands a live row. This
// way round, that import's CreateMediaRef hits the missing parent instead and rolls the whole
// import back. The mirror case, a ref committed just before the DELETE runs, is the RESTRICT
// violation below: expected, skipped, re-listed on the next tick.
func (g *GC) sweepUnreferencedRows(ctx context.Context) error {
	shas, err := g.q.ListUnreferencedMediaBlobs(ctx)
	if err != nil {
		return fmt.Errorf("media gc: listing unreferenced blobs: %w", err)
	}

	var errs []error
	for _, sha := range shas {
		if err := g.blobs.Delete(sha); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := g.q.DeleteMediaBlob(ctx, sha); err != nil {
			if db.IsRestrictViolation(err, mediaRefsSha256FK) {
				continue
			}
			errs = append(errs, fmt.Errorf("media gc: deleting blob row %s: %w", sha, err))
		}
	}
	return errors.Join(errs...)
}

// sweepOrphanFiles handles class 2: files with no media_blobs row, older than the grace period.
// The walk only collects candidates; whether each one still has a row is one batched query rather
// than a round trip per file, since a deployment with many rolled-back imports can mean thousands
// of candidates in a single sweep.
func (g *GC) sweepOrphanFiles(ctx context.Context) error {
	cutoff := time.Now().Add(-g.grace)

	var candidates []string
	if err := g.blobs.Walk(func(sha string, modTime time.Time) error {
		if !modTime.After(cutoff) {
			candidates = append(candidates, sha)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("media gc: walking store: %w", err)
	}
	if len(candidates) == 0 {
		return nil
	}

	existing, err := g.q.ListExistingMediaBlobs(ctx, candidates)
	if err != nil {
		return fmt.Errorf("media gc: checking blob rows: %w", err)
	}
	hasRow := make(map[string]bool, len(existing))
	for _, sha := range existing {
		hasRow[sha] = true
	}

	var errs []error
	for _, sha := range candidates {
		if hasRow[sha] {
			continue
		}
		if err := g.blobs.Delete(sha); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
