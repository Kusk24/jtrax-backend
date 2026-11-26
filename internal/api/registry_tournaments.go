// Tournament events, categories and registrations served by the shared CRUD API.
package api

import (
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/records"
	"github.com/Kusk24/jtrax-backend/internal/tournaments"
)

func tournamentResources() []*records.Resource {
	return []*records.Resource{
		{
			Name: "tournaments", Table: "tournament", IDCol: "tournament_id", IDPrefix: "trn",
			Cols: []records.Col{
				{Name: "name", Kind: "text", Required: true},
				{Name: "tournament_status", Kind: "text", Enum: tournamentStat},
				// Set when the office picks a status by hand (migration
				// 0056); otherwise the dates decide (tournamentstatus.go).
				{Name: "status_locked", Kind: "bool"},
				{Name: "start_date", Kind: "text"},
				{Name: "end_date", Kind: "text"},
				{Name: "venue_name", Kind: "text"},
				{Name: "venue_address", Kind: "text"},
				// A Google Maps search link built from venue_name at creation
				// time — see 0037. Not derived on read, so it survives the
				// venue name later being edited to something the link no
				// longer matches without silently drifting.
				{Name: "venue_map_url", Kind: "text"},
				{Name: "organizer_name", Kind: "text"},
				{Name: "registration_deadline", Kind: "text"},
				{Name: "early_bird_fee", Kind: "real"},
				// The day early bird ends — without it the fee above was
				// never chargeable (see 0029).
				{Name: "early_bird_deadline", Kind: "text"},
				{Name: "regular_fee", Kind: "real"},
				{Name: "max_participants", Kind: "int"},
				{Name: "registration_website_url", Kind: "text"},
				{Name: "registration_qr_code_image", Kind: "text"},
				{Name: "regulations_document_url", Kind: "text"},
				// Days before the start to ask entrants whether they are
				// coming (migration 0055). Empty or 0: no reminder.
				{Name: "arrival_reminder_days", Kind: "int"},
				// Opt-in, and staff-only to change: turning it on publishes
				// children's names and scores to anyone with the link.
				{Name: "results_public", Kind: "bool"},
				// Opt-in for the same reason: it opens a write endpoint to
				// anyone with the link.
				{Name: "public_registration", Kind: "bool"},
				{Name: "student_discount_pct", Kind: "int"},
				// Which reductions a JCA student gets at this event — the
				// discount above, the early-bird price, both or neither (0035).
				{Name: "student_gets_discount", Kind: "bool"},
				{Name: "student_gets_early_bird", Kind: "bool"},
				// The chess-results.com event this tournament is published as,
				// when the standings are somebody else's to author. Read here so
				// the console's list can say which events follow an arbiter.
				{Name: "chess_results_id", Kind: "int"},
				// Saved by the create wizard before the organiser has reviewed
				// it, and taken live only by /publish (see tournamentdraft.go).
				{Name: "draft", Kind: "bool"},
			},
			Derived: []records.Derived{
				// Whether the organiser uploaded a banner; without one the
				// pages draw their own (see banner.go).
				{Name: "has_banner", Roles: everyone,
					Expr: "EXISTS(SELECT 1 FROM tournament_banner b WHERE b.tournament_id = tournament.tournament_id)"},
				// Whether a regulation file was uploaded, so the parent portal
				// links it only when there is one to open (regulation.go).
				{Name: "has_regulation", Roles: everyone,
					Expr: "EXISTS(SELECT 1 FROM tournament_regulation g WHERE g.tournament_id = tournament.tournament_id)"},
			},
			AfterWrite: tournaments.TournamentStatusAfterWrite,
			ReadRoles:  everyone,
			// A draft is the organiser's until it is published.
			Scope: map[string]records.ScopeFn{
				"Teacher": tournaments.NotDraft,
				"Parent":  tournaments.NotDraft,
				"Student": tournaments.NotDraft,
			},
			// What a JCA student is charged today, from the same rule the
			// server charges by — so the portals show a price rather than
			// work one out (see pricing.go).
			Decorate: payments.WithStudentFee,
		},
		{
			Name: "tournament-categories", Table: "tournament_category", IDCol: "tournament_category_id", IDPrefix: "tcat",
			Cols: []records.Col{
				{Name: "tournament_id", Kind: "text", Required: true},
				{Name: "name", Kind: "text", Required: true},
			},
			ReadRoles:    everyone,
			BeforeDelete: tournaments.ReleaseCategoryEntrants,
		},
		{
			Name: "tournament-registrations", Table: "tournament_registration", IDCol: "tournament_registration_id", IDPrefix: "treg",
			Duplicate: tournaments.DuplicateEntryMessage,
			Cols: []records.Col{
				{Name: "tournament_id", Kind: "text", Required: true},
				// No longer required: a member of the public registering for an
				// open event has no student record, and NULL is what says so.
				{Name: "student_id", Kind: "text"},
				{Name: "participant_name", Kind: "text", Required: true},
				{Name: "participant_contact", Kind: "text"},
				{Name: "participant_date_of_birth", Kind: "text"},
				{Name: "tournament_category_id", Kind: "text"},
				{Name: "fide_rating", Kind: "real"},
				{Name: "fee_charged", Kind: "real"},
				{Name: "registered_at", Kind: "text"},
				{Name: "status", Kind: "text", Enum: registrationStat},
				{Name: "source", Kind: "text"},
				{Name: "contact_email", Kind: "text"},
				{Name: "contact_phone", Kind: "text"},
				{Name: "fee_quoted", Kind: "real"},
				{Name: "student_discount_applied", Kind: "bool"},
				// What the family said about their own child. Medical notes are
				// for the day; remarks are a request for the office.
				{Name: "medical_notes", Kind: "text"},
				{Name: "remarks", Kind: "text"},
				// Called across the hall and printed on the pairing card.
				{Name: "nickname", Kind: "text"},
				// As claimed on the entry form. Kept alongside the date of
				// birth rather than derived from it: the interesting case for
				// an age-limited group is when the two disagree.
				{Name: "participant_age", Kind: "int"},
				// As printed on a Thai ID card; a passport holder has none.
				{Name: "participant_name_th", Kind: "text"},
				// Which chess-results player this entry is, when staff picked
				// one because the names did not match (0052). Empty means the
				// console matches by name.
				{Name: "results_section_id", Kind: "int"},
				{Name: "results_player_name", Kind: "text"},
				// Whether they are coming (migration 0055): Pending until they
				// answer the reminder, or the desk records a phone call.
				{Name: "arrival_status", Kind: "text", Enum: arrivalStatus},
			},
			// terms_accepted_at is readable and not writable. It is a record of
			// consent, and a consent record staff can set by hand is one that
			// cannot be relied on for the question it exists to answer.
			Derived: []records.Derived{
				{Name: "terms_accepted_at", Expr: "terms_accepted_at"},
				{Name: "arrival_reminded_at", Expr: "arrival_reminded_at"},
				{Name: "arrival_answered_at", Expr: "arrival_answered_at"},
				// What the ID card scan read, beside what was submitted —
				// evidence for staff checking an age group, so not editable.
				{Name: "ocr_name", Expr: "ocr_name"},
				{Name: "ocr_date_of_birth", Expr: "ocr_date_of_birth"},
				{Name: "id_document_type", Expr: "id_document_type"},
				// The early-bird and closing-date rules' own record of what
				// they did (entryrules.go).
				{Name: "early_bird_applied", Expr: "early_bird_applied"},
				{Name: "early_bird_lapsed_at", Expr: "early_bird_lapsed_at"},
				{Name: "released_at", Expr: "released_at"},
			},
			// Staff write here; a parent enters their child through
			// `tournaments/{id}/entries`, which prices the entry itself. This
			// door used to take the fee and status from the parent's own
			// request, so a family could register at a price they chose.
			ReadRoles: []string{"Parent", "Student"},
			Scope: map[string]records.ScopeFn{
				"Parent":  byParentStudents("student_id"),
				"Student": byOwnStudent("student_id"),
			},
			Check:      tournaments.CheckRegistrationNotes,
			AfterWrite: tournaments.SyncRegistrationPayment,
		},
	}
}
