// The tournament draft lifecycle and its SQLite transaction boundaries.
package tournaments

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

type draftStore struct{ db *sql.DB }

var (
	errTournamentMissing  = errors.New("tournament missing")
	errTournamentLive     = errors.New("tournament already live")
	errDiscardLive        = errors.New("live tournament cannot be discarded")
	errLoadDraft          = errors.New("load draft state")
	errPreviewUnavailable = errors.New("preview unavailable")
)

// A draft-state or token lookup can use the database or an open transaction.
type draftReader interface {
	QueryRow(string, ...any) *sql.Row
}

func requireDraft(reader draftReader, id string) error {
	var flag int
	err := reader.QueryRow("SELECT draft FROM tournament WHERE tournament_id = ?", id).Scan(&flag)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return errTournamentMissing
	case err != nil:
		return fmt.Errorf("%w: %w", errLoadDraft, err)
	case flag != 1:
		return errTournamentLive
	default:
		return nil
	}
}

// A staff member reopening the wizard gets the same token. Check and assign in
// one transaction so publishing cannot invalidate the draft between those steps.
func (s draftStore) previewToken(id string) (string, error) {
	var token string
	err := db.InTransaction(s.db, func(tx *sql.Tx) error {
		if err := requireDraft(tx, id); err != nil {
			return err
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		if _, err := tx.Exec(
			"UPDATE tournament SET preview_token = ? WHERE tournament_id = ? AND preview_token IS NULL",
			hex.EncodeToString(raw), id); err != nil {
			return err
		}
		return tx.QueryRow("SELECT preview_token FROM tournament WHERE tournament_id = ?", id).Scan(&token)
	})
	return token, err
}

func (s draftStore) publish(id string) error {
	res, err := s.db.Exec(`UPDATE tournament SET draft = 0, public_registration = 1, preview_token = NULL
		WHERE tournament_id = ? AND draft = 1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// The staff endpoint has historically returned 404 if it cannot find the row.
		var exists int
		if err := s.db.QueryRow("SELECT 1 FROM tournament WHERE tournament_id = ?", id).Scan(&exists); err != nil {
			return errTournamentMissing
		}
		return errTournamentLive
	}
	return nil
}

func (s draftStore) discard(id string) error {
	return db.InTransaction(s.db, func(tx *sql.Tx) error {
		if err := requireDraft(tx, id); err != nil {
			if errors.Is(err, errTournamentLive) {
				return errDiscardLive
			}
			return err
		}
		for _, query := range []string{
			"DELETE FROM tournament_category WHERE tournament_id = ?",
			"DELETE FROM tournament_regulation WHERE tournament_id = ?",
			"DELETE FROM tournament_banner WHERE tournament_id = ?",
			"DELETE FROM tournament WHERE tournament_id = ? AND draft = 1",
		} {
			if _, err := tx.Exec(query, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func previewAllowed(reader draftReader, tournamentID, given string) bool {
	if given == "" {
		return false
	}
	var token sql.NullString
	err := reader.QueryRow("SELECT preview_token FROM tournament WHERE tournament_id = ? AND draft = 1", tournamentID).Scan(&token)
	return err == nil && token.Valid && subtle.ConstantTimeCompare([]byte(given), []byte(token.String)) == 1
}

// Field order preserves the public JSON representation shared with the portal.
type draftPreview struct {
	Categories []publicCategory  `json:"categories"`
	Preview    bool              `json:"preview"`
	Tournament *publicTournament `json:"tournament"`
}

func (s draftStore) preview(id, given string, cardPayments bool) (*draftPreview, error) {
	if !previewAllowed(s.db, id, given) {
		return nil, errPreviewUnavailable
	}
	tournament, err := scanPublicTournament(s.db.QueryRow(publicTournamentSelect+
		" WHERE t.tournament_id = ? AND t.draft = 1", id))
	if err != nil {
		return nil, err
	}
	categories, err := publicCategories(s.db, id)
	if err != nil {
		return nil, err
	}
	tournament.CardPayments = cardPayments
	return &draftPreview{Categories: categories, Preview: true, Tournament: tournament}, nil
}
