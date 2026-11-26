// Route table: auth endpoints plus every registry resource under /api/v1.
package api

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/academy"
	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/chessplay"
	"github.com/Kusk24/jtrax-backend/internal/classroom"
	"github.com/Kusk24/jtrax-backend/internal/communications"
	"github.com/Kusk24/jtrax-backend/internal/families"
	"github.com/Kusk24/jtrax-backend/internal/forms"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/lineinbox"
	"github.com/Kusk24/jtrax-backend/internal/mail"
	"github.com/Kusk24/jtrax-backend/internal/notify"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
	"github.com/Kusk24/jtrax-backend/internal/push"
	"github.com/Kusk24/jtrax-backend/internal/tournaments"
	"github.com/Kusk24/jtrax-backend/internal/webpush"
)

// NewHandler builds the full API handler (with the supplied Gin middleware), reading
// the mail and OCR configuration from the environment.
func NewHandler(d *sql.DB, middleware ...gin.HandlerFunc) http.Handler {
	cfg := mail.FromEnv()
	return NewHandlerWith(d, cfg, mail.New(cfg), ocr.New(ocr.FromEnv()), middleware...)
}

// NewHandlerWithMail keeps the older two-dependency form working for tests that
// only care about mail; form scanning is off, which is a supported state.
func NewHandlerWithMail(d *sql.DB, mailCfg mail.Config, sender mail.Sender) http.Handler {
	return NewHandlerWith(d, mailCfg, sender, nil)
}

// NewHandlerWith is the injectable form: tests pass a Sender that captures
// messages instead of delivering them, and an OCR provider that returns a fixed
// reading instead of calling a paid API.
func NewHandlerWith(d *sql.DB, mailCfg mail.Config, sender mail.Sender, scanner ocr.Provider, middleware ...gin.HandlerFunc) http.Handler {
	return newRouter(func(routes gin.IRoutes) {
		registerRoutes(routes, d, mailCfg, sender, scanner)
	}, middleware...)
}

func registerRoutes(mux gin.IRoutes, d *sql.DB, mailCfg mail.Config, sender mail.Sender, scanner ocr.Provider) {
	// Every answered call to the scan service is kept with its tokens, for
	// the cost line in Settings (scancost.go).
	scanner = forms.RecordScanUsage(d, scanner)

	mux.GET("/health", func(ctx *gin.Context) {
		httpx.JSON(ctx, http.StatusOK, map[string]string{"status": "ok"})
	})

	accounts.MountAuth(mux, d, mailCfg, sender)

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
	chessplay.RegisterRoutes(mux, d)
	tournaments.RegisterRoutes(mux, d, mailCfg, sender, notifier)
	forms.MountRegistrationScan(mux, d, scanner)
	tournaments.MountIDCardScan(mux, d, scanner)
	forms.MountOCRSettings(mux, d, scanner)
	lineinbox.MountLine(mux, d)
	communications.MountNotifications(mux, d, notifier, browserPush.PublicKey())
	classroom.MountClassCancel(mux, d, notifier)
	academy.MountDashboardActivity(mux, d)
	academy.MountAcademyContact(mux, d)

	resources := Registry()
	communications.AttachNotificationHooks(resources, d, notifier)
	for _, rs := range resources {
		rs.Register(mux, d)
	}
}

func mountPeople(mux gin.IRoutes, d *sql.DB) {
	accounts.MountUserAccounts(mux, d)
	families.MountPeopleCascade(mux, d)
	families.MountMyData(mux, d)
	classroom.MountCertificates(mux, d)
}
