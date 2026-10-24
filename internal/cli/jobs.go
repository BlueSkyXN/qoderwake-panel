package cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"qoderwake-panel/internal/jsoncompat"
)

const MaxOutput = 2 * 1024 * 1024

var ContextKeys = []string{"QODERWAKE_CONVERSATION_ID", "QODERWAKE_CONVERSATION_KIND", "QODERWAKE_RUN_ID", "QODERWAKE_RUN_TOKEN", "QODERWAKE_PARTICIPANT_ID", "QODER_AGENT_ID", "QODER_SESSION_ID", "QODERWAKE_SESSION_ID", "QODERWAKE_CLOUD_CONVERSATION"}
var requestKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,100}$`)
var wakerRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

type Request struct {
	Operation, Revision, Confirm, Stdin string
	Parameters                          Parameters
	Interactive                         bool
	Timeout                             time.Duration
	Context                             map[string]string
	Raw                                 map[string]any
}
type Result struct {
	ID        string  `json:"id"`
	Operation string  `json:"operation"`
	Waker     string  `json:"waker"`
	Status    string  `json:"status"`
	Created   float64 `json:"created"`
	Updated   float64 `json:"updated"`
	ExitCode  *int    `json:"exit_code"`
	Error     string  `json:"error"`
}
type Output struct {
	Result
	Output          string `json:"output"`
	NextOffset      int64  `json:"nextOffset"`
	OutputAvailable bool   `json:"outputAvailable"`
	Truncated       bool   `json:"truncated"`
	Interactive     bool   `json:"interactive"`
	CanInput        bool   `json:"canInput"`
}
type Detail struct {
	Spec
	Available   bool   `json:"available"`
	Error       string `json:"error,omitempty"`
	CurrentHelp string `json:"currentHelp,omitempty"`
}
type job struct {
	Result
	principal   string
	output      []rune
	offset      int64
	interactive bool
	cancel      context.CancelFunc
	terminal    *os.File
	secrets     []string
}
type probe struct {
	mtime, size int64
	at          time.Time
	detail      Detail
}
type Jobs struct {
	DB           *sql.DB
	Catalog      *Catalog
	Home, Binary string
	key          []byte
	mu           sync.Mutex
	jobs         map[string]*job
	probeMu      sync.Mutex
	probes       map[string]probe
	wg           sync.WaitGroup
	closed       bool
	Guard        func(context.Context, auth.Identity, Spec, Request) (func(), error)
	Resolve      func(auth.Identity, Parameters) (Parameters, error)
	Audit        func(auth.Identity, Result)
}

func OpenJobs(root, home, binary string, catalog *Catalog) (*Jobs, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "operations.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	st, err := file.Stat()
	file.Close()
	if err != nil || !st.Mode().IsRegular() {
		return nil, apierror.Invalid("invalid_cli_database")
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(10000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Jobs, error) { db.Close(); return nil, e }
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,principal TEXT NOT NULL,request_key TEXT NOT NULL,digest TEXT NOT NULL,operation TEXT NOT NULL,waker TEXT NOT NULL,status TEXT NOT NULL,created REAL NOT NULL,updated REAL NOT NULL,exit_code INTEGER,error TEXT NOT NULL DEFAULT '',UNIQUE(principal,request_key));`)
	if err != nil {
		return fail(err)
	}
	if _, err = db.Exec("UPDATE jobs SET status='unknown',error='panel_restarted',updated=? WHERE status IN ('running','queued')", now()); err != nil {
		return fail(err)
	}
	key, err := auth.PrivateToken(filepath.Join(root, "digest-key"))
	if err != nil {
		return fail(err)
	}
	if path, err := exec.LookPath(binary); err == nil {
		binary = path
	}
	return &Jobs{DB: db, Catalog: catalog, Home: home, Binary: binary, key: []byte(key), jobs: map[string]*job{}, probes: map[string]probe{}}, nil
}
func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }
func (j *Jobs) Environment() []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if key == "BUN_BE_BUN" || slices.Contains(ContextKeys, key) || strings.HasPrefix(key, "QODER_SESSION_") || strings.HasPrefix(key, "QODERWAKE_CONVERSATION_") || strings.HasPrefix(key, "QODERWAKE_GROUP_") || strings.HasPrefix(key, "QODERWAKE_RUN_") || strings.HasPrefix(key, "QODERWAKE_AGENT_") {
			continue
		}
		values[key] = value
	}
	values["QODERWAKE_HOME"] = j.Home
	values["NO_COLOR"] = "1"
	values["TERM"] = "dumb"
	values["COLUMNS"] = "180"
	out := []string{}
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	return out
}

