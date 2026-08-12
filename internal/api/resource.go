// Generic CRUD engine: every ER entity is described as a Resource and served
// by the same list/get/create/update/delete handlers. Authorization is
// decided per verb from the caller's role; row visibility for non-staff roles
// is enforced here in the query (WHERE scope), not in the frontends.
package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/auth"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

type Col struct {
	Name     string
	Kind     string // text | int | real | bool
	Enum     []string
	Required bool
}

// ScopeFn returns a WHERE fragment + args restricting rows to what the caller
// may see, or ("", nil) for unrestricted.
type ScopeFn func(id *auth.Identity) (string, []any)

// OwnFn reports whether a non-staff caller may create/update this row.
type OwnFn func(d *sql.DB, id *auth.Identity, row map[string]any) bool

type Resource struct {
	Name     string // URL segment
	Table    string
	IDCol    string
	IDPrefix string
	Cols     []Col
	ReadRoles  []string // roles beyond staff that may read (scoped)
	WriteRoles []string // roles beyond staff that may create/update
	Scope      map[string]ScopeFn
	Own        OwnFn
}

func isStaff(role string) bool { return role == "Admin" || role == "Receptionist" }

func (rs *Resource) col(name string) *Col {
	for i := range rs.Cols {
		if rs.Cols[i].Name == name {
			return &rs.Cols[i]
		}
	}
	return nil
}

func newID(prefix string) string {
	raw := make([]byte, 5)
	rand.Read(raw)
	return prefix + "_" + hex.EncodeToString(raw)
}

func (rs *Resource) canRead(role string) bool {
	return isStaff(role) || slices.Contains(rs.ReadRoles, role)
}

func (rs *Resource) canWrite(role string) bool {
	return isStaff(role) || slices.Contains(rs.WriteRoles, role)
}

func (rs *Resource) scopeFor(id *auth.Identity) (string, []any) {
	if isStaff(id.Role) || rs.Scope == nil {
		return "", nil
	}
	if fn, ok := rs.Scope[id.Role]; ok {
		return fn(id)
	}
	return "", nil
}

