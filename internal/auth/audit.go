package auth

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"qoderwake-panel/internal/apierror"
	"strings"
	"time"
)

type Caller struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Wakers  []string `json:"wakers"`
	Quota   int      `json:"quota"`
	Used    int      `json:"used"`
	Expires float64  `json:"expires"`
	Revoked bool     `json:"revoked"`
}

func (s *Store) CreateCaller(name string, wakers []string, quota, days int) (map[string]string, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 80 {
		return nil, apierror.Invalid("invalid_caller_name")
	}
	if len(wakers) < 1 {
		return nil, apierror.Invalid("invalid_waker_scope")
	}
	_, _, ws, err := fields("caller", "caller", "maintainer", wakers)
	if err != nil {
		return nil, err
	}
	if quota < 1 || quota > 100000 || days < 1 || days > 365 {
		return nil, apierror.Invalid("invalid_caller_quota")
	}
	id, err := randomID(12)
	if err != nil {
		return nil, err
	}
	raw, err := Random(32)
	if err != nil {
		return nil, err
	}
	raw = "qwc_" + raw
	_, err = s.DB.Exec("INSERT INTO callers(id,name,token_hash,wakers,quota,expires) VALUES(?,?,?,?,?,?)", id, name, Digest(raw), ws, quota, timestamp(s.now().Add(time.Duration(days)*24*time.Hour)))
	if err != nil {
		return nil, err
	}
	return map[string]string{"id": id, "token": raw}, nil
}
func (s *Store) Callers() ([]Caller, error) {
	rows, err := s.DB.Query("SELECT id,name,wakers,quota,used,expires,revoked FROM callers ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Caller{}
	for rows.Next() {
		var c Caller
		var ws string
		if err = rows.Scan(&c.ID, &c.Name, &ws, &c.Quota, &c.Used, &c.Expires, &c.Revoked); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(ws), &c.Wakers); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) RevokeCaller(id string) error {
	r, err := s.DB.Exec("UPDATE callers SET revoked=1 WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return apierror.Invalid("caller_not_found")
	}
	return err
}
func (s *Store) ConsumeCaller(identity Identity, waker string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ws string
	var quota, used int
	var expires float64
	var revoked bool
	err = tx.QueryRow("SELECT wakers,quota,used,expires,revoked FROM callers WHERE id=?", identity.CallerID).Scan(&ws, &quota, &used, &expires, &revoked)
	if err == sql.ErrNoRows || revoked || expires <= timestamp(s.now()) {
		return apierror.New(401, "invalid_credentials")
	}
	if err != nil {
		return err
	}
	var wakers []string
	if err = json.Unmarshal([]byte(ws), &wakers); err != nil {
		return err
	}
	found := false
	for _, w := range wakers {
		found = found || w == waker
	}
	if !found {
		return apierror.New(403, "waker_scope_rejected")
	}
	if used >= quota {
		return apierror.New(429, "caller_quota_exhausted")
	}
	if _, err = tx.Exec("UPDATE callers SET used=used+1 WHERE id=?", identity.CallerID); err != nil {
		return err
	}
	return tx.Commit()
}
func clip(value string, n int) string {
	r := []rune(value)
	if len(r) > n {
		return string(r[:n])
	}
	return value
}
func (s *Store) Audit(ip string, identity Identity, action, target string, status int, ok bool, waker, before, after string) error {
	if identity.Principal == "" {
		identity.Principal = "anonymous"
	}
	if identity.Role == "" {
		identity.Role = "anonymous"
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO audit(ts,ip,principal,role,action,target,status,ok,resource_waker,before_revision,after_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?)", s.now().UTC().Format(time.RFC3339), clip(ip, 64), identity.Principal, identity.Role, clip(action, 64), clip(target, 128), status, ok, clip(waker, 100), clip(before, 200), clip(after, 200))
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM audit WHERE rowid < (SELECT MAX(rowid)-10000 FROM audit)"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Recent(identity Identity) ([]map[string]any, error) {
	where := ""
	args := []any{}
	out := []map[string]any{}
	if identity.UserID != "" && identity.Role != "admin" {
		if len(identity.Wakers) == 0 {
			return out, nil
		}
		marks := make([]string, len(identity.Wakers))
		for i, w := range identity.Wakers {
			marks[i] = "?"
			args = append(args, w)
		}
		where = " WHERE a.resource_waker IN (" + strings.Join(marks, ",") + ")"
	}
	rows, err := s.DB.Query(`SELECT a.ts,a.ip,a.principal,a.role,a.action,a.target,a.status,a.ok,a.resource_waker,a.before_revision,a.after_revision,u.display_name FROM audit a LEFT JOIN users u ON a.principal='user:'||u.id`+where+" ORDER BY a.rowid DESC LIMIT 50", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{"ts", "ip", "principal", "role", "action", "target", "status", "ok", "wakerId", "beforeRevision", "afterRevision", "displayName"}
	for rows.Next() {
		values := make([]any, len(keys))
		pointers := make([]any, len(keys))
		for i := range pointers {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, k := range keys {
			m[k] = values[i]
		}
		if identity.Role != "admin" {
			delete(m, "ip")
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) HasUnresolvedDeletions() (bool, error) {
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM deletion_operations WHERE status IN ('pending','unknown')").Scan(&n)
	return n > 0, err
}
func (s *Store) String() string { return fmt.Sprintf("Panel auth store (%s)", "SQLite") }
