package api_test

import (
	"database/sql"
	"net/http"
	"testing"
	"time"
)

// The hole this closes: a parent's own request used to carry the fee, and the
// card payment charged it. Every door that could set it is tried here.
func TestAParentCannotChooseTheirOwnFee(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	priceWellington(t, srv, 300)

	// The generic resource no longer takes a parent's write at all.
	status, _, _ := parent.do("POST", "/api/v1/tournament-registrations", map[string]any{
		"tournament_id": "trn_wellington", "student_id": "stu_penny",
		"participant_name": "Penny", "fee_charged": 1,
	})
	if status != http.StatusForbidden {
		t.Fatalf("parent POST with a fee: got %d, want 403", status)
	}

	// Nor does the parent's own door accept a price.
	status, _, _ = parent.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, parent, map[string]any{
		"student_id": "stu_penny", "fee_charged": 1,
	}))
	if status != http.StatusBadRequest {
		t.Fatalf("entry carrying a fee: got %d, want 400", status)
	}

	status, obj, _ := parent.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, parent, map[string]any{
		"student_id": "stu_penny",
	}))
	if status != 201 {
		t.Fatalf("entering: %d (%v)", status, obj)
	}
	regID := obj["tournament_registration_id"].(string)

	// And once the row exists, it cannot be edited down either.
	status, _, _ = parent.do("PATCH", "/api/v1/tournament-registrations/"+regID, map[string]any{
		"fee_charged": 1,
	})
	if status != http.StatusForbidden {
		t.Fatalf("parent PATCH of the fee: got %d, want 403", status)
	}

	var charged float64
	d.QueryRow(`SELECT fee_charged FROM tournament_registration WHERE tournament_registration_id = ?`,
		regID).Scan(&charged)
	if charged != 300 {
		t.Fatalf("fee on the row is %v, want the tournament's 300", charged)
	}
}

// Somebody else's child is not one this parent can enter.
func TestAParentCanOnlyEnterTheirOwnChild(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")
	status, _, _ := c.do("POST", "/api/v1/tournaments/trn_wellington/entries", verifiedEntry(t, c, map[string]any{
		"student_id": "stu_nobody",
	}))
	if status != http.StatusNotFound {
		t.Fatalf("entering a stranger's child: got %d, want 404", status)
	}
}

// The organiser chooses what a JCA student pays, and the price the portal is
// shown is the price the entry is charged — one rule, not two copies of it.
func TestStudentPricingFollowsTheTournament(t *testing.T) {
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	cases := []struct {
		name            string
		discount, early bool
		earlyUntil      string
		want            float64
	}{
		{"discount only (the old rule)", true, false, tomorrow, 270},
		{"early bird only", false, true, tomorrow, 250},
		{"both, stacked", true, true, tomorrow, 225},
		{"neither", false, false, tomorrow, 300},
		{"both, after early bird ends", true, true, yesterday, 270},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDB(t)
			srv := newServerOn(t, d)
			staff := &client{t: t, srv: srv}
			staff.login("admin@jca.ac.th")
			status, obj, _ := staff.do("PATCH", "/api/v1/tournaments/trn_wellington", map[string]any{
				"regular_fee": 300, "early_bird_fee": 250, "early_bird_deadline": tc.earlyUntil,
				"student_discount_pct":  10,
				"student_gets_discount": tc.discount, "student_gets_early_bird": tc.early,
			})
			if status != 200 {
				t.Fatalf("setting the price: %d (%v)", status, obj)
			}

			parent := &client{t: t, srv: srv}
			parent.login("sandy01234@gmail.com")
			_, shown, _ := parent.do("GET", "/api/v1/tournaments/trn_wellington", nil)
			if shown["student_fee"] != tc.want {
				t.Fatalf("portal is shown %v, want %v", shown["student_fee"], tc.want)
			}
			status, entry, _ := parent.do("POST", "/api/v1/tournaments/trn_wellington/entries",
				verifiedEntry(t, parent, map[string]any{"student_id": "stu_penny"}))
			if status != 201 || entry["fee_charged"] != tc.want {
				t.Fatalf("entry charged %v (%d), want %v", entry["fee_charged"], status, tc.want)
			}
		})
	}
}

