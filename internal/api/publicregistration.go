// Public tournament registration: the three endpoints a stranger may call.
//
// Everything here is unauthenticated, which makes it the widest door in the
// product and the only place where a row is created by somebody the academy has
// never met. Four things hold that door:
//
//   - a tournament is closed until an organiser opens it (public_registration),
//     exactly like results_public;
//   - every submission lands as Pending and a member of staff approves it, so
//     nothing a stranger types becomes a participant on its own;
//   - the deadline and the capacity are enforced inside the write transaction,
//     not read beforehand and hoped about;
//   - it is rate-limited, and one email may hold one live place per event.
//
// # Why the discount is claimed rather than detected
//
// A registrant ticks "I am a JCA student" and is quoted the discounted fee on
// that claim alone. The server does look their email up against the academy's
// own records, but it never says so in the reply: if the discount appeared only
// for addresses that matched, this endpoint would be a way to test whether a
// given child is a pupil here, one guess at a time. The match is passed to the
// approval queue instead, where it belongs — staff see "claimed, and we found a
// matching student" or "claimed, no match" and decide.
package api

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// Bounds for what a stranger may put in the database. Generous enough for a
// real Thai name written in full, tight enough that the column is not a place
// to park a payload.
const (
	maxNameLen  = 80
	maxEmailLen = 254 // RFC 5321
	maxPhoneLen = 32
)

// publicTournamentSelect is the shape of an open event as the public sees it.
// Deliberately narrow: no organiser contact, no internal ids beyond the one
// needed to register, nothing about who else has signed up beyond a count.
const publicTournamentSelect = `
	SELECT t.tournament_id, t.name, t.tournament_status,
	       COALESCE(t.start_date,''), COALESCE(t.end_date,''),
	       COALESCE(t.venue_name,''), COALESCE(t.venue_address,''),
	       COALESCE(t.registration_deadline,''),
	       COALESCE(t.regular_fee, t.early_bird_fee, 0),
	       t.student_discount_pct, t.max_participants,
	       (SELECT COUNT(*) FROM tournament_registration r
	         WHERE r.tournament_id = t.tournament_id
	           AND r.status IN ('Pending','Approved'))
	FROM tournament t`

type publicTournament struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	StartDate    string  `json:"startDate"`
	EndDate      string  `json:"endDate"`
	VenueName    string  `json:"venueName"`
	VenueAddress string  `json:"venueAddress"`
	Deadline     string  `json:"registrationDeadline"`
	Fee          float64 `json:"fee"`
	StudentFee   float64 `json:"studentFee"`
	DiscountPct  int     `json:"studentDiscountPct"`
	Capacity     *int    `json:"capacity"`
	Taken        int     `json:"taken"`
	SpotsLeft    *int    `json:"spotsLeft"`
	Open         bool    `json:"open"`
	ClosedReason string  `json:"closedReason,omitempty"`
}

// scanPublicTournament reads one row of publicTournamentSelect and works out
// whether it is still taking entries.
func scanPublicTournament(sc interface{ Scan(...any) error }) (*publicTournament, error) {
	var t publicTournament
	var capacity sql.NullInt64
	if err := sc.Scan(&t.ID, &t.Name, &t.Status, &t.StartDate, &t.EndDate,
		&t.VenueName, &t.VenueAddress, &t.Deadline, &t.Fee, &t.DiscountPct,
		&capacity, &t.Taken); err != nil {
		return nil, err
	}
	if capacity.Valid {
		n := int(capacity.Int64)
		t.Capacity = &n
		left := n - t.Taken
		if left < 0 {
			left = 0
		}
		t.SpotsLeft = &left
	}
	t.StudentFee = discounted(t.Fee, t.DiscountPct)
	t.Open, t.ClosedReason = registrationOpen(t.Deadline, t.Capacity, t.Taken)
	return &t, nil
}

