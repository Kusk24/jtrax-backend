// Consent records — which privacy notice a family was shown and agreed to, and
// explicit consent for health data (PDPA s.23, s.26; migration 0080).
package families

import (
	"database/sql"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

// PrivacyNoticeVersion names the notice the portals show. Bump it whenever the
// wording changes, so each record says which text was agreed to. The current
// text is a draft awaiting the academy's approval.
const PrivacyNoticeVersion = "2026-10-15-draft"

// RecordConsent writes one agreement inside the entry's transaction, so an
// entry never exists without the record of what was agreed for it.
func RecordConsent(tx *sql.Tx, purpose, registrationID, userAccountID, givenBy string) error {
	_, err := tx.Exec(`INSERT INTO consent_record
		(consent_id, purpose, notice_version, tournament_registration_id, user_account_id, given_by_name)
		VALUES (?, ?, ?, ?, ?, ?)`,
		db.NewID("cons"), purpose, PrivacyNoticeVersion, registrationID, db.NullIfEmpty(userAccountID), givenBy)
	return err
}
