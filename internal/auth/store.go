// Package auth preserves the existing Panel identities, SQLite format and permissions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
	_ "modernc.org/sqlite"
	"qoderwake-panel/internal/apierror"
)

const Cookie = "qwp_session"
const maxTTL = 7 * 24 * time.Hour
const idleTTL = 12 * time.Hour

type Store struct {
	DB            *sql.DB
	admin, viewer string
	now           func() time.Time
}
type User struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Role        string   `json:"role"`
	Wakers      []string `json:"wakers"`
	Enabled     bool     `json:"enabled"`
	Revision    int64    `json:"revision"`
	CreatedAt   float64  `json:"createdAt"`
	UpdatedAt   float64  `json:"updatedAt"`
}
type Identity struct {
	Role, Principal, UserID, Username, DisplayName, Source, CSRF, SID, CallerID string
	Revision                                                                    int64
	Wakers                                                                      []string
}

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var resourceRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func Random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func equal(a, b string) bool        { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func timestamp(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }
func PrivateToken(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		token, e := Random(32)
		if e != nil {
			return "", e
		}
		f, e = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(e, os.ErrExist) {
			return PrivateToken(path)
		}
		if e != nil {
			return "", e
		}
		_, e = f.WriteString(token + "\n")
		closeErr := f.Close()
		if e != nil {
			return "", e
		}
		if closeErr != nil {
			return "", closeErr
		}
		return token, nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("invalid_private_token_file")
	}
	b := make([]byte, 4097)
	n, err := f.Read(b)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b[:n]))
	if len(token) < 16 || strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("invalid_private_token_file")
	}
	return token, nil
}
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "access.db")
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("invalid_access_database")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, now: time.Now}
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	if err = s.migrate(); err != nil {
		return fail(err)
	}
	if s.admin, err = PrivateToken(filepath.Join(root, "admin-token.txt")); err != nil {
		return fail(err)
	}
	if s.viewer, err = PrivateToken(filepath.Join(root, "viewer-token.txt")); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) migrate() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
 CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,role TEXT NOT NULL,credential TEXT NOT NULL,csrf TEXT NOT NULL,created REAL NOT NULL,seen REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS callers(id TEXT PRIMARY KEY,name TEXT NOT NULL,token_hash TEXT UNIQUE NOT NULL,wakers TEXT NOT NULL,quota INTEGER NOT NULL,used INTEGER NOT NULL DEFAULT 0,expires REAL NOT NULL,revoked INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS rates(bucket TEXT NOT NULL,ts REAL NOT NULL);
 CREATE INDEX IF NOT EXISTS rate_bucket ON rates(bucket,ts);
 CREATE TABLE IF NOT EXISTS audit(ts TEXT NOT NULL,ip TEXT NOT NULL,principal TEXT NOT NULL,role TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,status INTEGER NOT NULL,ok INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,username TEXT NOT NULL UNIQUE COLLATE NOCASE,display_name TEXT NOT NULL,password_hash TEXT NOT NULL,role TEXT NOT NULL,wakers TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,auth_version INTEGER NOT NULL DEFAULT 1,created REAL NOT NULL,updated REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS auth_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS deletion_previews(id_hash TEXT PRIMARY KEY,principal TEXT NOT NULL,kind TEXT NOT NULL,target TEXT NOT NULL,revision TEXT NOT NULL,impact_digest TEXT NOT NULL,warnings TEXT NOT NULL,expires REAL NOT NULL,consumed REAL);
 CREATE TABLE IF NOT EXISTS deletion_operations(principal TEXT NOT NULL,idempotency_key TEXT NOT NULL,request_digest TEXT NOT NULL,kind TEXT NOT NULL,target TEXT NOT NULL,status TEXT NOT NULL,result TEXT NOT NULL,created REAL NOT NULL,updated REAL NOT NULL,PRIMARY KEY(principal,idempotency_key));`)
	if err != nil {
		return err
	}
	for _, item := range [][3]string{{"sessions", "user_id", "TEXT"}, {"audit", "resource_waker", "TEXT NOT NULL DEFAULT ''"}, {"audit", "before_revision", "TEXT NOT NULL DEFAULT ''"}, {"audit", "after_revision", "TEXT NOT NULL DEFAULT ''"}} {
		rows, e := tx.Query("PRAGMA table_info(" + item[0] + ")")
		if e != nil {
			return e
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var defaultValue any
			if e = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); e != nil {
				rows.Close()
				return e
			}
			found = found || name == item[1]
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if !found {
			if _, e = tx.Exec("ALTER TABLE " + item[0] + " ADD COLUMN " + item[1] + " " + item[2]); e != nil {
				return e
			}
		}
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS session_user ON sessions(user_id); CREATE INDEX IF NOT EXISTS audit_waker ON audit(resource_waker)`); err != nil {
		return err
	}
	return tx.Commit()
}
func PasswordHash(value string) (string, error) {
	if n := utf8.RuneCountInString(value); n < 10 || n > 1024 {
		return "", apierror.Invalid("password_length_invalid")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	result, err := scrypt.Key([]byte(value), salt, 32768, 8, 1, 64)
	if err != nil {
		return "", err
	}
	return "scrypt$32768$8$1$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(result), nil
}
func PasswordMatches(value, encoded string) bool {
	if utf8.RuneCountInString(value) > 1024 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || strings.Join(parts[:4], "$") != "scrypt$32768$8$1" {
		return false
	}
	salt, e := hex.DecodeString(parts[4])
	if e != nil || len(salt) != 16 {
		return false
	}
	expected, e := hex.DecodeString(parts[5])
	if e != nil || len(expected) != 64 {
		return false
	}
	actual, e := scrypt.Key([]byte(value), salt, 32768, 8, 1, 64)
	return e == nil && subtle.ConstantTimeCompare(actual, expected) == 1
}
func fields(username, name, role string, wakers []string) (string, string, string, error) {
	if !usernameRE.MatchString(username) {
		return "", "", "", apierror.Invalid("invalid_username")
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return "", "", "", apierror.Invalid("invalid_display_name")
	}
	if !slices.Contains([]string{"admin", "maintainer", "observer"}, role) {
		return "", "", "", apierror.Invalid("invalid_user_role")
	}
	if len(wakers) > 100 {
		return "", "", "", apierror.Invalid("invalid_waker_scope")
	}
	for _, w := range wakers {
		if !resourceRE.MatchString(w) {
			return "", "", "", apierror.Invalid("invalid_waker_scope")
		}
	}
	ws := append([]string{}, wakers...)
	slices.Sort(ws)
	ws = slices.Compact(ws)
	if role == "admin" {
		ws = []string{}
	}
	b, _ := json.Marshal(ws)
	return strings.ToLower(username), name, string(b), nil
}
func (s *Store) AccountsMode() (bool, error) {
	var mode string
	err := s.DB.QueryRow("SELECT value FROM auth_settings WHERE key='mode'").Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return mode == "accounts", err
}
func (s *Store) AccountsEnabled() bool {
	enabled, err := s.AccountsMode()
	return enabled || err != nil
}

const userColumns = "id,username,display_name,role,wakers,enabled,auth_version,created,updated"

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var wakers string
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &wakers, &u.Enabled, &u.Revision, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, apierror.New(404, "user_not_found")
	}
	if err == nil {
		err = json.Unmarshal([]byte(wakers), &u.Wakers)
	}
	return u, err
}
func (s *Store) User(id string) (User, error) {
	return scanUser(s.DB.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", id))
}
func (s *Store) Users() ([]User, error) {
	rows, err := s.DB.Query("SELECT " + userColumns + " FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, e := scanUser(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) CreateUser(username, name, role string, wakers []string, password string, bootstrap bool) (User, error) {
	username, name, ws, err := fields(username, name, role, wakers)
	if err != nil {
		return User{}, err
	}
	if bootstrap && role != "admin" {
		return User{}, apierror.Invalid("invalid_user_role")
	}
	if !bootstrap {
		enabled, e := s.AccountsMode()
		if e != nil {
			return User{}, e
		}
		if !enabled {
			return User{}, apierror.New(409, "accounts_not_initialized")
		}
	}
	encoded, err := PasswordHash(password)
	if err != nil {
		return User{}, err
	}
	id, err := randomID(16)
	if err != nil {
		return User{}, err
	}
	now := timestamp(s.now())
	tx, err := s.DB.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if bootstrap {
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
			return User{}, err
		}
		if count > 0 {
			return User{}, apierror.New(409, "admin_already_initialized")
		}
	}
	_, err = tx.Exec("INSERT INTO users(id,username,display_name,password_hash,role,wakers,enabled,auth_version,created,updated) VALUES(?,?,?,?,?,?,1,1,?,?)", id, username, name, encoded, role, ws, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, apierror.New(409, "username_exists")
		}
		return User{}, err
	}
	if bootstrap {
		if _, err = tx.Exec("INSERT OR REPLACE INTO auth_settings VALUES('mode','accounts'); DELETE FROM sessions WHERE user_id IS NULL"); err != nil {
			return User{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return s.User(id)
}
func (s *Store) UpdateUser(id, name, role string, wakers []string, enabled bool, revision int64) (User, error) {
	_, name, ws, err := fields("placeholder", name, role, wakers)
	if err != nil {
		return User{}, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var oldRole string
	var oldEnabled bool
	var rev int64
	err = tx.QueryRow("SELECT role,enabled,auth_version FROM users WHERE id=?", id).Scan(&oldRole, &oldEnabled, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, apierror.New(404, "user_not_found")
	}
	if err != nil {
		return User{}, err
	}
	if rev != revision {
		return User{}, apierror.New(409, "version_conflict")
	}
	if oldRole == "admin" && oldEnabled && (role != "admin" || !enabled) {
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM users WHERE role='admin' AND enabled=1").Scan(&count); err != nil {
			return User{}, err
		}
		if count <= 1 {
			return User{}, apierror.New(409, "last_admin_required")
		}
	}
	if _, err = tx.Exec("UPDATE users SET display_name=?,role=?,wakers=?,enabled=?,auth_version=auth_version+1,updated=? WHERE id=?", name, role, ws, enabled, timestamp(s.now()), id); err != nil {
		return User{}, err
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=?", id); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return s.User(id)
}
func (s *Store) ResetPassword(id, password string, revision *int64) error {
	encoded, err := PasswordHash(password)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rev int64
	err = tx.QueryRow("SELECT auth_version FROM users WHERE id=?", id).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.New(404, "user_not_found")
	}
	if err != nil {
		return err
	}
	if revision != nil && *revision != rev {
		return apierror.New(409, "version_conflict")
	}
	if _, err = tx.Exec("UPDATE users SET password_hash=?,auth_version=auth_version+1,updated=? WHERE id=?", encoded, timestamp(s.now()), id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ChangePassword(identity Identity, current, password string) error {
	identity, err := s.Refresh(identity)
	if err != nil {
		return err
	}
	var encoded string
	if err = s.DB.QueryRow("SELECT password_hash FROM users WHERE id=?", identity.UserID).Scan(&encoded); err != nil || !PasswordMatches(current, encoded) {
		return apierror.New(403, "current_password_invalid")
	}
	return s.ResetPassword(identity.UserID, password, &identity.Revision)
}
func (s *Store) Refresh(identity Identity) (Identity, error) {
	if identity.UserID == "" {
		if identity.Role == "admin" || identity.Role == "viewer" {
			enabled, err := s.AccountsMode()
			if err != nil {
				return Identity{}, err
			}
			if enabled {
				return Identity{}, apierror.New(401, "session_expired")
			}
		}
		return identity, nil
	}
	u, err := s.User(identity.UserID)
	enabled, modeErr := s.AccountsMode()
	if modeErr != nil {
		return Identity{}, modeErr
	}
	if err != nil || !u.Enabled || u.Revision != identity.Revision || !enabled {
		return Identity{}, apierror.New(401, "session_expired")
	}
	identity.Role = u.Role
	identity.Username = u.Username
	identity.DisplayName = u.DisplayName
	identity.Wakers = u.Wakers
	identity.Principal = "user:" + u.ID
	return identity, nil
}
func (s *Store) RoleFor(token string) string {
	if len(token) == 0 || len(token) > 256 || s.AccountsEnabled() {
		return ""
	}
	d := Digest(token)
	if equal(d, Digest(s.admin)) {
		return "admin"
	}
	if equal(d, Digest(s.viewer)) {
		return "viewer"
	}
	return ""
}
func (s *Store) LoginUser(username, password string) (string, Identity, error) {
	var id, encoded string
	var enabled bool
	var rev int64
	mode, modeErr := s.AccountsMode()
	if modeErr != nil {
		return "", Identity{}, modeErr
	}
	if !mode {
		return "", Identity{}, apierror.New(401, "invalid_credentials")
	}
	err := s.DB.QueryRow("SELECT id,password_hash,enabled,auth_version FROM users WHERE username=?", username).Scan(&id, &encoded, &enabled, &rev)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return "", Identity{}, err
		}
		encoded = "scrypt$32768$8$1$" + strings.Repeat("00", 16) + "$" + strings.Repeat("00", 64)
	}
	valid := PasswordMatches(password, encoded)
	if id == "" || !enabled || !valid {
		return "", Identity{}, apierror.New(401, "invalid_credentials")
	}
	identity, err := s.Refresh(Identity{UserID: id, Revision: rev})
	if err != nil {
		return "", Identity{}, apierror.New(401, "invalid_credentials")
	}
	return s.createSession(identity, strconv.FormatInt(rev, 10))
}
func (s *Store) LoginToken(token string) (string, Identity, error) {
	role := s.RoleFor(token)
	if role == "" {
		return "", Identity{}, apierror.New(401, "invalid_credentials")
	}
	return s.createSession(Identity{Role: role}, Digest(token))
}
func (s *Store) createSession(identity Identity, credential string) (string, Identity, error) {
	raw, err := Random(32)
	if err != nil {
		return "", Identity{}, err
	}
	csrf, err := Random(32)
	if err != nil {
		return "", Identity{}, err
	}
	now := s.now()
	tx, err := s.DB.Begin()
	if err != nil {
		return "", Identity{}, err
	}
	defer tx.Rollback()
	if identity.UserID != "" {
		var enabled bool
		var rev int64
		err = tx.QueryRow("SELECT enabled,auth_version FROM users WHERE id=?", identity.UserID).Scan(&enabled, &rev)
		if err != nil || !enabled || rev != identity.Revision {
			return "", Identity{}, apierror.New(401, "invalid_credentials")
		}
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE seen<? OR created<?", timestamp(now.Add(-idleTTL)), timestamp(now.Add(-maxTTL))); err != nil {
		return "", Identity{}, err
	}
	var uid any
	if identity.UserID != "" {
		uid = identity.UserID
	}
	if _, err = tx.Exec("INSERT INTO sessions(id,role,credential,csrf,created,seen,user_id) VALUES(?,?,?,?,?,?,?)", Digest(raw), identity.Role, credential, csrf, timestamp(now), timestamp(now), uid); err != nil {
		return "", Identity{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", Identity{}, err
	}
	identity.Source = "cookie"
	identity.CSRF = csrf
	identity.SID = Digest(raw)
	if identity.Principal == "" {
		identity.Principal = "session:" + identity.SID[:12]
	}
	return raw, identity, nil
}
func (s *Store) Authenticate(r *http.Request) (Identity, error) {
	if header := r.Header.Get("Authorization"); header != "" {
		if len(header) < 7 || !strings.EqualFold(header[:7], "Bearer ") {
			return Identity{}, apierror.New(401, "invalid_credentials")
		}
		token := strings.TrimSpace(header[7:])
		role := s.RoleFor(token)
		if role != "" {
			return Identity{Role: role, Principal: "token:" + Digest(token)[:12], Source: "bearer"}, nil
		}
		var id, ws string
		var expires float64
		var revoked bool
		err := s.DB.QueryRow("SELECT id,wakers,expires,revoked FROM callers WHERE token_hash=?", Digest(token)).Scan(&id, &ws, &expires, &revoked)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Identity{}, err
		}
		if err != nil || revoked || expires <= timestamp(s.now()) {
			return Identity{}, apierror.New(401, "invalid_credentials")
		}
		identity := Identity{Role: "caller", Principal: "caller:" + id, CallerID: id, Source: "bearer"}
		if err = json.Unmarshal([]byte(ws), &identity.Wakers); err != nil {
			return Identity{}, err
		}
		return identity, nil
	}
	cookie, err := r.Cookie(Cookie)
	if err != nil || len(cookie.Value) == 0 || len(cookie.Value) > 256 {
		return Identity{}, apierror.New(401, "authentication_required")
	}
	sid := Digest(cookie.Value)
	var role, credential, csrf string
	var created, seen float64
	var uid sql.NullString
	err = s.DB.QueryRow("SELECT role,credential,csrf,created,seen,user_id FROM sessions WHERE id=?", sid).Scan(&role, &credential, &csrf, &created, &seen, &uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Identity{}, err
	}
	now := timestamp(s.now())
	valid := err == nil && now-created <= maxTTL.Seconds() && now-seen <= idleTTL.Seconds()
	if valid && !uid.Valid {
		token := s.viewer
		if role == "admin" {
			token = s.admin
		}
		valid = !s.AccountsEnabled() && equal(credential, Digest(token))
	}
	if !valid {
		s.DB.Exec("DELETE FROM sessions WHERE id=?", sid)
		return Identity{}, apierror.New(401, "session_expired")
	}
	if now-seen >= 60 {
		if _, err = s.DB.Exec("UPDATE sessions SET seen=? WHERE id=? AND seen<=?", now, sid, now-60); err != nil {
			return Identity{}, err
		}
	}
	identity := Identity{Role: role, Principal: "session:" + sid[:12], Source: "cookie", SID: sid, CSRF: csrf}
	if uid.Valid {
		identity.UserID = uid.String
		identity.Revision, err = strconv.ParseInt(credential, 10, 64)
		if err != nil {
			return Identity{}, apierror.New(401, "session_expired")
		}
		return s.Refresh(identity)
	}
	return identity, nil
}
func (s *Store) Logout(identity Identity) error {
	_, err := s.DB.Exec("DELETE FROM sessions WHERE id=?", identity.SID)
	return err
}
func (s *Store) Limit(bucket string, limit int, window time.Duration) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := s.now()
	if _, err = tx.Exec("DELETE FROM rates WHERE ts<?", timestamp(now.Add(-time.Hour))); err != nil {
		return false, err
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM rates WHERE bucket=? AND ts>?", bucket, timestamp(now.Add(-window))).Scan(&count); err != nil {
		return false, err
	}
	if count >= limit {
		return false, nil
	}
	if _, err = tx.Exec("INSERT INTO rates VALUES(?,?)", bucket, timestamp(now)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