// validate checks enum membership and required columns (create only).
func (rs *Resource) validate(row map[string]any, create bool) error {
	for name := range row {
		if rs.col(name) == nil {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	for _, c := range rs.Cols {
		v, present := row[c.Name]
		if create && c.Required && (!present || v == nil || v == "") {
			return fmt.Errorf("%s is required", c.Name)
		}
		if present && len(c.Enum) > 0 && v != nil {
			s, _ := v.(string)
			if !slices.Contains(c.Enum, s) {
				return fmt.Errorf("%s must be one of %s", c.Name, strings.Join(c.Enum, ", "))
			}
		}
	}
	return nil
}

func (rs *Resource) selectCols() string {
	names := []string{rs.IDCol}
	for _, c := range rs.Cols {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

func (rs *Resource) scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, _ := rows.Columns()
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (rs *Resource) handleList(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !rs.canRead(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		where := []string{}
		args := []any{}
		if frag, a := rs.scopeFor(id); frag != "" {
			where = append(where, frag)
			args = append(args, a...)
		}
		for name, vals := range r.URL.Query() {
			if rs.col(name) == nil && name != rs.IDCol {
				continue // ignore unknown query params (e.g. cache busters)
			}
			where = append(where, name+" = ?")
			args = append(args, vals[0])
		}
		q := "SELECT " + rs.selectCols() + " FROM " + rs.Table
		if len(where) > 0 {
			q += " WHERE " + strings.Join(where, " AND ")
		}
		q += " ORDER BY " + rs.IDCol
		rows, err := d.Query(q, args...)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		defer rows.Close()
		list, err := rs.scanRows(rows)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		httpx.JSON(w, http.StatusOK, list)
	}
}

func (rs *Resource) fetch(d *sql.DB, id *auth.Identity, rowID string) (map[string]any, int, error) {
	where := rs.IDCol + " = ?"
	args := []any{rowID}
	if frag, a := rs.scopeFor(id); frag != "" {
		where += " AND " + frag
		args = append(args, a...)
	}
	rows, err := d.Query("SELECT "+rs.selectCols()+" FROM "+rs.Table+" WHERE "+where, args...)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	defer rows.Close()
	list, err := rs.scanRows(rows)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if len(list) == 0 {
		return nil, http.StatusNotFound, fmt.Errorf("not found")
	}
	return list[0], http.StatusOK, nil
}

func (rs *Resource) handleGet(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !rs.canRead(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		row, status, err := rs.fetch(d, id, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, status, "not found", err)
			return
		}
		httpx.JSON(w, http.StatusOK, row)
	}
}

func (rs *Resource) handleCreate(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !rs.canWrite(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		row := map[string]any{}
		if err := httpx.Decode(r, &row); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		rowID, _ := row[rs.IDCol].(string)
		delete(row, rs.IDCol)
		if err := rs.validate(row, true); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if rowID == "" {
			if rs.IDPrefix == "" {
				httpx.Error(w, http.StatusBadRequest, rs.IDCol+" is required", nil)
				return
			}
			rowID = newID(rs.IDPrefix)
		}
		if !isStaff(id.Role) && rs.Own != nil {
			// Ownership checks need the id column too (it IS the owner key for
			// keyed resources like practice_settings / notification_preference).
			ownRow := map[string]any{rs.IDCol: rowID}
			for k, v := range row {
				ownRow[k] = v
			}
			if !rs.Own(d, id, ownRow) {
				httpx.Error(w, http.StatusForbidden, "not allowed for this record", nil)
				return
			}
		}
		names := []string{rs.IDCol}
		marks := []string{"?"}
		args := []any{rowID}
		for name, v := range row {
			names = append(names, name)
			marks = append(marks, "?")
			args = append(args, v)
		}
		_, err := d.Exec("INSERT INTO "+rs.Table+" ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "could not create record (check references and uniqueness)", err)
			return
		}
		created, _, err := rs.fetch(d, id, rowID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "created but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusCreated, created)
	}
}

func (rs *Resource) handleUpdate(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !rs.canWrite(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		existing, status, err := rs.fetch(d, id, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, status, "not found", err)
			return
		}
		if !isStaff(id.Role) && rs.Own != nil && !rs.Own(d, id, existing) {
			httpx.Error(w, http.StatusForbidden, "not allowed for this record", nil)
			return
		}
		patch := map[string]any{}
		if err := httpx.Decode(r, &patch); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid body", err)
			return
		}
		delete(patch, rs.IDCol)
		if err := rs.validate(patch, false); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if len(patch) == 0 {
			httpx.JSON(w, http.StatusOK, existing)
			return
		}
		sets := []string{}
		args := []any{}
		for name, v := range patch {
			sets = append(sets, name+" = ?")
			args = append(args, v)
		}
		args = append(args, r.PathValue("id"))
		if _, err := d.Exec("UPDATE "+rs.Table+" SET "+strings.Join(sets, ", ")+" WHERE "+rs.IDCol+" = ?", args...); err != nil {
			httpx.Error(w, http.StatusBadRequest, "could not update record", err)
			return
		}
		updated, _, err := rs.fetch(d, id, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "updated but could not reload", err)
			return
		}
		httpx.JSON(w, http.StatusOK, updated)
	}
}

// Deletes are staff-only across the board.
func (rs *Resource) handleDelete(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		res, err := d.Exec("DELETE FROM "+rs.Table+" WHERE "+rs.IDCol+" = ?", r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusConflict, "cannot delete: record is referenced by other data", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// Mount registers the five routes for a resource on the mux.
func (rs *Resource) Mount(mux *http.ServeMux, d *sql.DB) {
	base := "/api/v1/" + rs.Name
	mux.HandleFunc("GET "+base, rs.handleList(d))
	mux.HandleFunc("POST "+base, rs.handleCreate(d))
	mux.HandleFunc("GET "+base+"/{id}", rs.handleGet(d))
	mux.HandleFunc("PATCH "+base+"/{id}", rs.handleUpdate(d))
	mux.HandleFunc("DELETE "+base+"/{id}", rs.handleDelete(d))
}
