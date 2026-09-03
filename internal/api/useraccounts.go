// user_account endpoints are bespoke (staff-only) so password hashes never
// leave the server: input takes a plain "password", output never includes it.
package api

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

var accountRoles = []string{"Admin", "Receptionist", "Teacher", "Parent", "Student"}

func requireStaff(d *sql.DB, w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := requireIdentity(d, w, r)
	if id == nil {
		return nil
	}
	if !isStaff(id.Role) {
		httpx.Error(w, http.StatusForbidden, "not allowed", nil)
		return nil
	}
	return id
}

// validateIdentifierFor returns the canonical sign-in identifier for an account
// of this role, or the reason it is not one.
//
// A student may sign in with a bare ID — `stu_penny_ward` — because a child of
// five has no mailbox and inventing one for them produced an address that could
// never receive the reset link it existed to carry.
//
// Everybody else must give a real address, and the difference is not
// pedantry: **the identifier is the only route back into a locked account.**
// Staff and parents reset their own password through a link; a child cannot, so
// theirs is a job for the front desk. Letting a receptionist be created as
// `desk` would quietly move them into the group who need somebody else to let
// them back in, and nothing at the moment of creation would say so.
func validateIdentifierFor(role, identifier string) (string, error) {
	if role == "Student" {
		return auth.ValidateLoginID(identifier)
	}
	return auth.ValidateEmail(identifier)
}

func accountRow(d *sql.DB, id string) (map[string]any, error) {
	var m = map[string]any{}
	var email, role, name, lang, theme string
	err := d.QueryRow(`SELECT email, role, display_name, language_preference, theme_preference
		FROM user_account WHERE user_account_id = ?`, id).Scan(&email, &role, &name, &lang, &theme)
	if err != nil {
		return nil, err
	}
	m["user_account_id"] = id
	m["email"] = email
	m["role"] = role
	m["display_name"] = name
	m["language_preference"] = lang
	m["theme_preference"] = theme
	return m, nil
}

func mountUserAccounts(mux *http.ServeMux, d *sql.DB) {
	base := "/api/v1/user-accounts"

	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		rows, err := d.Query(`SELECT user_account_id, email, role, display_name,
			language_preference, theme_preference FROM user_account ORDER BY user_account_id`)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, email, role, name, lang, theme string
			if err := rows.Scan(&id, &email, &role, &name, &lang, &theme); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "query failed", err)
				return
			}
			out = append(out, map[string]any{
				"user_account_id": id, "email": email, "role": role,
				"display_name": name, "language_preference": lang, "theme_preference": theme,
			})
		}
		httpx.JSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		var in struct {
			Email       string `json:"email"`
			Password    string `json:"password"`
			Role        string `json:"role"`
			DisplayName string `json:"display_name"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		if in.DisplayName == "" {
			httpx.Error(w, http.StatusBadRequest, "display_name is required", nil)
			return
		}
		ok := false
		for _, rl := range accountRoles {
			ok = ok || rl == in.Role
		}
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "role must be one of "+strings.Join(accountRoles, ", "), nil)
			return
		}
		email, err := validateIdentifierFor(in.Role, in.Email)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		in.Email = email
		if err := auth.ValidatePassword(in.Password); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not hash password", err)
			return
		}
		id := newID("usr")
		if _, err := d.Exec(`INSERT INTO user_account (user_account_id, email, password_hash, role, display_name)
			VALUES (?,?,?,?,?)`, id, in.Email, hash, in.Role, in.DisplayName); err != nil {
			httpx.Error(w, http.StatusBadRequest, "could not create account (that sign-in ID or email is already taken)", err)
			return
		}
		row, _ := accountRow(d, id)
		httpx.JSON(w, http.StatusCreated, row)
	})

	mux.HandleFunc("GET "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		row, err := accountRow(d, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, row)
	})

	mux.HandleFunc("PATCH "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		caller := requireStaff(d, w, r)
		if caller == nil {
			return
		}
		var in struct {
			Email       *string `json:"email"`
			Password    *string `json:"password"`
			DisplayName *string `json:"display_name"`
		}
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		id := r.PathValue("id")
		/* Setting somebody else's password is an admin's job, not the front
		   desk's.

		   It is the one write here that hands over an account rather than
		   editing a record: whoever types the new password can sign in as that
		   person, read every family's details, and act as them. That is a
		   different kind of authority from booking a class, and the academy
		   wants it held by the people who already have the rest of it.

		   Creating an account still is not restricted, because registration is
		   the front desk's actual job — the difference is that a new account
		   belongs to nobody yet, so there is nothing to take over.

		   Changing your *own* is always allowed, so this cannot lock a
		   receptionist out of the one account they are entitled to. */
		if in.Password != nil && caller.Role != "Admin" && caller.UserAccountID != id {
			httpx.Error(w, http.StatusForbidden, "only an admin can reset another account's password", nil)
			return
		}
		if in.Email != nil {
			/* Which rule applies is a fact about the account, and a PATCH body
			   does not carry the role — so it is read, not taken on trust. A
			   caller cannot turn a receptionist into an ID-holder by claiming
			   to be editing a student. */
			var role string
			if err := d.QueryRow(`SELECT role FROM user_account WHERE user_account_id = ?`, id).
				Scan(&role); err != nil {
				httpx.Error(w, http.StatusNotFound, "not found", nil)
				return
			}
			email, err := validateIdentifierFor(role, *in.Email)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
				return
			}
			if _, err := d.Exec(`UPDATE user_account SET email = ? WHERE user_account_id = ?`,
				email, id); err != nil {
				httpx.Error(w, http.StatusBadRequest, "that sign-in ID or email is already taken", err)
				return
			}
		}
		if in.DisplayName != nil {
			d.Exec(`UPDATE user_account SET display_name = ? WHERE user_account_id = ?`, *in.DisplayName, id)
		}
		if in.Password != nil {
			if err := auth.ValidatePassword(*in.Password); err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
				return
			}
			hash, err := auth.HashPassword(*in.Password)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not hash password", err)
				return
			}
			d.Exec(`UPDATE user_account SET password_hash = ? WHERE user_account_id = ?`, hash, id)
		}
		row, err := accountRow(d, id)
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, row)
	})

	mux.HandleFunc("DELETE "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		if requireStaff(d, w, r) == nil {
			return
		}
		d.Exec(`DELETE FROM auth_session WHERE user_account_id = ?`, r.PathValue("id"))
		res, err := d.Exec(`DELETE FROM user_account WHERE user_account_id = ?`, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusConflict, "cannot delete: account is referenced by other data", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
}
