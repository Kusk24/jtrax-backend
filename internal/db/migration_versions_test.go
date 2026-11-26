// Applied migration filenames are permanent. New migrations take the next version.
package db

import (
	"regexp"
	"strconv"
	"testing"
)

func TestMigrationVersionOrder(t *testing.T) {
	// Both names in each pair have already shipped. The migration runner records
	// full filenames, so renaming these would replay SQL on existing databases.
	historicalPairs := map[string]string{
		"0018_notifications.sql":                             "0018_school_and_payment_status.sql",
		"0035_a_package_can_have_no_expiry.sql":              "0035_a_tournament_chooses_what_students_pay.sql",
		"0036_a_registration_carries_an_id_photo.sql":        "0036_every_entry_is_accepted.sql",
		"0037_a_category_is_its_own_chess_results_event.sql": "0037_a_venue_carries_a_map_link.sql",
		"0041_a_session_token_is_stored_hashed.sql":          "0041_an_announcement_has_an_audience.sql",
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	nameFormat := regexp.MustCompile(`^([0-9]{4})_[a-z0-9]+(_[a-z0-9]+)*\.sql$`)
	var previousName string
	var previousVersion int
	for _, entry := range entries { // ReadDir returns the same filename order used at startup.
		name := entry.Name()
		match := nameFormat.FindStringSubmatch(name)
		if match == nil {
			t.Fatalf("migration %q must use NNNN_lowercase_name.sql", name)
		}
		version, _ := strconv.Atoi(match[1])
		if historicalPairs[previousName] != name && version != previousVersion+1 {
			t.Fatalf("migration %q after %q must use version %04d", name, previousName, previousVersion+1)
		}
		previousName, previousVersion = name, version
	}
}