type limitBuffer struct {
	data  []byte
	limit int
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.limit {
		return 0, errors.New("output_limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (j *Jobs) Detail(ctx context.Context, identity auth.Identity, operation string) (Detail, error) {
	spec, err := j.Catalog.Get(operation)
	if err != nil {
		return Detail{}, err
	}
	if err = j.Catalog.Authorize(identity, spec, nil); err != nil {
		return Detail{}, err
	}
	result := Detail{Spec: spec}
	if spec.PanelOnly {
		result.Available = true
		return result, nil
	}
	st, err := os.Stat(j.Binary)
	if err != nil {
		result.Error = "cli_binary_unavailable"
		return result, nil
	}
	j.probeMu.Lock()
	defer j.probeMu.Unlock()
	if p, ok := j.probes[operation]; ok && p.mtime == st.ModTime().UnixNano() && p.size == st.Size() && time.Since(p.at) < 5*time.Minute {
		return p.detail, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(append([]string{}, spec.Path...), "--help")
	cmd := exec.CommandContext(ctx, j.Binary, args...)
	cmd.Env = j.Environment()
	stdout := &limitBuffer{limit: 131072}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	err = cmd.Run()
	result.Error = "cli_help_unavailable"
	if err == nil {
		current, e := ParseHelp(spec.Path, string(stdout.data))
		if e == nil {
			if current.Revision == spec.Revision {
				result.Available = true
				result.Error = ""
			} else {
				result.Error = "cli_contract_changed"
				result.CurrentHelp = string(stdout.data)
			}
		}
	}
	j.probes[operation] = probe{st.ModTime().UnixNano(), st.Size(), time.Now(), result}
	return result, nil
}
func DecodeRequest(body map[string]any) (Request, error) {
	req := Request{Timeout: 300 * time.Second, Context: map[string]string{}, Raw: body}
	req.Operation, _ = body["operation"].(string)
	req.Revision, _ = body["revision"].(string)
	req.Confirm, _ = body["confirm"].(string)
	raw, exists := body["parameters"]
	if !exists {
		raw = map[string]any{}
	}
	parameters, ok := raw.(map[string]any)
	if !ok {
		return req, apierror.Invalid("invalid_cli_parameters")
	}
	for key := range parameters {
		if key != "arguments" && key != "options" {
			return req, apierror.Invalid("invalid_cli_parameters")
		}
	}
	req.Parameters = Parameters{Arguments: map[string]any{}, Options: map[string]any{}}
	for _, part := range []struct {
		key    string
		target *map[string]any
	}{{"arguments", &req.Parameters.Arguments}, {"options", &req.Parameters.Options}} {
		if raw, exists := parameters[part.key]; exists {
			value, ok := raw.(map[string]any)
			if !ok {
				return req, apierror.Invalid("invalid_cli_parameters")
			}
			*part.target = value
		}
	}
	if v, exists := body["interactive"]; exists {
		var ok bool
		req.Interactive, ok = v.(bool)
		if !ok {
			return req, apierror.Invalid("invalid_cli_execution")
		}
	}
	if v, exists := body["timeout"]; exists {
		number, ok := v.(json.Number)
		if !ok {
			return req, apierror.Invalid("invalid_cli_execution")
		}
		n, e := number.Int64()
		if e != nil || n < 10 || n > 86400 {
			return req, apierror.Invalid("invalid_cli_execution")
		}
		req.Timeout = time.Duration(n) * time.Second
	}
	if v, exists := body["stdin"]; exists {
		var ok bool
		req.Stdin, ok = v.(string)
		if !ok || strings.ContainsRune(req.Stdin, 0) || len(req.Stdin) > 60000 {
			return req, apierror.Invalid("invalid_cli_input")
		}
	}
	if req.Interactive && req.Stdin != "" {
		return req, apierror.Invalid("invalid_cli_input")
	}
	if v, exists := body["context"]; exists {
		values, ok := v.(map[string]any)
		if !ok {
			return req, apierror.Invalid("invalid_cli_context")
		}
		for key, v := range values {
			value, ok := v.(string)
			if !ok || !slices.Contains(ContextKeys, key) || strings.ContainsRune(value, 0) || len([]rune(value)) > 4096 {
				return req, apierror.Invalid("invalid_cli_context")
			}
			req.Context[key] = value
		}
	}
	return req, nil
}
func (j *Jobs) digest(body map[string]any) string {
	h := hmac.New(sha256.New, j.key)
	h.Write(jsoncompat.Marshal(body))
	return hex.EncodeToString(h.Sum(nil))
}
func scanJob(row interface{ Scan(...any) error }) (Result, string, string, error) {
	var result Result
	var principal, digest string
	var code sql.NullInt64
	err := row.Scan(&result.ID, &principal, &digest, &result.Operation, &result.Waker, &result.Status, &result.Created, &result.Updated, &code, &result.Error)
	if code.Valid {
		value := int(code.Int64)
		result.ExitCode = &value
	}
	return result, principal, digest, err
}

const columns = "id,principal,digest,operation,waker,status,created,updated,exit_code,error"

func (j *Jobs) owned(identity auth.Identity, result Result, principal string) error {
	if result.ID == "" || (principal != identity.Principal && identity.Role != "admin") {
		return apierror.New(404, "cli_job_not_found")
	}
	spec, err := j.Catalog.Get(result.Operation)
	if err != nil {
		return err
	}
	if err = j.Catalog.Authorize(identity, spec, nil); err != nil {
		return err
	}
	if identity.Role != "admin" && !slices.Contains(identity.Wakers, result.Waker) {
		return apierror.New(403, "waker_scope_rejected")
	}
	return nil
}
func (j *Jobs) Submit(ctx context.Context, identity auth.Identity, req Request, key string) (Result, error) {
	if !requestKeyRE.MatchString(key) {
		return Result{}, apierror.Invalid("cli_request_key_required")
	}
	spec, err := j.Catalog.Get(req.Operation)
	if err != nil {
		return Result{}, err
	}
	if _, err = BuildArgv(spec, req.Parameters); err != nil {
		return Result{}, err
	}
	if err = j.Catalog.Authorize(identity, spec, &req.Parameters); err != nil {
		return Result{}, err
	}
	if spec.PanelOnly {
		return Result{}, apierror.New(409, "cli_use_panel_action")
	}
	if req.Revision != spec.Revision {
		return Result{}, apierror.New(409, "cli_contract_changed")
	}
	if spec.Effect != "read" && req.Confirm != spec.ID {
		return Result{}, apierror.New(409, "confirmation_required")
	}
	if identity.Role != "admin" && (req.Interactive || req.Stdin != "" || len(req.Context) > 0) {
		return Result{}, apierror.New(403, "permission_denied")
	}
	digest := j.digest(req.Raw)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Result{}, apierror.New(503, "maintenance_mode")
	}
	previous, _, oldDigest, err := scanJob(j.DB.QueryRow("SELECT "+columns+" FROM jobs WHERE principal=? AND request_key=?", identity.Principal, key))
	if err == nil {
		if oldDigest != digest {
			return Result{}, apierror.New(409, "idempotency_key_conflict")
		}
		return previous, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	active := 0
	for _, job := range j.jobs {
		if job.Status == "running" || job.Status == "queued" {
			active++
		}
	}
	if active >= 4 {
		return Result{}, apierror.New(429, "concurrency_limited")
	}
	detail, err := j.Detail(ctx, identity, spec.ID)
	if err != nil {
		return Result{}, err
	}
	if !detail.Available {
		return Result{}, apierror.New(409, detail.Error)
	}
	raw, err := auth.Random(24)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256([]byte(raw))
	id := hex.EncodeToString(sum[:16])
	wid, _ := req.Parameters.Options["--waker-id"].(string)
	if !wakerRE.MatchString(wid) {
		wid = ""
	}
	result := Result{ID: id, Operation: spec.ID, Waker: wid, Status: "queued", Created: now(), Updated: now()}
	_, err = j.DB.Exec("INSERT INTO jobs(id,principal,request_key,digest,operation,waker,status,created,updated) VALUES(?,?,?,?,?,?,?,?,?)", id, identity.Principal, key, digest, spec.ID, wid, result.Status, result.Created, result.Updated)
	if err != nil {
		return Result{}, err
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	currentJob := &job{Result: result, principal: identity.Principal, interactive: req.Interactive, cancel: cancel}
	j.jobs[id] = currentJob
	completed := []*job{}
	for _, value := range j.jobs {
		if value.Status != "running" && value.Status != "queued" {
			completed = append(completed, value)
		}
	}
	slices.SortFunc(completed, func(a, b *job) int {
		if a.Created < b.Created {
			return -1
		}
		if a.Created > b.Created {
			return 1
		}
		return 0
	})
	for len(completed) > 100 {
		delete(j.jobs, completed[0].ID)
		completed = completed[1:]
	}
	j.wg.Add(1)
	go j.run(jobCtx, currentJob, identity, spec, req)
	return result, nil
}
func (j *Jobs) List(identity auth.Identity) ([]Result, error) {
	rows, err := j.DB.Query("SELECT "+columns+" FROM jobs WHERE principal=? ORDER BY created DESC LIMIT 50", identity.Principal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Result{}
	for rows.Next() {
		result, principal, _, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		if j.owned(identity, result, principal) == nil {
			out = append(out, result)
		}
	}
	return out, rows.Err()
}
func (j *Jobs) Get(identity auth.Identity, id string, offset int64) (Output, error) {
	if offset < 0 {
		return Output{}, apierror.Invalid("invalid_cli_cursor")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	result, principal, _, err := scanJob(j.DB.QueryRow("SELECT "+columns+" FROM jobs WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Output{}, apierror.New(404, "cli_job_not_found")
	}
	if err != nil {
		return Output{}, err
	}
	if err = j.owned(identity, result, principal); err != nil {
		return Output{}, err
	}
	out := Output{Result: result, NextOffset: offset}
	job := j.jobs[id]
	if job == nil {
		return out, nil
	}
	start := max(job.offset, offset)
	index := start - job.offset
	end := min(index+65536, int64(len(job.output)))
	if index < int64(len(job.output)) {
		out.Output = string(job.output[index:end])
	}
	out.NextOffset = start + int64(len([]rune(out.Output)))
	out.OutputAvailable = true
	out.Truncated = job.offset > 0
	out.Interactive = job.interactive
	out.CanInput = job.terminal != nil && job.Status == "running"
	return out, nil
}
func (j *Jobs) Control(identity auth.Identity, id, action, value string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	result, principal, _, err := scanJob(j.DB.QueryRow("SELECT "+columns+" FROM jobs WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.New(404, "cli_job_not_found")
	}
	if err != nil {
		return err
	}
	if err = j.owned(identity, result, principal); err != nil {
		return err
	}
	job := j.jobs[id]
	if job == nil || (job.Status != "queued" && job.Status != "running") {
		return apierror.New(409, "cli_job_finished")
	}
	if action == "cancel" {
		job.cancel()
		return nil
	}
	if action != "input" || !job.interactive || identity.Role != "admin" {
		return apierror.New(403, "permission_denied")
	}
	if len(value) > 4000 || strings.ContainsRune(value, 0) {
		return apierror.Invalid("invalid_cli_input")
	}
	if job.terminal == nil {
		return apierror.New(409, "cli_input_not_ready")
	}
	if strings.TrimSpace(value) != "" {
		job.secrets = append(job.secrets, strings.TrimSpace(value))
	}
	_, err = job.terminal.WriteString(value)
	return err
}
func (j *Jobs) setState(job *job, status, code string, exit *int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job.Status = status
	job.Error = code
	job.ExitCode = exit
	job.Updated = now()
	_, err := j.DB.Exec("UPDATE jobs SET status=?,updated=?,error=?,exit_code=? WHERE id=?", status, job.Updated, code, exit, job.ID)
	if err != nil {
		job.Error = "cli_record_update_failed"
		job.Status = "unknown"
	}
}
func (j *Jobs) Close() error {
	j.mu.Lock()
	j.closed = true
	for _, job := range j.jobs {
		job.cancel()
	}
	j.mu.Unlock()
	j.wg.Wait()
	return j.DB.Close()
}
