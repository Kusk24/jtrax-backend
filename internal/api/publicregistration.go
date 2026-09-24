// Public tournament registration: the three endpoints a stranger may call.
//
// Everything here is unauthenticated, which makes it the widest door in the
// product and the only place where a row is created by somebody the academy has
// never met. Three things hold that door:
//
//   - a tournament is closed until an organiser opens it (public_registration),
//     exactly like results_public;
//   - the deadline and the capacity are enforced inside the write transaction,
//     not read beforehand and hoped about;
//   - it is rate-limited, and one email may hold one live place per event.
//
// # The fourth used to be approval, and is gone on purpose
//
// A submission used to land as Pending and wait for a member of staff, so
// nothing a stranger typed became a participant on its own. The academy takes
// every entry, so that queue was a step that only ever ended one way — and an
// unworked queue is worse than none, because a place nobody has confirmed is
// indistinguishable from a place nobody has looked at.
//
// What it cost is real and worth naming: a stranger's submission is now a
// participant immediately. What replaces it is validation at the door rather
// than judgement behind it — the category must belong to this event, the age
// rule in the category's name is enforced against the given date of birth, and
// a claimed student discount must name a student that exists. The desk's
// remaining power is to correct a fee or withdraw an entry, not to admit one.
//
// # Why the discount is claimed rather than detected
//
// A registrant ticks "I am a JCA student" and is quoted the discounted fee on
// that claim alone. The server does look their email up against the academy's
// own records, but it never says so in the reply: if the discount appeared only
// for addresses that matched, this endpoint would be a way to test whether a
// given child is a pupil here, one guess at a time. The match is surfaced only
// in the staff roster, where "claimed, and we found a matching student" versus
// "claimed, no match" is a thing the desk can act on afterwards.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
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
	       COALESCE(t.early_bird_fee, 0), COALESCE(t.early_bird_deadline, ''),
	       EXISTS(SELECT 1 FROM tournament_regulation g WHERE g.tournament_id = t.tournament_id),
	       t.student_discount_pct, t.student_gets_discount, t.student_gets_early_bird,
	       t.max_participants,
	       (SELECT COUNT(*) FROM tournament_registration r
	         WHERE r.tournament_id = t.tournament_id
	           AND r.status IN ('Pending','Approved'))
	FROM tournament t`

type publicTournament struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	StartDate    string `json:"startDate"`
	EndDate      string `json:"endDate"`
	VenueName    string `json:"venueName"`
	VenueAddress string `json:"venueAddress"`
	Deadline     string `json:"registrationDeadline"`
	/* Fee is what an outside participant pays if they register right now —
	   the early-bird price while its window is open, the regular price after.
	   Students pay StudentFee, by whichever reductions the organiser chose
	   for this event (see pricing.go). */
	Fee             float64 `json:"fee"`
	RegularFee      float64 `json:"regularFee"`
	EarlyBirdFee    float64 `json:"earlyBirdFee,omitempty"`
	EarlyBirdUntil  string  `json:"earlyBirdUntil,omitempty"`
	EarlyBirdActive bool    `json:"earlyBirdActive"`
	HasRegulation   bool    `json:"hasRegulation"`
	StudentFee      float64 `json:"studentFee"`
	DiscountPct     int     `json:"studentDiscountPct"`
	Capacity        *int    `json:"capacity"`
	price           tournamentPrice
	Taken           int    `json:"taken"`
	SpotsLeft       *int   `json:"spotsLeft"`
	Open            bool   `json:"open"`
	ClosedReason    string `json:"closedReason,omitempty"`
}

// scanPublicTournament reads one row of publicTournamentSelect and works out
// whether it is still taking entries.
func scanPublicTournament(sc interface{ Scan(...any) error }) (*publicTournament, error) {
	var t publicTournament
	var capacity sql.NullInt64
	if err := sc.Scan(&t.ID, &t.Name, &t.Status, &t.StartDate, &t.EndDate,
		&t.VenueName, &t.VenueAddress, &t.Deadline, &t.RegularFee,
		&t.EarlyBirdFee, &t.EarlyBirdUntil, &t.HasRegulation,
		&t.DiscountPct, &t.price.StudentDiscount, &t.price.StudentEarlyBird,
		&capacity, &t.Taken); err != nil {
		return nil, err
	}
	t.price.Regular, t.price.EarlyBird = t.RegularFee, t.EarlyBirdFee
	t.price.EarlyBirdUntil, t.price.DiscountPct = t.EarlyBirdUntil, t.DiscountPct
	day := today()
	t.EarlyBirdActive = t.price.earlyBirdOpen(day)
	t.Fee = t.price.OutsiderFee(day)
	if capacity.Valid {
		n := int(capacity.Int64)
		t.Capacity = &n
		left := n - t.Taken
		if left < 0 {
			left = 0
		}
		t.SpotsLeft = &left
	}
	t.StudentFee = t.price.StudentFee(day)
	t.Open, t.ClosedReason = registrationOpen(t.Deadline, t.Capacity, t.Taken)
	return &t, nil
}

// registrationOpen reports whether entries are still being taken, and if not,
// which of the two reasons applies. The reason is shown to the public, so it is
// a fact about the event rather than about anybody who registered.
func registrationOpen(deadline string, capacity *int, taken int) (bool, string) {
	if deadline != "" && today() > deadline {
		return false, "deadline"
	}
	if capacity != nil && taken >= *capacity {
		return false, "full"
	}
	return true, ""
}

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
	/* Required when IsStudent: the discount is only quoted against a student
	   id the academy can actually find. */
	StudentID string `json:"studentId"`

	/* Called across the hall and printed on the pairing card. Every Thai
	   junior event uses one. */
	Nickname string `json:"nickname"`
	/* The form asks for an age, not a date of birth, so it is stored as given
	   rather than derived. The date of birth is what the ID card says; the
	   interesting case for an age-limited group is when the two disagree. */
	Age int `json:"age"`
	/* The five numbered conditions on the entry form — no refunds, the
	   organiser may change the schedule, the organiser is not liable. Recorded
	   rather than assumed: "did this person agree not to be refunded" is
	   exactly the question somebody asks three weeks later. */
	AcceptTerms bool `json:"acceptTerms"`
}

// validate checks everything at the boundary and returns a message safe to show
// a stranger — it names the field, never the internals.
func (in *registerInput) validate() string {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = auth.NormalizeEmail(in.Email)
	in.Phone = strings.TrimSpace(in.Phone)
	in.DateOfBirth = strings.TrimSpace(in.DateOfBirth)
	in.CategoryID = strings.TrimSpace(in.CategoryID)
	in.StudentID = strings.TrimSpace(in.StudentID)
	in.Nickname = strings.TrimSpace(in.Nickname)

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
	if in.IsStudent && (in.StudentID == "" || len(in.StudentID) > 40) {
		return "please give the JCA student ID so we can apply the discount"
	}
	if len([]rune(in.Nickname)) > maxNameLen {
		return "that nickname is too long"
	}
	/* Refused rather than defaulted. An entry recorded as having accepted
	   terms nobody ticked is worse than no record at all — it is a false one,
	   and the record only has value if it can only mean yes. */
	if !in.AcceptTerms {
		return "please accept the terms and conditions to enter"
	}
	/* 0 is "not given", which is allowed — the age limit below is enforced on
	   the date of birth. A negative or implausible age is a typo worth
	   catching here, where the message can say so. */
	if in.Age < 0 || in.Age > 120 {
		return "that age does not look right"
	}
	return ""
}

// categoryAgeLimit reads the age out of a category's name — "U8 Boys" means
// under 8 — so the age rule lives in the name the organiser already wrote
// rather than in a column nobody fills. 0 means the name carries no age.
var categoryAgePattern = regexp.MustCompile(`(?i)\bU\s?(\d{1,2})\b`)

func categoryAgeLimit(name string) int {
	m := categoryAgePattern.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// ageOn is completed years on a date — the tournament's start day, because
// that is the day the age matters.
func ageOn(dob, on time.Time) int {
	years := on.Year() - dob.Year()
	if on.YearDay() < dob.YearDay() {
		years--
	}
	return years
}

// handlePublicRegister takes one entry.
//
// The whole write is a single transaction, and the checks that could go stale
// between reading and writing — the deadline, the capacity, whether this email
// already holds a place — are made inside it. Reading the count first and
// inserting afterwards would let two people take the last seat at once, which
// on the day means turning a child away at the door.
func handlePublicRegister(deps *publicEntryDeps) http.HandlerFunc {
	d := deps.db
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
		var catName string
		if in.CategoryID != "" {
			err := tx.QueryRow(`SELECT name FROM tournament_category
			                    WHERE tournament_category_id = ? AND tournament_id = ?`,
				in.CategoryID, tournamentID).Scan(&catName)
			if errors.Is(err, sql.ErrNoRows) {
				httpx.Error(w, http.StatusBadRequest, "that category is not part of this tournament", nil)
				return
			}
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not register", err)
				return
			}
			// The age rule lives in the category's name: "U8" is under 8 on
			// the tournament's start day. The page disables ineligible
			// categories, but the page is a courtesy — this is the rule.
			if limit := categoryAgeLimit(catName); limit > 0 {
				/* The date of birth is the rule, because it is what an ID card
				   proves and an age is what somebody typed. A claimed age is
				   accepted only when there is no date of birth at all — which
				   is the entrant who skipped the card scan, and is still
				   better than refusing an entry we could check. */
				if in.DateOfBirth == "" {
					if in.Age == 0 {
						httpx.Error(w, http.StatusBadRequest, "this category has an age limit — please give the player's date of birth or age", nil)
						return
					}
					if in.Age >= limit {
						httpx.Error(w, http.StatusBadRequest, "the player is too old for this category — please pick another", nil)
						return
					}
				} else {
					dob, _ := time.Parse("2006-01-02", in.DateOfBirth)
					/* The academy's day, not the server's: a tournament in
					   Bangkok starts on the date the poster says, and a UTC
					   clock turns that over seven hours early. */
					on := academytime.Now()
					if t.StartDate != "" {
						if parsed, err := time.Parse("2006-01-02", t.StartDate); err == nil {
							on = parsed
						}
					}
					if ageOn(dob, on) >= limit {
						httpx.Error(w, http.StatusBadRequest, "the player is too old for this category — please pick another", nil)
						return
					}
				}
			}
			categoryID = in.CategoryID
		}

		// The student link. A claimed discount now has to survive a lookup:
		// the given student ID must name a real student, or the registration
		// is refused with something the parent can act on. Without a claim,
		// the old quiet email match still ties the entry to a student for
		// staff, and never changes the price.
		var studentID any
		if in.IsStudent {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM student WHERE student_id = ?`,
				in.StudentID).Scan(&n); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not register", err)
				return
			}
			if n == 0 {
				httpx.Error(w, http.StatusBadRequest,
					"we could not find that JCA student ID — check it, or untick the student box", nil)
				return
			}
			studentID = in.StudentID
		} else {
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
		}

		// A verified student pays by whichever reductions the organiser
		// chose; an outsider pays the early-bird price while its window is
		// open and the regular price after. Both come from pricing.go, the
		// same rule the parent portal is charged by.
		fee := t.Fee
		if in.IsStudent {
			fee = t.StudentFee
		}

		regID := newID("treg")
		// The entrant's right to pay for this entry later, from the email.
		// Only the hash is stored; the code itself goes back once, below.
		payCode, payCodeHash, err := newPayCode()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not register", err)
			return
		}
		// fee_charged is set here because approving used to set it, and there
		// is no approving any more. A quote that never becomes a charge would
		// leave every public entry owing nothing on the desk's own roster —
		// the two columns still differ in meaning, and staff may still correct
		// fee_charged afterwards, but its starting value is what we quoted.
		_, err = tx.Exec(`INSERT INTO tournament_registration (
			tournament_registration_id, tournament_id, student_id, participant_name,
			participant_date_of_birth, tournament_category_id, registered_at,
			status, source, contact_email, contact_phone, fee_quoted, fee_charged,
			student_discount_applied, nickname, participant_age, terms_accepted_at,
			pay_code_hash
		) VALUES (?,?,?,?,?,?,?,'Approved','Public',?,?,?,?,?,?,?,?,?)`,
			regID, tournamentID, studentID, in.Name,
			nullIfEmpty(in.DateOfBirth), categoryID, sqliteNow(),
			in.Email, in.Phone, fee, fee, boolToInt(in.IsStudent),
			in.Nickname, nullIfZero(in.Age),
			/* Stamped here rather than taken from the request: the time the
			   terms were accepted is the server's fact, and a client-supplied
			   timestamp on a consent record is worth nothing. validate()
			   refuses the entry unless AcceptTerms is true, so reaching this
			   line is what "accepted" means. */
			sqliteNow(), payCodeHash)
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

		// Off the request, so a slow mail server does not hold the entrant at a
		// spinner after their place is already theirs.
		go sendEntryConfirmation(deps, in.Email, tournamentID, t.Name, regID, in.Name, catName, fee, payCode)

		httpx.JSON(w, http.StatusCreated, map[string]any{
			"registered": true,
			"status":     "Approved",
			"feeQuoted":  fee,
			// What the done screen needs to offer "Pay now" straight away. The
			// code is shown to this caller once and never again; the email
			// carries the same one.
			"registrationId": regID,
			"payCode":        payCode,
			"cardPayments":   deps.stripe != nil && fee > 0,
			"emailed":        deps.sender != nil,
			// Kept, and false, rather than dropped: a portal still running the
			// previous build reads this to decide whether to say "we will
			// confirm your place". Removing the key would leave it undefined,
			// which is falsey by accident rather than on purpose.
			"needsApproval": false,
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

func mountPublicRegistration(mux *http.ServeMux, deps *publicEntryDeps) {
	d := deps.db
	const p = "/api/v1/public/tournaments"
	// Reads are cheap and cacheable; the write is the one that costs something,
	// so it carries the tighter budget.
	mux.HandleFunc("GET "+p, httpx.RateLimit(60, handlePublicTournamentList(d)))
	mux.HandleFunc("GET "+p+"/{id}", httpx.RateLimit(60, handlePublicTournament(d)))
	mux.HandleFunc("POST "+p+"/{id}/register", httpx.RateLimit(10, handlePublicRegister(deps)))

	// The pay link's two calls. The code is the whole of the authorization,
	// and it is 256 bits, so the limit is a flood guard rather than what
	// stops guessing. Paying spends a Stripe API call, so it gets less.
	const e = "/api/v1/public/tournament-registrations/{id}"
	mux.HandleFunc("POST "+e, httpx.RateLimit(30, handlePublicEntry(deps)))
	mux.HandleFunc("POST "+e+"/pay", httpx.RateLimit(20, handlePublicEntryPay(deps)))
}

// nullIfZero keeps "not given" out of the database as NULL rather than 0.
//
// An age of 0 would be a claim about a newborn; the column has to be able to
// say nothing at all, because the form allows an entrant to give a date of
// birth instead.
func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
