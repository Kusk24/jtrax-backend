// Asking tournament entrants whether they are coming.
//
// The organiser sets, per tournament, how many days before the start JTrax
// asks (arrival_reminder_days). From that day on, every Approved entry that has
// not been asked yet gets one email with a link to confirm or decline. The
// schedule is read fresh on every run, so changing it moves the reminders not
// yet sent; an entry already asked is not asked again.
//
// The link carries a random code, like the pay link (publicpay.go): only its
// hash is stored, the code goes after the # so it stays out of server logs,
// and the page posts it in a body. The page asks before recording anything, so
// a mail scanner that opens every link cannot answer for the family.
package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
)

// StartArrivalReminders sends due reminders now and then on the entry
// sweeper's schedule, until ctx ends. Started by the server, not by
// NewHandler, so tests decide when it runs.
func StartArrivalReminders(ctx context.Context, d *sql.DB, cfg mail.Config, sender mail.Sender) {
	go func() {
		ticker := time.NewTicker(sweepEvery)
		defer ticker.Stop()
		for {
			if n, err := sendArrivalReminders(d, cfg, sender, today()); err != nil {
				log.Printf("arrival reminders: %v", err)
			} else if n > 0 {
				log.Printf("arrival reminders: asked %d entrants", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

type dueReminder struct {
	regID, tournamentID, tournament, participant, email, studentID, start, venue string
}

// sendArrivalReminders asks every entrant whose reminder is due on `day` and
// returns how many were asked. An entry is due once `day` is on or after the
// start less the configured days, and the tournament has not started yet.
func sendArrivalReminders(d *sql.DB, cfg mail.Config, sender mail.Sender, day string) (int, error) {
	rows, err := d.Query(`
		SELECT r.tournament_registration_id, t.tournament_id, t.name, r.participant_name,
		       r.contact_email, COALESCE(r.student_id, ''), t.start_date, COALESCE(t.venue_name, '')
		  FROM tournament_registration r
		  JOIN tournament t ON t.tournament_id = r.tournament_id
		 WHERE r.status = 'Approved'
		   AND r.arrival_reminded_at IS NULL
		   AND r.arrival_status = 'Pending'
		   AND t.draft = 0
		   AND COALESCE(t.arrival_reminder_days, 0) > 0
		   AND COALESCE(t.start_date, '') <> ''
		   AND t.start_date >= ?
		   AND date(t.start_date, '-' || t.arrival_reminder_days || ' days') <= ?`, day, day)
	if err != nil {
		return 0, err
	}
	var due []dueReminder
	for rows.Next() {
		var r dueReminder
		if err := rows.Scan(&r.regID, &r.tournamentID, &r.tournament, &r.participant,
			&r.email, &r.studentID, &r.start, &r.venue); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, r)
	}
	rows.Close()
	return askFamilies(d, cfg, sender, due, "arrival_reminded_at IS NULL")
}

// askFamilies emails the given entries and returns how many were asked. Each
// entry is claimed first, only if `claim` still holds, so two senders that
// overlap cannot ask the same entry twice.
func askFamilies(d *sql.DB, cfg mail.Config, sender mail.Sender, due []dueReminder, claim string) (int, error) {
	/* One email per family: entries in the same tournament that would go to
	   the same addresses — a parent with two children entered — are asked
	   together, one link answering for each child. In order of first due. */
	type family struct {
		to      []string
		entries []dueReminder
	}
	var families []*family
	byKey := map[string]*family{}
	for _, r := range due {
		to := arrivalRecipients(d, r)
		sorted := make([]string, len(to))
		for i, a := range to {
			sorted[i] = strings.ToLower(strings.TrimSpace(a))
		}
		sort.Strings(sorted)
		key := r.tournamentID + "|" + strings.Join(sorted, ",")
		if len(to) == 0 {
			key += "|" + r.regID // nobody to write to: not grouped
		}
		f := byKey[key]
		if f == nil {
			f = &family{to: to}
			byKey[key] = f
			families = append(families, f)
		}
		f.entries = append(f.entries, r)
	}

	asked := 0
	for _, f := range families {
		var codes []arrivalCode
		var names []string
		for _, r := range f.entries {
			code, hash, err := newPayCode()
			if err != nil {
				return asked, err
			}
			res, err := d.Exec(`UPDATE tournament_registration
			                       SET arrival_reminded_at = ?, arrival_code_hash = ?
			                     WHERE tournament_registration_id = ? AND `+claim,
				time.Now().UTC().Format(time.RFC3339), hash, r.regID)
			if err != nil {
				return asked, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			codes = append(codes, arrivalCode{r.regID, r.participant, code})
			names = append(names, r.participant)
		}
		if len(codes) == 0 {
			continue
		}
		if len(f.to) == 0 {
			log.Printf("arrival reminders: %s has no email to ask", codes[0].regID)
			continue
		}
		first := f.entries[0]
		email := arrivalEmail(cfg.AppURL, first.tournament, fmtDay(first.start), first.venue, codes)
		for _, addr := range f.to {
			if err := mail.Deliver(sender, addr, "Are you coming? "+first.tournament+" / ยืนยันการเข้าร่วม", email); err != nil {
				log.Printf("arrival reminders: %s to %s did not send: %v", codes[0].regID, addr, err)
			}
		}
		asked += len(codes)
	}
	return asked, nil
}

// arrivalCode is one entry's private answer code.
type arrivalCode struct{ regID, participant, code string }

// arrivalFamilyLink is one link answering for every entry given: the first
// entry's page, with each further entry and its code after the #, where the
// page reads them (and the server's logs never see them).
func arrivalFamilyLink(appURL string, codes []arrivalCode) string {
	link := arrivalLink(appURL, codes[0].regID, codes[0].code)
	if len(codes) > 1 {
		also := make([]string, 0, len(codes)-1)
		for _, c := range codes[1:] {
			also = append(also, url.QueryEscape(c.regID)+"."+c.code)
		}
		link += "&also=" + strings.Join(also, ",")
	}
	return link
}

// mailArrival emails one reminder with `code` in its link, and returns how
// many addresses it went to.
func mailArrival(d *sql.DB, cfg mail.Config, sender mail.Sender, r dueReminder, code string) int {
	to := arrivalRecipients(d, r)
	email := arrivalEmail(cfg.AppURL, r.tournament, fmtDay(r.start), r.venue, []arrivalCode{{r.regID, r.participant, code}})
	for _, addr := range to {
		if err := mail.Deliver(sender, addr, "Are you coming? "+r.tournament+" / ยืนยันการเข้าร่วม", email); err != nil {
			log.Printf("arrival reminders: %s to %s did not send: %v", r.regID, addr, err)
		}
	}
	return len(to)
}

// handleResendArrival sends one entrant's reminder again, from the
// Participants table. Only while they have not answered and the tournament
// has not started; the new link replaces the old one.
func handleResendArrival(d *sql.DB, cfg mail.Config, sender mail.Sender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		var due dueReminder
		var answer, status string
		due.regID = r.PathValue("id")
		err := d.QueryRow(`
			SELECT t.tournament_id, t.name, r.participant_name, r.contact_email,
			       COALESCE(r.student_id, ''), COALESCE(t.start_date, ''), COALESCE(t.venue_name, ''),
			       r.arrival_status, r.status
			  FROM tournament_registration r
			  JOIN tournament t ON t.tournament_id = r.tournament_id
			 WHERE r.tournament_registration_id = ?`, due.regID).
			Scan(&due.tournamentID, &due.tournament, &due.participant, &due.email,
				&due.studentID, &due.start, &due.venue, &answer, &status)
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no such entry", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the entry", err)
			return
		}
		if answer != "Pending" {
			httpx.Error(w, http.StatusConflict, "this entrant has already answered", nil)
			return
		}
		if status != "Approved" || (due.start != "" && due.start < today()) {
			httpx.Error(w, http.StatusConflict, "this entry can no longer be asked", nil)
			return
		}
		if len(arrivalRecipients(d, due)) == 0 {
			httpx.Error(w, http.StatusConflict, "this entry has no email to send to", nil)
			return
		}
		code, hash, err := newPayCode()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not send", err)
			return
		}
		sentAt := time.Now().UTC().Format(time.RFC3339)
		if _, err := d.Exec(`UPDATE tournament_registration
		                        SET arrival_reminded_at = ?, arrival_code_hash = ?
		                      WHERE tournament_registration_id = ?`, sentAt, hash, due.regID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not send", err)
			return
		}
		sent := mailArrival(d, cfg, sender, due, code)
		httpx.JSON(w, http.StatusOK, map[string]any{"sent": sent, "arrival_reminded_at": sentAt})
	}
}

// handleBatchArrival asks the chosen entrants of one tournament in one go,
// one email per family. Entries that have answered, are not approved, or
// whose tournament has started are skipped rather than refused.
func handleBatchArrival(d *sql.DB, cfg mail.Config, sender mail.Sender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		var body struct {
			IDs []string `json:"registration_ids"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.IDs) == 0 {
			httpx.Error(w, http.StatusBadRequest, "choose who to ask", nil)
			return
		}
		if len(body.IDs) > 1000 {
			httpx.Error(w, http.StatusBadRequest, "too many entries at once", nil)
			return
		}
		args := []any{r.PathValue("id"), today()}
		marks := make([]string, len(body.IDs))
		for i, id := range body.IDs {
			marks[i] = "?"
			args = append(args, id)
		}
		rows, err := d.Query(`
			SELECT r.tournament_registration_id, t.tournament_id, t.name, r.participant_name,
			       r.contact_email, COALESCE(r.student_id, ''), COALESCE(t.start_date, ''), COALESCE(t.venue_name, '')
			  FROM tournament_registration r
			  JOIN tournament t ON t.tournament_id = r.tournament_id
			 WHERE t.tournament_id = ?
			   AND r.status = 'Approved'
			   AND r.arrival_status = 'Pending'
			   AND (COALESCE(t.start_date, '') = '' OR t.start_date >= ?)
			   AND r.tournament_registration_id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the entries", err)
			return
		}
		var due []dueReminder
		for rows.Next() {
			var e dueReminder
			if err := rows.Scan(&e.regID, &e.tournamentID, &e.tournament, &e.participant,
				&e.email, &e.studentID, &e.start, &e.venue); err != nil {
				rows.Close()
				httpx.Error(w, http.StatusInternalServerError, "could not read the entries", err)
				return
			}
			due = append(due, e)
		}
		rows.Close()
		/* After the rows are closed: the lookup needs the connection. */
		reachable := due[:0]
		for _, e := range due {
			if len(arrivalRecipients(d, e)) > 0 {
				reachable = append(reachable, e)
			}
		}
		due = reachable
		asked, err := askFamilies(d, cfg, sender, due, "arrival_status = 'Pending'")
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not send", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"asked": asked, "skipped": len(body.IDs) - asked})
	}
}

