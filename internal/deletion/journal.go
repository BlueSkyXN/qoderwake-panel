package deletion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/jsoncompat"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

type Journal struct {
	DB  *sql.DB
	Now func() time.Time
}
type Preview struct {
	Kind, Target, Revision, ImpactDigest string
	WarningsRequired                     []string
	Expires                              float64
}
type Operation struct {
	Kind    string         `json:"kind"`
	Target  string         `json:"target"`
	Status  string         `json:"status"`
	Result  map[string]any `json:"result"`
	Created float64        `json:"created"`
	Updated float64        `json:"updated"`
}

func NewJournal(db *sql.DB) *Journal { return &Journal{db, time.Now} }
func (j *Journal) now() float64      { return float64(j.Now().UnixNano()) / 1e9 }
func (j *Journal) Create(principal string, impact Impact) (string, float64, error) {
	raw, err := auth.Random(32)
	if err != nil {
		return "", 0, err
	}
	now := j.now()
	warnings, _ := json.Marshal(sortedSet(impact.WarningsRequired))
	tx, err := j.DB.Begin()
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM deletion_previews WHERE expires<? OR consumed IS NOT NULL", now); err != nil {
		return "", 0, err
	}
	expires := now + 300
	_, err = tx.Exec("INSERT INTO deletion_previews(id_hash,principal,kind,target,revision,impact_digest,warnings,expires,consumed) VALUES(?,?,?,?,?,?,?,?,NULL)", auth.Digest(raw), principal, impact.Kind, impact.Target, impact.Revision, impact.ImpactDigest, string(warnings), expires)
	if err != nil {
		return "", 0, err
	}
	return raw, expires, tx.Commit()
}
func (j *Journal) Preview(raw, principal, kind, target string) (Preview, error) {
	if len(raw) < 20 || len(raw) > 256 {
		return Preview{}, apierror.New(409, "invalid_deletion_preview")
	}
	var p Preview
	var warnings string
	var consumed sql.NullFloat64
	err := j.DB.QueryRow("SELECT kind,target,revision,impact_digest,warnings,expires,consumed FROM deletion_previews WHERE id_hash=? AND principal=?", auth.Digest(raw), principal).Scan(&p.Kind, &p.Target, &p.Revision, &p.ImpactDigest, &warnings, &p.Expires, &consumed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Preview{}, err
	}
	if errors.Is(err, sql.ErrNoRows) || p.Expires <= j.now() || consumed.Valid || p.Kind != kind || p.Target != target {
		return Preview{}, apierror.New(409, "invalid_deletion_preview")
	}
	if err != nil {
		return Preview{}, err
	}
	if err = json.Unmarshal([]byte(warnings), &p.WarningsRequired); err != nil {
		return Preview{}, err
	}
	return p, nil
}
func requestDigest(kind, target, impact string, warnings []string) string {
	return auth.Digest(string(jsoncompat.Compact(map[string]any{"kind": kind, "target": target, "impactDigest": impact, "warnings": sortedSet(warnings)}, true)))
}
func (j *Journal) Unresolved(kind, target string) (string, error) {
	var status string
	err := j.DB.QueryRow("SELECT status FROM deletion_operations WHERE kind=? AND target=? AND status IN ('pending','unknown') ORDER BY created DESC LIMIT 1", kind, target).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}
func (j *Journal) Operation(principal, key string) (Operation, error) {
	if !keyPattern.MatchString(key) {
		return Operation{}, apierror.Invalid("invalid_idempotency_key")
	}
	var result Operation
	var raw string
	err := j.DB.QueryRow("SELECT kind,target,status,result,created,updated FROM deletion_operations WHERE principal=? AND idempotency_key=?", principal, key).Scan(&result.Kind, &result.Target, &result.Status, &raw, &result.Created, &result.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return result, apierror.New(404, "deletion_operation_not_found")
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(raw), &result.Result)
	return result, err
}
func replay(row interface{ Scan(...any) error }, digest string) (map[string]any, error) {
	var previous, status, raw string
	err := row.Scan(&previous, &status, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if previous != digest {
		return nil, apierror.New(409, "idempotency_key_conflict")
	}
	var result map[string]any
	if err = json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	result["replay"] = true
	return result, nil
}
func (j *Journal) Replay(principal, key, kind, target, impact string, warnings []string) (map[string]any, error) {
	if !keyPattern.MatchString(key) {
		return nil, apierror.Invalid("invalid_idempotency_key")
	}
	return replay(j.DB.QueryRow("SELECT request_digest,status,result FROM deletion_operations WHERE principal=? AND idempotency_key=?", principal, key), requestDigest(kind, target, impact, warnings))
}
func (j *Journal) Begin(raw, principal, kind, target, impact string, warnings []string, key string) (map[string]any, error) {
	previous, err := j.Replay(principal, key, kind, target, impact, warnings)
	if err != nil || previous != nil {
		return previous, err
	}
	p, err := j.Preview(raw, principal, kind, target)
	if err != nil {
		return nil, err
	}
	if p.ImpactDigest != impact {
		return nil, apierror.New(409, "stale_deletion_preview")
	}
	required := sortedSet(p.WarningsRequired)
	if !slices.Equal(required, sortedSet(warnings)) {
		return nil, apierror.New(409, "deletion_warnings_unconfirmed")
	}
	digest := requestDigest(kind, target, impact, required)
	tx, err := j.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	previous, err = replay(tx.QueryRow("SELECT request_digest,status,result FROM deletion_operations WHERE principal=? AND idempotency_key=?", principal, key), digest)
	if err != nil || previous != nil {
		return previous, err
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM deletion_operations WHERE kind=? AND target=? AND status IN ('pending','unknown')", kind, target).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, apierror.New(409, "deletion_operation_unresolved")
	}
	now := j.now()
	updated, err := tx.Exec("UPDATE deletion_previews SET consumed=? WHERE id_hash=? AND principal=? AND consumed IS NULL AND expires>?", now, auth.Digest(raw), principal, now)
	if err != nil {
		return nil, err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, apierror.New(409, "invalid_deletion_preview")
	}
	_, err = tx.Exec("INSERT INTO deletion_operations(principal,idempotency_key,request_digest,kind,target,status,result,created,updated) VALUES(?,?,?,?,?,'pending',?, ?,?)", principal, key, digest, kind, target, `{"ok":false,"status":"pending"}`, now, now)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}
func (j *Journal) Finish(principal, key, status string, result map[string]any) error {
	if !slices.Contains([]string{"succeeded", "failed", "pending", "unknown"}, status) {
		return apierror.Invalid("invalid_deletion_status")
	}
	raw, _ := json.Marshal(result)
	updated, err := j.DB.Exec("UPDATE deletion_operations SET status=?,result=?,updated=? WHERE principal=? AND idempotency_key=? AND status='pending'", status, string(raw), j.now(), principal, key)
	if err != nil {
		return err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apierror.Invalid("deletion_operation_not_pending")
	}
	return nil
}
func (j *Journal) Resolve(kind, target, status string, result map[string]any) error {
	if status != "succeeded" && status != "failed" {
		return apierror.Invalid("invalid_deletion_status")
	}
	raw, _ := json.Marshal(result)
	updated, err := j.DB.Exec("UPDATE deletion_operations SET status=?,result=?,updated=? WHERE rowid=(SELECT rowid FROM deletion_operations WHERE kind=? AND target=? AND status IN ('pending','unknown') ORDER BY created DESC LIMIT 1)", status, string(raw), j.now(), kind, target)
	if err != nil {
		return err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apierror.New(404, "deletion_operation_not_found")
	}
	return nil
}
