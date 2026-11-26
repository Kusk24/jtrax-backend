// The periodic jobs started with the school API and their integration clients.
package main

import (
	"context"
	"database/sql"

	"github.com/Kusk24/jtrax-backend/internal/academy"
	"github.com/Kusk24/jtrax-backend/internal/classroom"
	"github.com/Kusk24/jtrax-backend/internal/communications"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/push"
	"github.com/Kusk24/jtrax-backend/internal/retention"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
	"github.com/Kusk24/jtrax-backend/internal/tournaments"
	"github.com/Kusk24/jtrax-backend/internal/webpush"
)

func startSchoolJobs(ctx context.Context, d *sql.DB) {
	// An unpaid entry's early-bird price and place lapse on their dates
	// (internal/tournaments/entryrules.go); this applies them through the day.
	tournaments.StartEntrySweeper(ctx, d)
	// Personal data nothing needs any more is deleted on a timer; the
	// academy's own periods come from JTRAX_RETAIN_* (internal/retention/retention.go).
	retention.StartRetention(ctx, d, retention.RetentionFromEnv())
	mailCfg := mail.FromEnv()
	// Every email's footer gives the contact details the office saved in
	// Settings → Academy Contact.
	mail.SetContactSource(func() mail.Contact { return academy.AcademyContact(d) })
	tournaments.StartArrivalReminders(ctx, d, mailCfg, mail.New(mailCfg))
	// A scheduled class becomes ongoing at its start time. The desk records attendance.
	classroom.StartClassStarts(ctx, d)
	// Card payments the Stripe webhook never reported are settled by asking
	// Stripe (internal/payments/stripe.go). Off until the Stripe key is set.
	notifier := notify.New(d, mail.New(mailCfg), mailCfg)
	notifier.SetPush(push.New(push.FromEnv()))
	notifier.SetWebPush(webpush.New(webpush.FromEnv()))
	stripeClient := stripepay.New(stripepay.FromEnv())
	payments.StartStripeReconciler(ctx, d, stripeClient, notifier)
	// The emails about an unpaid tournament entry: payment not completed,
	// the day-before reminder, and the cancellation when its place is
	// released (internal/tournaments/entryemails.go).
	tournaments.StartEntryNotices(ctx, d, stripeClient, notifier)
	// Low-credit and expiring-credit reminders, in-app, once each
	// (internal/payments/creditreminders.go).
	payments.StartCreditReminders(ctx, d, notifier)
	// Announcements scheduled for later tell parents when they start showing
	// (internal/communications/announcementwindow.go).
	communications.StartAnnouncementSchedule(ctx, d, notifier)
}