// arrivalRecipients is who to ask: the email on the entry, or else the
// guardians of the student it is for.
func arrivalRecipients(d *sql.DB, r dueReminder) []string {
	if strings.Contains(r.email, "@") {
		return []string{r.email}
	}
	if r.studentID == "" {
		return nil
	}
	rows, err := d.Query(`
		SELECT DISTINCT ua.email
		  FROM student_parent sp
		  JOIN parent p ON p.parent_id = sp.parent_id
		  JOIN user_account ua ON ua.user_account_id = p.user_account_id
		 WHERE sp.student_id = ? AND ua.email LIKE '%@%'`, r.studentID)
	if err != nil {
		log.Printf("arrival reminders: guardians of %s: %v", r.studentID, err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if rows.Scan(&e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// arrivalLink is the web app's confirm page, the code after the #.
func arrivalLink(appURL, regID, code string) string {
	return strings.TrimSuffix(appURL, "/") + "/arrival/" + url.PathEscape(regID) + "#code=" + code
}

// arrivalEmail asks a family whether each of their children is coming: one
// row per child with Attending and Not attending buttons. A button opens the
// confirm page with that answer chosen; the page records it only when the
// parent taps Confirm, so a mail scanner that opens every link cannot answer
// for them — and, as Not attending can cost a child their place, must not.
func arrivalEmail(appURL, tournament, start, venue string, codes []arrivalCode) mail.Email {
	family := arrivalFamilyLink(appURL, codes)
	names := make([]string, len(codes))
	choices := make([]mail.Choice, len(codes))
	for i, c := range codes {
		names[i] = c.participant
		pick := func(answer string) string {
			return family + "&pick=" + url.QueryEscape(c.regID) + "." + answer
		}
		choices[i] = mail.Choice{
			Label: c.participant,
			Buttons: []mail.Button{
				{Label: "✓ Attending", URL: pick("Confirmed")},
				{Label: "✗ Not attending", URL: pick("NotAttending")},
			},
		}
	}
	who := joinNames(names)
	where, whereTH := "", ""
	if venue != "" {
		where, whereTH = " at "+venue, " ที่ "+venue
	}
	return mail.Email{
		Heading:  "Are you coming?",
		Greeting: "Hello,",
		Paragraphs: []string{
			tournament + " starts on " + start + where + ".",
			"Please tell us whether " + who + " will attend.",
		},
		Choices: choices,
		After: []string{
			"Tap an answer, then Confirm on the page that opens. You can change it until the tournament starts.",
			tournament + " จะเริ่มวันที่ " + start + whereTH + " กรุณายืนยันว่า " + who + " จะเข้าร่วมหรือไม่ โดยกดปุ่มด้านบน แล้วกดยืนยันในหน้าที่เปิดขึ้น",
		},
		Signoff: []string{"Best regards,", "JCA Chess School", "JTrax Parent Portal"},
	}
}

// arrivalEntry is what the confirm page shows.
type arrivalEntry struct {
	TournamentName  string `json:"tournamentName"`
	ParticipantName string `json:"participantName"`
	StartDate       string `json:"startDate"`
	Venue           string `json:"venue,omitempty"`
	Status          string `json:"status"`
	// Open is false once the tournament has started or the entry is no
	// longer in it: the answer can then no longer change.
	Open bool `json:"open"`
}

var errNoArrival = errors.New("no such reminder")

func findArrival(d *sql.DB, regID, code string) (*arrivalEntry, error) {
	if len(code) != 64 {
		return nil, errNoArrival
	}
	var e arrivalEntry
	var stored, regStatus string
	err := d.QueryRow(`
		SELECT t.name, r.participant_name, COALESCE(t.start_date, ''), COALESCE(t.venue_name, ''),
		       r.arrival_status, r.status, COALESCE(r.arrival_code_hash, '')
		  FROM tournament_registration r
		  JOIN tournament t ON t.tournament_id = r.tournament_id
		 WHERE r.tournament_registration_id = ?`, regID).
		Scan(&e.TournamentName, &e.ParticipantName, &e.StartDate, &e.Venue, &e.Status, &regStatus, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoArrival
	}
	if err != nil {
		return nil, err
	}
	if stored == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(hashPayCode(code))) != 1 {
		return nil, errNoArrival
	}
	e.Open = regStatus == "Approved" && (e.StartDate == "" || e.StartDate >= today())
	return &e, nil
}

// handleArrival shows the holder of a reminder link what they are answering.
func handleArrival(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, ok := readCode(w, r)
		if !ok {
			return
		}
		e, err := findArrival(d, r.PathValue("id"), code)
		if errors.Is(err, errNoArrival) {
			httpx.Error(w, http.StatusNotFound, "this link does not work", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the entry", err)
			return
		}
		httpx.JSON(w, http.StatusOK, e)
	}
}

// handleArrivalAnswer records Confirmed or NotAttending. Answering again
// changes the answer, until the tournament starts.
func handleArrivalAnswer(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Code   string `json:"code"`
			Answer string `json:"answer"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", nil)
			return
		}
		if in.Answer != "Confirmed" && in.Answer != "NotAttending" {
			httpx.Error(w, http.StatusBadRequest, "answer must be Confirmed or NotAttending", nil)
			return
		}
		regID := r.PathValue("id")
		e, err := findArrival(d, regID, strings.TrimSpace(in.Code))
		if errors.Is(err, errNoArrival) {
			httpx.Error(w, http.StatusNotFound, "this link does not work", nil)
			return
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not read the entry", err)
			return
		}
		if !e.Open {
			httpx.Error(w, http.StatusConflict, "this tournament can no longer be answered", nil)
			return
		}
		if _, err := d.Exec(`UPDATE tournament_registration
		                        SET arrival_status = ?, arrival_answered_at = ?
		                      WHERE tournament_registration_id = ?`,
			in.Answer, time.Now().UTC().Format(time.RFC3339), regID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not save the answer", err)
			return
		}
		e.Status = in.Answer
		httpx.JSON(w, http.StatusOK, e)
	}
}

func mountArrival(mux *http.ServeMux, d *sql.DB, cfg mail.Config, sender mail.Sender) {
	const a = "/api/v1/public/arrival/{id}"
	mux.HandleFunc("POST "+a, httpx.RateLimit(30, handleArrival(d)))
	mux.HandleFunc("POST "+a+"/answer", httpx.RateLimit(20, handleArrivalAnswer(d)))
	mux.HandleFunc("POST /api/v1/tournament-registrations/{id}/arrival-reminder",
		httpx.RateLimit(30, handleResendArrival(d, cfg, sender)))
	mux.HandleFunc("POST /api/v1/tournaments/{id}/arrival-reminders",
		httpx.RateLimit(10, handleBatchArrival(d, cfg, sender)))
}

// SendArrivalReminders runs one reminder pass as of `day`. Exported for tests,
// which decide when the timer would have fired.
func SendArrivalReminders(d *sql.DB, cfg mail.Config, sender mail.Sender, day string) (int, error) {
	return sendArrivalReminders(d, cfg, sender, day)
}
