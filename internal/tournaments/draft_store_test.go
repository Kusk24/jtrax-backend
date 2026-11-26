// Draft lifecycle tests exercise transaction ordering directly with SQLite.
package tournaments

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

func newDraftStore(t *testing.T) draftStore {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "draft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, q := range []string{
		"INSERT INTO tournament (tournament_id, name, draft) VALUES ('cup', 'School cup', 1)",
		"INSERT INTO tournament_category (tournament_category_id, tournament_id, name) VALUES ('cup-u10', 'cup', 'Under 10')",
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return draftStore{d}
}

func TestDiscardRollbackKeepsDraftAndCategories(t *testing.T) {
	store := newDraftStore(t)
	_, err := store.db.Exec(`CREATE TRIGGER refuse_discard BEFORE DELETE ON tournament
		BEGIN SELECT RAISE(ABORT, 'discard unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.discard("cup"); err == nil {
		t.Fatal("expected discard to fail")
	}
	if err := requireDraft(store.db, "cup"); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := store.db.QueryRow("SELECT tournament_category_id FROM tournament_category WHERE tournament_id = 'cup'").Scan(&id); err != nil || id != "cup-u10" {
		t.Fatalf("category after rollback = %q, error = %v", id, err)
	}
}

func TestConcurrentDraftPublishAndDiscard(t *testing.T) {
	store := newDraftStore(t)
	published := make(chan error, 1)
	go func() { published <- store.publish("cup") }()
	discardErr, publishErr := store.discard("cup"), <-published
	var categories int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM tournament_category WHERE tournament_id = 'cup'").Scan(&categories); err != nil {
		t.Fatal(err)
	}
	switch {
	case publishErr == nil && errors.Is(discardErr, errDiscardLive) && categories == 1:
	case discardErr == nil && errors.Is(publishErr, errTournamentMissing) && categories == 0:
	default:
		t.Fatalf("publish = %v, discard = %v, remaining categories = %d", publishErr, discardErr, categories)
	}
}
