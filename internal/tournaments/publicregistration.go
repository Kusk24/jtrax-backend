// Public tournament listings, registration forms and route declarations.
package tournaments

import (
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/families"
	"github.com/Kusk24/jtrax-backend/internal/fees"
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

// The entry form is text only — the ID card is read by the scan, not sent
// with the entry — so this bounds a few fields.
const maxEntryFormBytes = 64 << 10

// publicTournamentSelect is the shape of an open event as the public sees it.
// Deliberately narrow: no organiser contact, no internal ids beyond the one
// needed to register, nothing about who else has signed up beyond a count.
const publicTournamentSelect = `
	SELECT t.tournament_id, t.name, t.tournament_status,
	       COALESCE(t.start_date,''), COALESCE(t.end_date,''),
	       COALESCE(t.venue_name,''), COALESCE(t.venue_address,''), COALESCE(t.venue_map_url,''),
	       COALESCE(t.registration_deadline,''),
	       COALESCE(t.regular_fee, t.early_bird_fee, 0),
	       COALESCE(t.early_bird_fee, 0), COALESCE(t.early_bird_deadline, ''),
	       EXISTS(SELECT 1 FROM tournament_regulation g WHERE g.tournament_id = t.tournament_id),
	       EXISTS(SELECT 1 FROM tournament_banner b WHERE b.tournament_id = t.tournament_id),
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
	VenueMapURL  string `json:"venueMapUrl,omitempty"`
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
	// An uploaded banner. Without one the page draws its own from the name,
	// the date and the venue.
	HasBanner    bool    `json:"hasBanner"`
	StudentFee   float64 `json:"studentFee"`
	DiscountPct  int     `json:"studentDiscountPct"`
	Capacity     *int    `json:"capacity"`
	price        fees.TournamentPrice
	Taken        int    `json:"taken"`
	SpotsLeft    *int   `json:"spotsLeft"`
	Open         bool   `json:"open"`
	ClosedReason string `json:"closedReason,omitempty"`
	/* Whether "Pay & Register" can send the family to Stripe. Without it the
	   page registers and says to pay at the desk. */
	CardPayments bool `json:"cardPayments"`
}

// scanPublicTournament reads one row of publicTournamentSelect and works out
// whether it is still taking entries.
func scanPublicTournament(sc interface{ Scan(...any) error }) (*publicTournament, error) {
	var t publicTournament
	var capacity sql.NullInt64
	if err := sc.Scan(&t.ID, &t.Name, &t.Status, &t.StartDate, &t.EndDate,
		&t.VenueName, &t.VenueAddress, &t.VenueMapURL, &t.Deadline, &t.RegularFee,
		&t.EarlyBirdFee, &t.EarlyBirdUntil, &t.HasRegulation, &t.HasBanner,
		&t.DiscountPct, &t.price.StudentDiscount, &t.price.StudentEarlyBird,
		&capacity, &t.Taken); err != nil {
		return nil, err
	}
	t.price.Regular, t.price.EarlyBird = t.RegularFee, t.EarlyBirdFee
	t.price.EarlyBirdUntil, t.price.DiscountPct = t.EarlyBirdUntil, t.DiscountPct
	day := academytime.Today()
	t.EarlyBirdActive = t.price.EarlyBirdOpen(day)
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
	if deadline != "" && academytime.Today() > deadline {
		return false, "deadline"
	}
	if capacity != nil && taken >= *capacity {
		return false, "full"
	}
	return true, ""
}

// handlePublicTournamentList serves every event currently open to the public.
func handlePublicTournamentList(d *sql.DB) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		rows, err := d.Query(publicTournamentSelect + `
			WHERE t.public_registration = 1 AND t.draft = 0
			  AND t.tournament_status <> 'Completed'
			ORDER BY COALESCE(t.start_date,'9999') ASC, t.name ASC`)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournaments", err)
			return
		}
		defer rows.Close()
		out := []publicTournament{}
		for rows.Next() {
			t, err := scanPublicTournament(rows)
			if err != nil {
				httpx.Error(ctx, http.StatusInternalServerError, "could not load tournaments", err)
				return
			}
			out = append(out, *t)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournaments", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"tournaments": out})
	}
}

// handlePublicTournament serves one open event, with its categories.
func handlePublicTournament(d *sql.DB, cardPayments bool) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := ctx.Param("id")
		row := d.QueryRow(publicTournamentSelect+`
			WHERE t.tournament_id = ? AND t.public_registration = 1 AND t.draft = 0`, id)
		t, err := scanPublicTournament(row)
		if errors.Is(err, sql.ErrNoRows) {
			// A closed event is indistinguishable from one that does not
			// exist, so this cannot be used to discover tournament ids.
			httpx.Error(ctx, http.StatusNotFound, "not found", nil)
			return
		}
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		cats, err := publicCategories(d, id)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not load tournament", err)
			return
		}
		t.CardPayments = cardPayments
		httpx.JSON(ctx, http.StatusOK, map[string]any{"tournament": t, "categories": cats})
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
	/* The privacy notice (PDPA s.23): the person filling in the form — the
	   player's parent or guardian, or the player if 20 or over — says they
	   have read it. Required, and recorded with the notice's version. */
	PrivacyNotice bool `json:"privacyNotice"`

	/* The player's name in Thai script, as a Thai ID card prints it. Optional:
	   a player entering on a passport has none. */
	NameTh string `json:"nameTh"`
	// "thai-id" or "passport" — which document the family said they hold.
	DocumentType string `json:"documentType"`
	/* What the ID card scan read, kept beside what was finally submitted so
	   staff can check an age group against the document. Empty when the
	   family did not scan. */
	ScannedName        string `json:"scannedName"`
	ScannedDateOfBirth string `json:"scannedDateOfBirth"`
	/* The ID card check the scan returned (idcheck.go). Required: the date
	   of birth, and so the age group, comes from it. */
	IDCheck string `json:"idCheck"`

	/* "now" sends the family straight to Stripe and holds the email until
	   the payment either settles or does not; "later" emails the pay link at
	   once. Empty is "later", which is what every form before this one did. */
	PayChoice string `json:"payChoice"`
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
	in.NameTh = strings.TrimSpace(in.NameTh)
	in.ScannedName = strings.TrimSpace(in.ScannedName)
	in.ScannedDateOfBirth = strings.TrimSpace(in.ScannedDateOfBirth)

	switch {
	case len([]rune(in.Name)) < 2 || len([]rune(in.Name)) > maxNameLen:
		return "please give the player's full name"
	case in.Email == "" || len(in.Email) > maxEmailLen:
		return "please give an email address"
	case in.Phone == "":
		return "please give a contact phone number"
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
		if families.DobTooYoung(in.DateOfBirth) {
			return families.ErrDOBTooYoung.Error()
		}
	}
	if in.IsStudent && (in.StudentID == "" || len(in.StudentID) > 40) {
		return "please give the JCA student ID so we can apply the discount"
	}
	if len([]rune(in.Nickname)) > maxNameLen {
		return "that nickname is too long"
	}
	if len([]rune(in.NameTh)) > maxNameLen || len([]rune(in.ScannedName)) > maxNameLen {
		return "that name is too long"
	}
	switch in.DocumentType {
	case "", "thai-id", "passport":
	default:
		return "that document type is not one we know"
	}
	switch in.PayChoice {
	case "", "now", "later":
	default:
		return "please choose whether to pay now or later"
	}
	if in.ScannedDateOfBirth != "" {
		if _, err := time.Parse("2006-01-02", in.ScannedDateOfBirth); err != nil {
			in.ScannedDateOfBirth = "" // a misread is not the entrant's mistake
		}
	}
	/* Refused rather than defaulted. An entry recorded as having accepted
	   terms nobody ticked is worse than no record at all — it is a false one,
	   and the record only has value if it can only mean yes. */
	if !in.AcceptTerms {
		return "please accept the terms and conditions to enter"
	}
	if !in.PrivacyNotice {
		return "please confirm you have read the privacy notice"
	}
	/* 0 is "not given", which is allowed — the age limit below is enforced on
	   the date of birth. A negative or implausible age is a typo worth
	   catching here, where the message can say so. */
	if in.Age < 0 || in.Age > 120 {
		return "that age does not look right"
	}
	return ""
}

// categoryAgeLimit reads the age out of a category's name — "U8 Boys",
// "U08" and "Under 8" all mean under 8 — so the age rule lives in the name the
// organiser already wrote rather than in a column nobody fills. 0 means the
// name carries no age.
var categoryAgePattern = regexp.MustCompile(`(?i)\bU(?:nder)?[\s-]?(\d{1,2})\b`)

func categoryAgeLimit(name string) int {
	m := categoryAgePattern.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// bornInTime reports whether somebody born on dob may play an under-`limit`
// category at an event held in `year`: born in year-limit or later. "U10" in
// 2026 is born on or after 1 January 2016.
func bornInTime(dob time.Time, year, limit int) bool {
	return dob.Year() >= year-limit
}

// handlePublicRegister accepts the public form and returns the receipt used by the payment screen.
func handlePublicRegister(deps *publicEntryDeps) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		in, msg := readPublicEntryForm(ctx)
		if msg != "" {
			httpx.Error(ctx, http.StatusBadRequest, msg, nil)
			return
		}
		entry, err := registerPublicEntry(deps.db, ctx.Param("id"), in, deps.stripe != nil)
		if err != nil {
			writeRegistrationError(ctx, err)
			return
		}
		deps.openPaymentAndNotify(entry, in.Name)
		httpx.JSON(ctx, http.StatusCreated, map[string]any{
			"registered": true,
			"status":     "Approved",
			"feeQuoted":  entry.fee,
			// What the done screen needs to offer "Pay now" straight away. The
			// code is shown to this caller once and never again; the email
			// carries the same one.
			"registrationId": entry.id,
			"payCode":        entry.payCode,
			"cardPayments":   deps.stripe != nil && entry.fee > 0,
			"payChoice":      entry.payChoice,
			// Only what has gone: a "pay now" entry is emailed once the
			// payment settles or fails, not here.
			"emailed": deps.sender != nil && entry.payChoice != "now",
			// Kept, and false, rather than dropped: a portal still running the
			// previous build reads this to decide whether to say "we will
			// confirm your place". Removing the key would leave it undefined,
			// which is falsey by accident rather than on purpose.
			"needsApproval": false,
		})
	}
}

func readPublicEntryForm(ctx *gin.Context) (registerInput, string) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxEntryFormBytes)
	if err := ctx.Request.ParseMultipartForm(maxEntryFormBytes); err != nil {
		return registerInput{}, "could not read the form — please try again"
	}
	// age arrives as a form string like every other field here; an empty
	// or unparsable one is 0, "not given", which is exactly what the old
	// JSON decode produced for an absent field.
	age, _ := strconv.Atoi(ctx.Request.FormValue("age"))
	in := registerInput{
		Name:               ctx.Request.FormValue("name"),
		Email:              ctx.Request.FormValue("email"),
		Phone:              ctx.Request.FormValue("phone"),
		DateOfBirth:        ctx.Request.FormValue("dateOfBirth"),
		CategoryID:         ctx.Request.FormValue("categoryId"),
		IsStudent:          ctx.Request.FormValue("isStudent") == "true",
		StudentID:          ctx.Request.FormValue("studentId"),
		Nickname:           ctx.Request.FormValue("nickname"),
		Age:                age,
		AcceptTerms:        ctx.Request.FormValue("acceptTerms") == "true",
		PrivacyNotice:      ctx.Request.FormValue("privacyNotice") == "true",
		NameTh:             ctx.Request.FormValue("nameTh"),
		DocumentType:       ctx.Request.FormValue("documentType"),
		ScannedName:        ctx.Request.FormValue("scannedName"),
		ScannedDateOfBirth: ctx.Request.FormValue("scannedDateOfBirth"),
		IDCheck:            ctx.Request.FormValue("idCheck"),
		PayChoice:          ctx.Request.FormValue("payChoice"),
	}
	msg := in.validate()
	return in, msg
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mountPublicRegistration(mux gin.IRoutes, deps *publicEntryDeps) {
	d := deps.db
	const p = "/api/v1/public/tournaments"
	// Reads are cheap and cacheable; the write is the one that costs something,
	// so it carries the tighter budget.
	mux.GET(p, httpx.RateLimit(60), handlePublicTournamentList(d))
	mux.GET(p+"/:id", httpx.RateLimit(60), handlePublicTournament(d, deps.stripe != nil))
	mux.POST(p+"/:id/register", httpx.RateLimit(10), handlePublicRegister(deps))

	// The pay link's two calls. The code is the whole of the authorization,
	// and it is 256 bits, so the limit is a flood guard rather than what
	// stops guessing. Paying spends a Stripe API call, so it gets less.
	const e = "/api/v1/public/tournament-registrations/:id"
	mux.POST(e, httpx.RateLimit(30), handlePublicEntry(deps))
	mux.POST(e+"/pay", httpx.RateLimit(20), handlePublicEntryPay(deps))
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
