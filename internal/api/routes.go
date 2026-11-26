// Route table: auth endpoints plus every registry resource under /api/v1.
package api

import (
	"database/sql"
	"net/http"

	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
	"github.com/Kusk24/jtrax-backend/internal/push"
	"github.com/Kusk24/jtrax-backend/internal/stripepay"
	"github.com/Kusk24/jtrax-backend/internal/webpush"
)

// NewHandler builds the full API handler (CORS applied by the caller), reading
// the mail and OCR configuration from the environment.
func NewHandler(d *sql.DB) http.Handler {
	cfg := mail.FromEnv()
	return NewHandlerWith(d, cfg, mail.New(cfg), ocr.New(ocr.FromEnv()))
}

// NewHandlerWithMail keeps the older two-dependency form working for tests that
// only care about mail; form scanning is off, which is a supported state.
func NewHandlerWithMail(d *sql.DB, mailCfg mail.Config, sender mail.Sender) http.Handler {
	return NewHandlerWith(d, mailCfg, sender, nil)
}

// NewHandlerWith is the injectable form: tests pass a Sender that captures
// messages instead of delivering them, and an OCR provider that returns a fixed
// reading instead of calling a paid API.
func NewHandlerWith(d *sql.DB, mailCfg mail.Config, sender mail.Sender, scanner ocr.Provider) http.Handler {
	mux := newRouter()
	registerRoutes(mux, d, mailCfg, sender, scanner)
	return mux
}

func registerRoutes(mux routeRegistrar, d *sql.DB, mailCfg mail.Config, sender mail.Sender, scanner ocr.Provider) {
	// Every answered call to the scan service is kept with its tokens, for
	// the cost line in Settings (scancost.go).
	scanner = recordScanUsage(d, scanner)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mountAuth(mux, d, mailCfg, sender)

	// Built early: the desk's tournament fee and the notification endpoints
	// below both send through it.
	notifier := notify.New(d, sender, mailCfg)
	// Phones get pushes through Expo, which needs no key to be switched on.
	notifier.SetPush(push.New(push.FromEnv()))
	// Browsers get them through their own push services, once the VAPID keys
	// are set (internal/webpush).
	browserPush := webpush.New(webpush.FromEnv())
	notifier.SetWebPush(browserPush)

	mountPeople(mux, d)
	mountChess(mux, d)
	mountTournamentOperations(mux, d, mailCfg, sender, notifier)
	mountRegistrationScan(mux, d, scanner)
	mountIDCardScan(mux, d, scanner)
	mountOCRSettings(mux, d, scanner)
	mountLine(mux, d)
	mountNotifications(mux, d, notifier, browserPush.PublicKey())
	mountClassCancel(mux, d, notifier)
	mountDashboardActivity(mux, d)
	mountAcademyContact(mux, d)

	resources := Registry()
	attachNotificationHooks(resources, d, notifier)
	for _, rs := range resources {
		rs.mount(mux, d)
	}
}

func mountChess(mux routeRegistrar, d *sql.DB) {
	relay := mountGameRooms(mux, d)
	mountChallenges(mux, d, relay)
	mountPuzzles(mux, d)
	mountPuzzleBank(mux, d)
	mountPractice(mux, d)
	mountProgress(mux, d)
	mountHistory(mux, d)
	mountLichess(mux, d)
}

func mountPeople(mux routeRegistrar, d *sql.DB) {
	mountUserAccounts(mux, d)
	mountPeopleCascade(mux, d)
	mountMyData(mux, d)
	mountCertificates(mux, d)
}

func mountTournamentOperations(mux routeRegistrar, d *sql.DB, mailCfg mail.Config, sender mail.Sender, notifier *notify.Service) {
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
	mountPaymentVoid(mux, d)
	mountStripe(mux, d, client, cfg, notifier)
}
