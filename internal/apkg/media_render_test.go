package apkg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jolls/enshu/internal/db"
	"github.com/Jolls/enshu/internal/fsrs"
	"github.com/Jolls/enshu/internal/review"
)

// TestImport_ImageRendersFromApkgImport is the end-to-end regression test for the "images don't
// display" bug: a note field imported from a real .apkg carries Anki's raw
// <img src="cat.jpg"> convention untouched (dbwrite.go's importNotes never rewrites field HTML),
// so it is the reviewer's render path -- review.BuildBatch, through render.RewriteMediaSrcs --
// that must resolve it against media_refs to /media/{sha256} before the card ever reaches a
// browser. A regression here means every imported image 404s again.
func TestImport_ImageRendersFromApkgImport(t *testing.T) {
	tx := beginTx(t)
	ctx := context.Background()
	ownerID := seedUser(t, tx)
	store := testMediaStore(t)
	q := db.New(tx)

	spec := defaultSynthSpec(t)
	spec.Notes[0].Fields[0] = `front1 <img src="cat.jpg">`
	pkg := buildSchema11Package(t, spec)
	col := readBytes(t, pkg)
	m := syntheticMedia("cat.jpg", []byte("a fake jpeg"))
	col.Media = []IrMedia{m}

	if _, err := Import(ctx, tx, ownerID, col, time.Now(), store); err != nil {
		t.Fatalf("Import: %v", err)
	}

	deck, err := q.GetDeckByOwnerAndName(ctx, db.GetDeckByOwnerAndNameParams{OwnerID: ownerID, Name: "Default"})
	if err != nil {
		t.Fatalf("GetDeckByOwnerAndName: %v", err)
	}

	p, err := fsrs.NewDefaultParams(0.9)
	if err != nil {
		t.Fatalf("NewDefaultParams: %v", err)
	}
	now := time.Now()
	window := review.StudyDay{Start: now.Add(-time.Hour), End: now.Add(23 * time.Hour)}
	batch, err := review.BuildBatch(ctx, tx, p, ownerID, deck.ID, deck.Name, window,
		review.DefaultNewPerDay, review.DefaultRevPerDay, review.RevOrderDue, review.NewMixAfterReviews,
		review.Cursor{AtStart: true}, 30, now)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}

	var sawRewritten bool
	for _, c := range batch.Cards {
		if strings.Contains(string(c.Question), `src="cat.jpg"`) {
			t.Fatalf("card %s: question HTML still carries the raw Anki filename unrewritten: %s",
				c.CardID.String(), c.Question)
		}
		if strings.Contains(string(c.Question), "/media/"+m.SHA256) {
			sawRewritten = true
		}
	}
	if !sawRewritten {
		t.Fatal("no card's question HTML contains the rewritten /media/{sha256} src")
	}
}
