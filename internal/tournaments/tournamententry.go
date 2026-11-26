// A parent entering their own child in a tournament.
//
// This used to be the generic registration POST, which wrote whatever the
// parent's request carried — including `fee_charged` and `status`. The card
// payment charges `fee_charged`, so a family could enter at a price they chose
// and pay exactly that. Here the parent says only what is theirs to say — which
// child, a contact number, and the two notes — and the server fills in the
// rest: the child's name from the academy's records, the price from the
// tournament (pricing.go), and the status staff entry has always had.
package tournaments

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

type entryRequest struct {
	StudentID    string `json:"student_id"`
	Contact      string `json:"participant_contact"`
	MedicalNotes string `json:"medical_notes"`
	Remarks      string `json:"remarks"`
	// The ID card check the scan returned (idcheck.go): required, and the
	// child's date of birth for the age group comes from it.
	IDCheck    string `json:"id_check"`
	CategoryID string `json:"tournament_category_id"`
	// What the public form asks too: the name called across the hall, the
	// name in Thai script (optional), and the conditions of entry.
	Nickname    string `json:"nickname"`
	NameTh      string `json:"participant_name_th"`
	AcceptTerms bool   `json:"accept_terms"`
	/* The privacy notice (PDPA s.23), recorded when the app says the parent
	   saw it. Not refused when absent: phones still on an older build send
	   no such field, and the parent is entering their own child under the
	   academy's contract with them. */
	PrivacyNotice bool `json:"privacy_notice"`
	/* Explicit consent for medical notes, which are health data (PDPA s.26).
	   Required whenever any are given. */
	HealthConsent bool `json:"health_consent"`
}

// handleEnterTournament registers one of the caller's children for the
// tournament in the path, priced by the server.
//
// Parents only. Staff register at the desk through the ordinary resource, where
// setting a fee by hand is their job; a Student may not commit their family to
// a fee.
func handleEnterTournament(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		if id.Role != "Parent" {
			httpx.Error(ctx, http.StatusForbidden, "only a parent may enter their child", nil)
			return
		}
		// Unknown fields are refused rather than ignored, so a request that
		// still carries a fee fails loudly instead of appearing to work.
		var in entryRequest
		if err := httpx.Decode(ctx, &in); err != nil {
			httpx.Error(ctx, http.StatusBadRequest, "invalid body", nil)
			return
		}
		if msg := in.validate(); msg != "" {
			httpx.Error(ctx, http.StatusBadRequest, msg, nil)
			return
		}
		tournamentID := ctx.Param("id")
		entry, err := registerChild(d, id, tournamentID, in)
		if err != nil {
			writeRegistrationError(ctx, err)
			return
		}
		httpx.JSON(ctx, http.StatusCreated, map[string]any{
			"tournament_registration_id": entry.id,
			"tournament_id":              tournamentID,
			"student_id":                 in.StudentID,
			"participant_name":           entry.playerName,
			"status":                     "Approved",
			"fee_charged":                entry.fee,
			"tournament_category_id":     db.NullIfEmpty(in.CategoryID),
			"participant_date_of_birth":  entry.dateOfBirth,
		})
	}
}

func (in *entryRequest) validate() string {
	in.StudentID = strings.TrimSpace(in.StudentID)
	in.Contact = strings.TrimSpace(in.Contact)
	if in.StudentID == "" {
		return "student_id is required"
	}
	in.Nickname = strings.TrimSpace(in.Nickname)
	in.NameTh = strings.TrimSpace(in.NameTh)
	if in.Nickname == "" {
		return "please give the player's nickname"
	}
	if len([]rune(in.Nickname)) > maxNameLen || len([]rune(in.NameTh)) > maxNameLen {
		return "that name is too long"
	}
	// Refused rather than defaulted, as on the public form: a record of
	// agreement nobody gave is worse than none.
	if !in.AcceptTerms {
		return "please accept the terms and conditions to enter"
	}
	if strings.TrimSpace(in.MedicalNotes) != "" && !in.HealthConsent {
		return "please agree to the academy keeping the medical notes, or leave them empty"
	}
	if len(in.Contact) > maxPhoneLen {
		return "participant_contact is too long"
	}
	if err := CheckRegistrationNotes(map[string]any{
		"medical_notes": in.MedicalNotes, "remarks": in.Remarks,
	}); err != nil {
		return err.Error()
	}

	in.CategoryID = strings.TrimSpace(in.CategoryID)
	return ""
}

func mountTournamentEntry(mux gin.IRoutes, d *sql.DB) {
	mux.POST("/api/v1/tournaments/:id/entries", handleEnterTournament(d))
}

// entryClosed says why a tournament is not taking entries, or "" when it is:
// a draft, registration closed by the organiser (the public registration
// switch on the tournament's card, which closes the parent portal too), the
// closing date passed, or every place taken. The public form answers to the
// same three (registrationOpen).
func entryClosed(q interface {
	QueryRow(string, ...any) *sql.Row
}, tournamentID string) (string, error) {
	var draft, open, taken int
	var deadline string
	var capacity sql.NullInt64
	err := q.QueryRow(`
		SELECT draft, COALESCE(public_registration, 0), COALESCE(registration_deadline, ''), max_participants,
		       (SELECT COUNT(*) FROM tournament_registration r
		         WHERE r.tournament_id = t.tournament_id AND r.status IN ('Pending','Approved'))
		  FROM tournament t WHERE tournament_id = ?`, tournamentID).Scan(&draft, &open, &deadline, &capacity, &taken)
	if err != nil {
		return "", err
	}
	if draft == 1 {
		return "", sql.ErrNoRows
	}
	if open == 0 {
		return "registration for this tournament is closed", nil
	}
	var limit *int
	if capacity.Valid {
		n := int(capacity.Int64)
		limit = &n
	}
	if ok, why := registrationOpen(deadline, limit, taken); !ok {
		if why == "full" {
			return "this tournament is full", nil
		}
		return "registration for this tournament has closed", nil
	}
	return "", nil
}

// DuplicateEntryMessage names the two ways a tournament entry can repeat one
// already there: the same JCA student (0014), or the same player by email,
// name and date of birth (0070) — one email may enter several players.
// Anything else is not a duplicate it knows.
func DuplicateEntryMessage(err error) string {
	text := err.Error()
	if !strings.Contains(text, "UNIQUE") {
		return ""
	}
	switch {
	case strings.Contains(text, "student_id"):
		return "this child already has an entry in this tournament"
	case strings.Contains(text, "one_per_player"):
		return "this player is already entered in this tournament"
	}
	return ""
}