// A fee handed over at the counter used to leave the entry reading Pending for
// ever, because nothing tied the money to it.
func TestTheDeskRecordsAFeePaid(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	regID := registerPenny(t, parent, 300)
	path := "/api/v1/tournament-registrations/" + regID + "/desk-payment"

	// Money is staff's to assert, and card is Stripe's.
	if status, _, _ := parent.do("POST", path, map[string]any{"payment_method": "Cash"}); status != http.StatusForbidden {
		t.Fatalf("parent at the desk door: got %d, want 403", status)
	}
	desk := &client{t: t, srv: srv}
	desk.login("admin@jca.ac.th")
	if status, _, _ := desk.do("POST", path, map[string]any{"payment_method": "CreditCard"}); status != http.StatusBadRequest {
		t.Fatalf("card at the desk: got %d, want 400", status)
	}

	status, obj, _ := desk.do("POST", path, map[string]any{"payment_method": "Cash"})
	if status != 200 {
		t.Fatalf("recording cash: %d (%v)", status, obj)
	}
	var n int
	var method, payStatus string
	var amount float64
	d.QueryRow(`SELECT COUNT(*), MAX(payment_method), MAX(status), MAX(final_amount)
	              FROM payment WHERE tournament_registration_id = ?`, regID).
		Scan(&n, &method, &payStatus, &amount)
	if n != 1 || method != "Cash" || payStatus != "Paid" || amount != 300 {
		t.Fatalf("payment behind the entry: n=%d %s %s %v", n, method, payStatus, amount)
	}

	// Twice is not two payments.
	if status, _, _ := desk.do("POST", path, map[string]any{"payment_method": "Cash"}); status != http.StatusConflict {
		t.Fatalf("paying twice: got %d, want 409", status)
	}
}

// Once the money is in, the fee is what was paid. Changing it is a refund, not
// an edit — but the rest of the entry stays editable.
func TestAPaidFeeCannotBeRewritten(t *testing.T) {
	d := newDB(t)
	srv := newServerOn(t, d)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	regID := registerPenny(t, parent, 300)

	desk := &client{t: t, srv: srv}
	desk.login("admin@jca.ac.th")
	desk.do("POST", "/api/v1/tournament-registrations/"+regID+"/desk-payment",
		map[string]any{"payment_method": "PromptPay"})

	path := "/api/v1/tournament-registrations/" + regID
	if status, _, _ := desk.do("PATCH", path, map[string]any{"fee_charged": 1}); status != http.StatusBadRequest {
		t.Fatalf("editing a paid fee: got %d, want 400", status)
	}
	if status, _, _ := desk.do("PATCH", path, map[string]any{"remarks": "Seat by the door"}); status != 200 {
		t.Fatalf("editing the remarks of a paid entry: got %d, want 200", status)
	}
}

// Staff repricing an entry the family has opened a card link for must reprice
// the payment too, and drop the old link — or the family pays the old number.
func TestRepricingAnEntryRepricesItsOpenCardPayment(t *testing.T) {
	d := newDB(t)
	srv := newStripeServer(t, d)
	parent := &client{t: t, srv: srv}
	parent.login("sandy01234@gmail.com")
	regID := registerPenny(t, parent, 300)
	if status, obj, _ := parent.do("POST", "/api/v1/tournament-registrations/"+regID+"/stripe-link", nil); status != 200 {
		t.Fatalf("opening the card link: %d (%v)", status, obj)
	}

	desk := &client{t: t, srv: srv}
	desk.login("admin@jca.ac.th")
	if status, obj, _ := desk.do("PATCH", "/api/v1/tournament-registrations/"+regID,
		map[string]any{"fee_charged": 200}); status != 200 {
		t.Fatalf("repricing: %d (%v)", status, obj)
	}
	var amount float64
	var link *string
	d.QueryRow(`SELECT final_amount, stripe_checkout_url FROM payment WHERE tournament_registration_id = ?`,
		regID).Scan(&amount, &link)
	if amount != 200 || link != nil {
		t.Fatalf("open payment after repricing: amount %v, link %v", amount, link)
	}
}

