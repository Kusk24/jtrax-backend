// An ID card check: the step both tournament entry forms take before a player
// is entered — the public form and a parent's portal.
//
// The card is read by the scan, once. What was read — the date of birth, the
// name, which document — is kept in id_card_check and its id goes back to the
// form; the photo is dropped with the request (0062). The entry then names the
// check, and the server takes the date of birth from it rather than from what
// the form typed, so the age group is decided by the document.
//
// A check is spent by the entry that uses it, and goes stale after
// idCheckLife, so it cannot be kept and reused for somebody else.
package tournaments

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
	"github.com/Kusk24/jtrax-backend/internal/accounts"
	"github.com/Kusk24/jtrax-backend/internal/db"
	"github.com/Kusk24/jtrax-backend/internal/families"
	"github.com/Kusk24/jtrax-backend/internal/forms"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
	"github.com/Kusk24/jtrax-backend/internal/ocr"
)

// idCheckLife is how long a scan stays usable: long enough to fill in the
// rest of the form, short enough that a stale one is not lying about.
const idCheckLife = 2 * time.Hour

// idCheck is what a scan read off the card.
type idCheck struct {
	DateOfBirth  string
	Name         string
	DocumentType string
}

// readIDCard reads the card in the request's "image" field and returns what
// was on it. It writes the error reply itself and returns nil when it fails.
// The image is never written anywhere.
func readIDCard(d *sql.DB, provider ocr.Provider, ctx *gin.Context) *ocr.IDCard {
	provider = forms.ScannerFor(d, provider)
	reader, ok := provider.(ocr.IDCardReader)
	if provider == nil || !ok {
		forms.ScanFailed(ctx, http.StatusServiceUnavailable,
			"ID card reading is not available right now — please try again later", "not_configured", 0, nil)
		return nil
	}

	// Cap what can be read before parsing, not after.
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, forms.MaxScanBytes)
	if err := ctx.Request.ParseMultipartForm(forms.MaxScanBytes); err != nil {
		forms.ScanFailed(ctx, http.StatusRequestEntityTooLarge, "image is too large (10 MB maximum)", "too_large", 0, err)
		return nil
	}
	defer ctx.Request.MultipartForm.RemoveAll()

	file, _, err := ctx.Request.FormFile("image")
	if err != nil {
		forms.ScanFailed(ctx, http.StatusBadRequest, "expected an image field named 'image'", "no_image", 0, err)
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		forms.ScanFailed(ctx, http.StatusBadRequest, "could not read the uploaded image", "no_image", 0, err)
		return nil
	}
	if len(data) == 0 {
		forms.ScanFailed(ctx, http.StatusBadRequest, "the uploaded image is empty", "no_image", 0, nil)
		return nil
	}
	// Sniff the bytes rather than trust the declared type: the browser's
	// content type is caller-supplied, and this decides what is sent on to
	// a third party.
	mime := forms.SniffImage(data)
	if !forms.ScannableTypes[mime] {
		forms.ScanFailed(ctx, http.StatusUnsupportedMediaType,
			"upload a photo of the card (JPEG, PNG, WebP or HEIC)", "not_an_image", 0, nil)
		return nil
	}

	card, err := reader.ExtractIDCard(ctx.Request.Context(), data, mime)
	if err != nil {
		// The provider's error can quote the request, which is the child's
		// identity document, so it stays internal: the reply names only the
		// kind of failure and the provider's status.
		forms.ProviderFailed(ctx, "card", err)
		return nil
	}
	if _, err := time.Parse("2006-01-02", strings.TrimSpace(card.DateOfBirth.Value)); err != nil {
		// The date of birth is what the check is for; a card read without
		// one has verified nothing.
		forms.ScanFailed(ctx, http.StatusUnprocessableEntity,
			"could not read the date of birth — try a clearer, straighter photo", "no_date_of_birth", 0, nil)
		return nil
	}
	return card
}

// saveIDCheck keeps what a card said, for the entry that will use it, and
// clears checks nobody used.
func saveIDCheck(d *sql.DB, tournamentID, studentID string, card *ocr.IDCard) (string, error) {
	d.Exec(`DELETE FROM id_card_check WHERE created_at < datetime('now', '-1 day')`)
	name := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(card.FirstName.Value), strings.TrimSpace(card.LastName.Value)}, " "))
	id := db.NewID("idc")
	_, err := d.Exec(`INSERT INTO id_card_check
		(id_card_check_id, tournament_id, student_id, date_of_birth, name, document_type)
		VALUES (?,?,?,?,?,?)`,
		id, tournamentID, db.NullIfEmpty(studentID), strings.TrimSpace(card.DateOfBirth.Value),
		db.NullIfEmpty(name), db.NullIfEmpty(card.DocumentType))
	return id, err
}