// discounted applies a percentage off, rounded to the nearest whole unit of
// currency. Fees here are whole baht; half a baht is not a price anybody quotes.
func discounted(fee float64, pct int) float64 {
	if pct <= 0 || fee <= 0 {
		return fee
	}
	return math.Round(fee * float64(100-pct) / 100)
}

// registrationOpen reports whether entries are still being taken, and if not,
// which of the two reasons applies. The reason is shown to the public, so it is
// a fact about the event rather than about anybody who registered.
func registrationOpen(deadline string, capacity *int, taken int) (bool, string) {
	if deadline != "" && todayISO() > deadline {
		return false, "deadline"
	}
	if capacity != nil && taken >= *capacity {
		return false, "full"
	}
	return true, ""
}

// todayISO is the date the deadline is compared against. Dates in this schema
// are stored as plain YYYY-MM-DD with no zone, so the comparison is a string
// one and the boundary is local midnight — the same day the poster says.
func todayISO() string { return time.Now().Format("2006-01-02") }

// handlePublicTournamentList serves every event currently open to the public.
func handlePublicTournamentList(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Query(publicTournamentSelect + `
			WHERE t.public_registration = 1
			  AND t.tournament_status <> 'Completed'
			ORDER BY COALESCE(t.start_date,'9999') ASC, t.name ASC`)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournaments", err)
			return
		}
		defer rows.Close()
		out := []publicTournament{}
		for rows.Next() {
			t, err := scanPublicTournament(rows)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not load tournaments", err)
				return
			}
			out = append(out, *t)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournaments", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"tournaments": out})
	}
}

// handlePublicTournament serves one open event, with its categories.
func handlePublicTournament(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		row := d.QueryRow(publicTournamentSelect+`
			WHERE t.tournament_id = ? AND t.public_registration = 1`, id)
		t, err := scanPublicTournament(row)
		if errors.Is(err, sql.ErrNoRows) {
			// A closed event is indistinguishable from one that does not
			// exist, so this cannot be used to discover tournament ids.
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		cats, err := publicCategories(d, id)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"tournament": t, "categories": cats})
	}
}

type publicCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func publicCategories(d *sql.DB, tournamentID string) ([]publicCategory, error) {
	rows, err := d.Query(`SELECT tournament_category_id, name FROM tournament_category
	                      WHERE tournament_id = ? ORDER BY name`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []publicCategory{}
	for rows.Next() {
		var c publicCategory
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// registerInput is what the public form sends. Unknown fields are rejected by
// httpx.Decode, so a client cannot smuggle in a status or a fee.
type registerInput struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	DateOfBirth string `json:"dateOfBirth"`
	CategoryID  string `json:"categoryId"`
	IsStudent   bool   `json:"isStudent"`
}

// validate checks everything at the boundary and returns a message safe to show
// a stranger — it names the field, never the internals.
func (in *registerInput) validate() string {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = auth.NormalizeEmail(in.Email)
	in.Phone = strings.TrimSpace(in.Phone)
	in.DateOfBirth = strings.TrimSpace(in.DateOfBirth)
	in.CategoryID = strings.TrimSpace(in.CategoryID)

	switch {
	case len([]rune(in.Name)) < 2 || len([]rune(in.Name)) > maxNameLen:
		return "please give the player's full name"
	case in.Email == "" || len(in.Email) > maxEmailLen:
		return "please give an email address"
	case len(in.Phone) > maxPhoneLen:
		return "that phone number is too long"
	}
	if _, err := mail.ParseAddress(in.Email); err != nil {
		return "that email address does not look right"
	}
	if in.DateOfBirth != "" {
		if _, err := time.Parse("2006-01-02", in.DateOfBirth); err != nil {
			return "that date of birth does not look right"
		}
	}
	return ""
}

// handlePublicRegister takes one entry.
//
// The whole write is a single transaction, and the checks that could go stale
// between reading and writing — the deadline, the capacity, whether this email
// already holds a place — are made inside it. Reading the count first and
// inserting afterwards would let two people take the last seat at once, which
// on the day means turning a child away at the door.
func handlePublicRegister(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tournamentID := r.PathValue("id")
		var in registerInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if msg := in.validate(); msg != "" {
			httpx.Error(w, http.StatusBadRequest, msg, nil)
			return
		}

		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		defer tx.Rollback()

		row := tx.QueryRow(publicTournamentSelect+`
			WHERE t.tournament_id = ? AND t.public_registration = 1`, tournamentID)
		t, err := scanPublicTournament(row)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		if !t.Open {
			if t.ClosedReason == "full" {
				httpx.Error(w, http.StatusConflict, "this tournament is full", nil)
			} else {
				httpx.Error(w, http.StatusConflict, "registration for this tournament has closed", nil)
			}
			return
		}

		// A category, when given, has to belong to *this* event — otherwise the
		// form is a way to attach an entry to somebody else's tournament.
		var categoryID any
		if in.CategoryID != "" {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM tournament_category
			                       WHERE tournament_category_id = ? AND tournament_id = ?`,
				in.CategoryID, tournamentID).Scan(&n); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not register", err)
				return
			}
			if n == 0 {
				httpx.Error(w, http.StatusBadRequest, "that category is not part of this tournament", nil)
				return
			}
			categoryID = in.CategoryID
		}

		// The student lookup, whose result never reaches the reply. NULL when
		// no account carries this address, which is the ordinary case.
		var studentID any
		var matched string
		err = tx.QueryRow(`SELECT s.student_id FROM student s
		                   JOIN user_account u ON u.user_account_id = s.user_account_id
		                   WHERE lower(trim(u.email)) = ?`, in.Email).Scan(&matched)
		if err == nil {
			studentID = matched
		} else if !errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}

		// Quoted on the claim, not on the match — see the package comment.
		fee := t.Fee
		if in.IsStudent {
			fee = discounted(t.Fee, t.DiscountPct)
		}

		regID := newID("treg")
		_, err = tx.Exec(`INSERT INTO tournament_registration (
			tournament_registration_id, tournament_id, student_id, participant_name,
			participant_date_of_birth, tournament_category_id, registered_at,
			status, source, contact_email, contact_phone, fee_quoted,
			student_discount_applied
		) VALUES (?,?,?,?,?,?,?,'Pending','Public',?,?,?,?)`,
			regID, tournamentID, studentID, in.Name,
			nullIfEmpty(in.DateOfBirth), categoryID, sqliteNow(),
			in.Email, in.Phone, fee, boolToInt(in.IsStudent))
		if err != nil {
			// The partial unique indexes are the last word on duplicates, and
			// they are reached rather than pre-checked so that two simultaneous
			// submissions cannot both pass a check and both insert.
			if isUniqueViolation(err) {
				httpx.Error(w, http.StatusConflict,
					"that email address is already registered for this tournament", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}

		httpx.JSON(w, http.StatusCreated, map[string]any{
			"registered": true,
			"status":     "Pending",
			"feeQuoted":  fee,
			// Said plainly so nobody turns up on the day assuming a place: the
			// desk still has to confirm it.
			"needsApproval": true,
		})
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether err is SQLite refusing a duplicate.
//
// Matched on the message because the two drivers this product runs on — modernc
// locally, libSQL remotely — return different error types for it, and neither
// is worth importing here just to type-assert.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

func mountPublicRegistration(mux *http.ServeMux, d *sql.DB) {
	const p = "/api/v1/public/tournaments"
	// Reads are cheap and cacheable; the write is the one that costs something,
	// so it carries the tighter budget.
	mux.HandleFunc("GET "+p, httpx.RateLimit(60, handlePublicTournamentList(d)))
	mux.HandleFunc("GET "+p+"/{id}", httpx.RateLimit(60, handlePublicTournament(d)))
	mux.HandleFunc("POST "+p+"/{id}/register", httpx.RateLimit(10, handlePublicRegister(d)))
}