// verifiedEntry is a parent's entry body as the portal sends it after the ID
// card step: an ID card check for the child, read as born in 2017, and the
// Under 10 group, which that fits.
func verifiedEntry(t *testing.T, c *client, body map[string]any) map[string]any {
	t.Helper()
	// The seeded tournament closed on its deadline; these tests are about
	// entering one that is open.
	if v, ok := serverDBs.Load(c.srv.URL); ok {
		v.(*sql.DB).Exec(`UPDATE tournament SET public_registration = 1, registration_deadline = NULL
		                   WHERE tournament_id = 'trn_wellington'`)
	}
	student, _ := body["student_id"].(string)
	if _, ok := body["tournament_category_id"]; !ok {
		body["tournament_category_id"] = "tcat_u10"
	}
	if _, ok := body["nickname"]; !ok {
		body["nickname"] = "Pen"
	}
	if _, ok := body["accept_terms"]; !ok {
		body["accept_terms"] = true
	}
	return withCheck(t, c, "trn_wellington", student, "2017-05-01", "id_check", body)
}

// The parent form asks what the public one does — a nickname and the
// conditions of entry — and takes the family's contact details from file.
func TestAParentEntryRecordsTermsNicknameAndTheFamilysContact(t *testing.T) {
	c := &client{t: t, srv: newServer(t)}
	c.login("sandy01234@gmail.com")
	path := "/api/v1/tournaments/trn_wellington/entries"

	if s, _, _ := c.do("POST", path, verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny", "accept_terms": false,
	})); s != 400 {
		t.Fatalf("terms not accepted: want 400, got %d", s)
	}
	if s, _, _ := c.do("POST", path, verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny", "nickname": " ",
	})); s != 400 {
		t.Fatalf("no nickname: want 400, got %d", s)
	}
	s, out, _ := c.do("POST", path, verifiedEntry(t, c, map[string]any{
		"student_id": "stu_penny", "participant_name_th": "เพนนี",
	}))
	if s != 201 {
		t.Fatalf("enter: %d (%v)", s, out)
	}
	c.login("admin@jca.ac.th")
	_, row, _ := c.do("GET", "/api/v1/tournament-registrations/"+out["tournament_registration_id"].(string), nil)
	if row["nickname"] != "Pen" || row["participant_name_th"] != "เพนนี" || row["terms_accepted_at"] == nil {
		t.Fatalf("not recorded: %v", row)
	}
	if row["contact_phone"] != "+66 12 345 6789" || row["contact_email"] != "sandy01234@gmail.com" {
		t.Fatalf("family contact not on the entry: phone %v email %v", row["contact_phone"], row["contact_email"])
	}
}

// A parent cannot enter a tournament whose registration the academy closed
// on its card, or whose closing date has passed — the same doors the public
// form answers to.
func TestAParentCannotEnterAClosedTournament(t *testing.T) {
	d := newDB(t)
	c := &client{t: t, srv: newServerOn(t, d)}
	c.login("sandy01234@gmail.com")
	path := "/api/v1/tournaments/trn_wellington/entries"

	body := verifiedEntry(t, c, map[string]any{"student_id": "stu_penny"}) // opens it
	d.Exec(`UPDATE tournament SET public_registration = 0 WHERE tournament_id = 'trn_wellington'`)
	if s, out, _ := c.do("POST", path, body); s != 409 {
		t.Fatalf("closed by the academy: want 409, got %d (%v)", s, out)
	}

	d.Exec(`UPDATE tournament SET public_registration = 1, registration_deadline = '2020-01-01' WHERE tournament_id = 'trn_wellington'`)
	if s, out, _ := c.do("POST", path, body); s != 409 {
		t.Fatalf("past its closing date: want 409, got %d (%v)", s, out)
	}
}
