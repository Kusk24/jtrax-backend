// Tournament registration, results and payment routes.
package tournaments

import (
	"database/sql"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/payments"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
)

func RegisterRoutes(mux gin.IRoutes, d *sql.DB, mailCfg mail.Config, sender mail.Sender, notifier *notify.Service) {
	results := mountChessResults(mux, d)
	mountTournamentResults(mux, d, results)
	mountTournamentRegulation(mux, d)
	mountTournamentBanner(mux, d)
	mountRegistrationQueue(mux, d)
	mountTournamentEntry(mux, d)
	mountArrival(mux, d, mailCfg, sender)

	// Public registration and the payment endpoints share the same checkout client.
	cfg := stripepay.FromEnv()
	client := stripepay.New(cfg)
	mountTournamentDraft(mux, d, client != nil)
	mountPublicRegistration(mux, &publicEntryDeps{
		db: d, sender: sender, mail: mailCfg, stripe: client, stripeCfg: cfg, notifier: notifier,
	})
	mountDeskPayment(mux, d, notifier)
	payments.MountPaymentVoid(mux, d)
	mountStripe(mux, d, client, cfg, notifier)
}
