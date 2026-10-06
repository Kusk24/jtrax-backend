package api

import (
	"errors"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/academytime"
)

// errDOBTooYoung is the refusal for a date of birth under a year ago.
var errDOBTooYoung = errors.New("the date of birth must be at least a year ago")

// dobTooYoung reports a date of birth less than a year before today at the
// academy, or in the future: nobody that young plays, so it is a misread card
// or a typo. An empty or unreadable date is left to the other checks.
func dobTooYoung(dob string) bool {
	dob = strings.TrimSpace(dob)
	if len(dob) < 10 {
		return false
	}
	latest := academytime.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	return dob[:10] > latest
}

// checkStudentDOB is the students' resource check.
func checkStudentDOB(row map[string]any) error {
	if dob, _ := row["date_of_birth"].(string); dobTooYoung(dob) {
		return errDOBTooYoung
	}
	return nil
}