// useIDCheck spends the check an entry names. It must be for this
// tournament (and this child, from the portal) and still be fresh.
func useIDCheck(tx *sql.Tx, checkID, tournamentID, studentID string) (c idCheck, err error) {
	if strings.TrimSpace(checkID) == "" {
		return c, registrationRefusal{http.StatusBadRequest, "please upload the player's ID card to verify their age"}
	}
	var forStudent, name, docType sql.NullString
	var created string
	err = tx.QueryRow(`SELECT date_of_birth, student_id, name, document_type, created_at
	                     FROM id_card_check WHERE id_card_check_id = ? AND tournament_id = ?`,
		checkID, tournamentID).Scan(&c.DateOfBirth, &forStudent, &name, &docType, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, registrationRefusal{http.StatusBadRequest, "the ID card check has expired — please upload the ID card again"}
	}
	if err != nil {
		return c, err
	}
	if forStudent.String != studentID {
		return c, registrationRefusal{http.StatusBadRequest, "the ID card check is for another player — please upload this player's ID card"}
	}
	if at, perr := time.Parse("2006-01-02 15:04:05", created); perr == nil && time.Since(at) > idCheckLife {
		return c, registrationRefusal{http.StatusBadRequest, "the ID card check has expired — please upload the ID card again"}
	}
	if _, err = tx.Exec(`DELETE FROM id_card_check WHERE id_card_check_id = ?`, checkID); err != nil {
		return c, err
	}
	c.Name, c.DocumentType = name.String, docType.String
	/* The card is the entry's date of birth; under a year old is a misread. */
	if families.DobTooYoung(c.DateOfBirth) {
		return c, registrationRefusal{http.StatusBadRequest, "the date of birth on the ID card is less than a year ago — please upload a clearer photo"}
	}
	return c, nil
}

// checkCategoryAge is the age rule for one category, against a date of birth
// from an ID card: the category must belong to the tournament, and the age
// in its name goes by birth year, as chess events do — "U10" at an event in
// 2026 is anybody born in 2016 or later. An empty category skips this optional check.
func checkCategoryAge(tx *sql.Tx, tournamentID, categoryID, dob string) error {
	if categoryID == "" {
		return nil
	}
	var name, start string
	err := tx.QueryRow(`SELECT c.name, COALESCE(t.start_date, '')
	                     FROM tournament_category c JOIN tournament t ON t.tournament_id = c.tournament_id
	                    WHERE c.tournament_category_id = ? AND c.tournament_id = ?`,
		categoryID, tournamentID).Scan(&name, &start)
	if errors.Is(err, sql.ErrNoRows) {
		return registrationRefusal{http.StatusBadRequest, "that category is not part of this tournament"}
	}
	if err != nil {
		return err
	}
	limit := categoryAgeLimit(name)
	if limit == 0 {
		return nil
	}
	/* The academy's year, not the server's: a tournament in Bangkok is dated
	   by the poster, and a UTC clock turns the year over seven hours early. */
	year := academytime.Now().Year()
	if parsed, perr := time.Parse("2006-01-02", start); perr == nil {
		year = parsed.Year()
	}
	born, perr := time.Parse("2006-01-02", dob)
	if perr != nil {
		return registrationRefusal{http.StatusBadRequest, "this category has an age limit — please upload the player's ID card"}
	}
	if !bornInTime(born, year, limit) {
		return registrationRefusal{http.StatusBadRequest, "by the ID card the player is too old for this category — please pick another"}
	}
	return nil
}

// ageAt is how old somebody born on dob is on the day `on` (both
// YYYY-MM-DD), or 0 when either is missing.
func ageAt(dob, on string) int {
	b, err := time.Parse("2006-01-02", dob)
	if err != nil {
		return 0
	}
	day, err := time.Parse("2006-01-02", on)
	if err != nil {
		day = academytime.Now()
	}
	age := day.Year() - b.Year()
	if day.Month() < b.Month() || (day.Month() == b.Month() && day.Day() < b.Day()) {
		age--
	}
	if age < 0 {
		return 0
	}
	return age
}

// handleParentScanIDCard reads one of the caller's children's ID card for a
// tournament they are about to enter. Parents only; the child must be theirs
// and the tournament live.
func handleParentScanIDCard(d *sql.DB, provider ocr.Provider) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		id := accounts.RequireIdentity(d, ctx)
		if id == nil {
			return
		}
		if id.Role != "Parent" {
			httpx.Error(ctx, http.StatusForbidden, "only a parent may enter their child", nil)
			return
		}
		tournamentID := ctx.Param("id")
		/* No card is read for a tournament that is not taking entries. */
		if msg, err := entryClosed(d, tournamentID); err != nil {
			httpx.Error(ctx, http.StatusNotFound, "no such tournament", nil)
			return
		} else if msg != "" {
			httpx.Error(ctx, http.StatusConflict, msg, nil)
			return
		}
		var n int
		// Read before the image so the form field is there to check; the
		// multipart parse happens inside readIDCard, so peek at the query
		// string instead.
		studentID := strings.TrimSpace(ctx.Query("student_id"))
		if err := d.QueryRow(`SELECT COUNT(*) FROM student_parent WHERE parent_id = ? AND student_id = ?`,
			id.ParentID, studentID).Scan(&n); err != nil || n == 0 {
			httpx.Error(ctx, http.StatusNotFound, "no such child", nil)
			return
		}
		card := readIDCard(d, provider, ctx)
		if card == nil {
			return
		}
		checkID, err := saveIDCheck(d, tournamentID, studentID, card)
		if err != nil {
			httpx.Error(ctx, http.StatusInternalServerError, "could not save the ID card check", err)
			return
		}
		httpx.JSON(ctx, http.StatusOK, map[string]any{"fields": card, "checkId": checkID})
	}
}
