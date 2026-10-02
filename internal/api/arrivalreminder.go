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
	"errors"
	"log"
	"net/http"
	"net/url"
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

	asked := 0
	for _, r := range due {
		code, hash, err := newPayCode()
		if err != nil {
			return asked, err
		}
		// Claimed before sending, and only if still unclaimed, so two runs
		// that overlap cannot email the same family twice.
		res, err := d.Exec(`UPDATE tournament_registration
		                       SET arrival_reminded_at = ?, arrival_code_hash = ?
		                     WHERE tournament_registration_id = ? AND arrival_reminded_at IS NULL`,
			time.Now().UTC().Format(time.RFC3339), hash, r.regID)
		if err != nil {
			return asked, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if mailArrival(d, cfg, sender, r, code) == 0 {
			log.Printf("arrival reminders: %s has no email to ask", r.regID)
			continue
		}
		asked++
	}
	return asked, nil
}

// mailArrival emails one reminder with `code` in its link, and returns how
// many addresses it went to.
func mailArrival(d *sql.DB, cfg mail.Config, sender mail.Sender, r dueReminder, code string) int {
	to := arrivalRecipients(d, r)
	link := arrivalLink(cfg.AppURL, r.regID, code)
	body := arrivalReminderBody(r.tournament, r.participant, fmtDay(r.start), r.venue, link)
	for _, addr := range to {
		if err := sender.Send(addr, "Are you coming? "+r.tournament+" / ยืนยันการเข้าร่วม", body); err != nil {
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

func arrivalReminderBody(tournament, participant, start, venue, link string) string {
	var b strings.Builder
	contactEN, contactTH := contactLines()
	where := ""
	if venue != "" {
		where = " at " + venue
	}
	b.WriteString("Hello,\n\n")
	b.WriteString(tournament + " starts on " + start + where + ".\n")
	b.WriteString("Please tell us whether " + participant + " will attend:\n" + link + "\n\n")
	b.WriteString(contactEN + "\n\nJCA Chess Academy\n\n----------\n\n")
	b.WriteString("สวัสดีค่ะ\n\n")
	if venue != "" {
		b.WriteString(tournament + " จะเริ่มวันที่ " + start + " ที่ " + venue + "\n")
	} else {
		b.WriteString(tournament + " จะเริ่มวันที่ " + start + "\n")
	}
	b.WriteString("กรุณายืนยันว่า " + participant + " จะเข้าร่วมหรือไม่:\n" + link + "\n\n")
	b.WriteString(contactTH + "\n\nJCA Chess Academy\n")
	return b.String()
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
}

// SendArrivalReminders runs one reminder pass as of `day`. Exported for tests,
// which decide when the timer would have fired.
func SendArrivalReminders(d *sql.DB, cfg mail.Config, sender mail.Sender, day string) (int, error) {
	return sendArrivalReminders(d, cfg, sender, day)
}
