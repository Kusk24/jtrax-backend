// The two dates an unpaid tournament entry answers to.
//
// A place is saved before its fee is paid, so a family whose card fails, or
// who would rather pay at the desk, does not lose it on the spot. The academy's
// rule for an entry still unpaid:
//
//   - the early-bird price holds only if it is paid by the early-bird date;
//     after that the entry costs the regular price (with the student discount
//     still off it, where the organiser gives one);
//   - a place not paid for by the time registration closes is released —
//     the entry becomes Withdrawn, its place goes back to the pool, and staff
//     can restore it by setting it back to Approved.
//
// sweepUnpaidEntries applies both. It runs on a timer (StartEntrySweeper) and
// again right before a payment link is handed out, so nobody is ever sent to
// Stripe at a price that has lapsed. Entries made before 0049 carry no record
// of how they were priced and are never touched.
package api

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// sweepEvery is how often the timer runs the rules. The dates are whole days,
// so a quarter of an hour late is never late in any way anybody notices.
const sweepEvery = 15 * time.Minute

// StartEntrySweeper runs sweepUnpaidEntries now and then on a timer, until
// ctx ends. Started by the server, not by NewHandler, so tests control when
// the rules run.
func StartEntrySweeper(ctx context.Context, d *sql.DB) {
	go func() {
		ticker := time.NewTicker(sweepEvery)
		defer ticker.Stop()
		for {
			if err := sweepUnpaidEntries(d, today()); err != nil {
				log.Printf("entry rules: %v", err)
			}
			logTournamentStatus(d)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// unpaid is the SQL for "no payment against this entry has been made":
// nothing recorded, or only a Pending card attempt.
const unpaid = `NOT EXISTS (SELECT 1 FROM payment p
	                  WHERE p.tournament_registration_id = r.tournament_registration_id
	                    AND p.status <> 'Pending')`

// sweepUnpaidEntries applies the two rules as they stand on `day`.
func sweepUnpaidEntries(d *sql.DB, day string) error {
	if err := lapseEarlyBird(d, day); err != nil {
		return err
	}
	return releaseUnpaid(d, day)
}

// lapseEarlyBird reprices unpaid early-bird entries whose window has closed.
func lapseEarlyBird(d *sql.DB, day string) error {
	rows, err := d.Query(`
		SELECT r.tournament_registration_id, r.tournament_id, COALESCE(r.priced_as_student, 0)
		  FROM tournament_registration r
		  JOIN tournament t ON t.tournament_id = r.tournament_id
		 WHERE r.early_bird_applied = 1
		   AND r.status = 'Approved'
		   AND COALESCE(t.early_bird_deadline, '') <> ''
		   AND t.early_bird_deadline < ?
		   AND `+unpaid, day)
	if err != nil {
		return err
	}
	type due struct {
		id, tournament string
		student        bool
	}
	var list []due
	for rows.Next() {
		var e due
		if err := rows.Scan(&e.id, &e.tournament, &e.student); err != nil {
			rows.Close()
			return err
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, e := range list {
		price, err := loadPrice(d, e.tournament)
		if err != nil {
			return err
		}
		// Priced on a day the window is shut, so neither rule can pick the
		// early-bird price again.
		after := price
		after.EarlyBirdUntil = ""
		fee := after.OutsiderFee(day)
		if e.student {
			fee = after.StudentFee(day)
		}
		err = inTx(d, func(tx *sql.Tx) error {
			res, err := tx.Exec(`
				UPDATE tournament_registration
				   SET fee_charged = ?, early_bird_applied = 0, early_bird_lapsed_at = ?
				 WHERE tournament_registration_id = ? AND early_bird_applied = 1`,
				fee, sqliteNow(), e.id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil
			}
			/* A card attempt opened at the old price is repriced too, and its
			   Stripe page forgotten, so the next "Pay now" opens one for the
			   right amount. The webhook refuses an amount that does not match
			   the row, so an old page paid late cannot settle it cheaply. */
			_, err = tx.Exec(`
				UPDATE payment
				   SET amount = ?, final_amount = ?, stripe_checkout_url = NULL, stripe_session_id = NULL
				 WHERE tournament_registration_id = ? AND status = 'Pending'`,
				fee, fee, e.id)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// releaseUnpaid withdraws entries still unpaid once registration has closed.
// A free entry owes nothing and is never released, and neither is one made
// after the closing date.
func releaseUnpaid(d *sql.DB, day string) error {
	_, err := d.Exec(`
		UPDATE tournament_registration AS r
		   SET status = 'Withdrawn', released_at = ?
		 WHERE r.status = 'Approved'
		   AND r.early_bird_applied IS NOT NULL
		   /* Once only: a place staff restored stays restored. */
		   AND r.released_at IS NULL
		   AND COALESCE(r.fee_charged, r.fee_quoted, 0) > 0
		   AND EXISTS (SELECT 1 FROM tournament t
		                WHERE t.tournament_id = r.tournament_id
		                  AND COALESCE(t.registration_deadline, '') <> ''
		                  AND t.registration_deadline < ?
		                  /* One the desk let in after closing was a decision
		                     made after the rule, not one it should undo. */
		                  AND date(r.registered_at) <= t.registration_deadline)
		   AND `+unpaid, sqliteNow(), day)
	return err
}
