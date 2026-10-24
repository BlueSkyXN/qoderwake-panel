// Package usage counts model turns using the existing incremental SQLite cursors.
package usage

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"qoderwake-panel/internal/jsoncompat"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const MaxScanBytes = 8 * 1024 * 1024
const MaxFileBytes = 2 * 1024 * 1024
const MaxLineBytes = 65536
const GuardBytes = 4096

type ScanStats struct {
	BytesRead        int64 `json:"bytesRead"`
	GuardBytesRead   int64 `json:"guardBytesRead"`
	PendingFiles     int   `json:"pendingFiles"`
	UnavailableFiles int   `json:"unavailableFiles"`
	SkippedLines     int64 `json:"skippedLines"`
	MaxBytes         int64 `json:"maxBytes"`
	Complete         bool  `json:"complete"`
}
type Recent struct {
	Day    string   `json:"day"`
	Run    string   `json:"run"`
	Models []string `json:"models"`
}
type Snapshot struct {
	PerModel [][2]any  `json:"perModel"`
	PerDay   [][2]any  `json:"perDay"`
	Recent   []Recent  `json:"recent"`
	Scan     ScanStats `json:"scan"`
	Note     string    `json:"note"`
}
type probe struct {
	newest int64
	count  int
}
type Store struct {
	DB               *sql.DB
	Runs             string
	MaxScan, MaxFile int64
	mu               sync.Mutex
	cache            *Snapshot
	at               time.Time
	probe            probe
	cancel           context.CancelFunc
	done             chan struct{}
}

func Open(path, runs string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	st, e := file.Stat()
	if e != nil {
		file.Close()
		return nil, e
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("unsafe_usage_database")
	}
	if e = file.Chmod(0600); e != nil {
		file.Close()
		return nil, e
	}
	file.Close()
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(10000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{DB: db, Runs: runs, MaxScan: MaxScanBytes, MaxFile: MaxFileBytes}
	if err = store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}
func (s *Store) migrate() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS usage_events(event_key TEXT PRIMARY KEY,day TEXT,model TEXT,run TEXT,timestamp TEXT);
 CREATE TABLE IF NOT EXISTS usage_files(path TEXT PRIMARY KEY,size INTEGER,mtime INTEGER);
 CREATE TABLE IF NOT EXISTS usage_cursors(path TEXT PRIMARY KEY,device INTEGER,inode INTEGER,offset INTEGER,prefix TEXT,suffix TEXT,discarding INTEGER,skipped INTEGER,size INTEGER,mtime_ns INTEGER);
 CREATE TABLE IF NOT EXISTS usage_occurrences(path TEXT,line_hash TEXT,count INTEGER,PRIMARY KEY(path,line_hash));`)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("PRAGMA table_info(usage_cursors)")
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range []string{"size", "mtime_ns"} {
		if !columns[name] {
			if _, err = tx.Exec("ALTER TABLE usage_cursors ADD COLUMN " + name + " INTEGER"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (s *Store) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		for {
			result, err := s.Scan()
			delay := 15 * time.Second
			if err != nil {
				delay = 30 * time.Second
			} else if !result.Scan.Complete {
				delay = 2 * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
func (s *Store) Close() error {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	return s.DB.Close()
}
func (s *Store) Invalidate() { s.mu.Lock(); s.at = time.Time{}; s.mu.Unlock() }
func (s *Store) signal() probe {
	out := probe{}
	paths, _ := filepath.Glob(filepath.Join(s.Runs, "*", "qodercli.log"))
	for _, path := range paths {
		if st, err := os.Lstat(path); err == nil {
			out.count++
			out.newest = max(out.newest, st.ModTime().UnixNano())
		}
	}
	return out
}
func (s *Store) Data() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && time.Since(s.at) <= 15*time.Second && s.probe == s.signal() {
		return *s.cache, nil
	}
	return s.scan()
}
func (s *Store) Scan() (Snapshot, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.scan() }
func hash(raw []byte) string             { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

type cursor struct {
	device, inode, offset int64
	prefix, suffix        string
	discarding            bool
	skipped               int64
	size, mtime           sql.NullInt64
}
type fileStats struct {
	bytes, guards, skipped int64
	pending                bool
}

var unsafeLog = errors.New("unsafe_usage_log")

func guards(file *os.File, offset int64) (string, string, int64, error) {
	size := min(offset, int64(GuardBytes))
	prefix, suffix := make([]byte, size), make([]byte, size)
	n, e := file.ReadAt(prefix, 0)
	if e != nil && e != io.EOF {
		return "", "", 0, e
	}
	m, e := file.ReadAt(suffix, offset-size)
	if e != nil && e != io.EOF {
		return "", "", 0, e
	}
	return hash(prefix[:n]), hash(suffix[:m]), int64(n + m), nil
}

var eventPattern = regexp.MustCompile(`turn\.started\s+model="([^"]+)"`)
var stampPattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)

func decode(raw []byte) string { return jsoncompat.DecodeUTF8(raw) }

func scanFile(tx *sql.Tx, path string, budget int64) (fileStats, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fileStats{}, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return fileStats{}, err
	}
	info, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || info.Nlink != 1 {
		return fileStats{}, unsafeLog
	}
	var old cursor
	err = tx.QueryRow("SELECT device,inode,offset,prefix,suffix,discarding,skipped,size,mtime_ns FROM usage_cursors WHERE path=?", path).Scan(&old.device, &old.inode, &old.offset, &old.prefix, &old.suffix, &old.discarding, &old.skipped, &old.size, &old.mtime)
	if err != nil && err != sql.ErrNoRows {
		return fileStats{}, err
	}
	var offset, skipped, checked int64
	discarding := false
	if err == nil && old.device == int64(info.Dev) && old.inode == int64(info.Ino) && old.offset >= 0 && old.offset <= st.Size() {
		if old.offset == st.Size() && old.size.Valid && old.size.Int64 == st.Size() && old.mtime.Valid && old.mtime.Int64 == st.ModTime().UnixNano() {
			return fileStats{skipped: old.skipped}, nil
		}
		prefix, suffix, count, e := guards(file, old.offset)
		if e != nil {
			return fileStats{}, e
		}
		checked = count
		if prefix == old.prefix && suffix == old.suffix {
			offset, discarding, skipped = old.offset, old.discarding, old.skipped
		}
	}
	if offset == 0 {
		if _, err = tx.Exec("DELETE FROM usage_occurrences WHERE path=?", path); err != nil {
			return fileStats{}, err
		}
	}
	reader := bufio.NewReader(io.NewSectionReader(file, offset, budget))
	position := offset
	read := int64(0)
	grew := false
	for read < budget {
		raw := make([]byte, 0, min(4096, budget-read))
		limit := min(int64(MaxLineBytes+1), budget-read)
		for int64(len(raw)) < limit {
			c, e := reader.ReadByte()
			if e == io.EOF {
				break
			}
			if e != nil {
				return fileStats{}, e
			}
			raw = append(raw, c)
			if c == '\n' {
				break
			}
		}
		read += int64(len(raw))
		if len(raw) == 0 {
			break
		}
		grew = true
		position += int64(len(raw))
		newline := raw[len(raw)-1] == '\n'
		if discarding {
			offset = position
			discarding = !newline
			continue
		}
		if !newline {
			if len(raw) > MaxLineBytes {
				discarding = true
				skipped++
				offset = position
				continue
			}
			break
		}
		offset = position
		if len(raw) > MaxLineBytes {
			skipped++
			continue
		}
		line := strings.TrimSpace(decode(raw))
		match := eventPattern.FindStringSubmatch(line)
		if len(match) < 2 {
			continue
		}
		digest := hash([]byte(line))
		var occurrence int64
		err = tx.QueryRow("SELECT count FROM usage_occurrences WHERE path=? AND line_hash=?", path, digest).Scan(&occurrence)
		if err != nil && err != sql.ErrNoRows {
			return fileStats{}, err
		}
		run := filepath.Base(filepath.Dir(path))
		key := hash([]byte(run + "\x00" + line + "\x00" + strconv.FormatInt(occurrence, 10)))
		stamp := stampPattern.FindString(line)
		if stamp == "" {
			stamp = st.ModTime().In(time.Local).Format("2006-01-02T15:04:05")
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO usage_events VALUES(?,?,?,?,?)", key, stamp[:10], match[1], run, stamp); err != nil {
			return fileStats{}, err
		}
		if _, err = tx.Exec("INSERT OR REPLACE INTO usage_occurrences VALUES(?,?,?)", path, digest, occurrence+1); err != nil {
			return fileStats{}, err
		}
	}
	prefix, suffix, extra, err := guards(file, offset)
	if err != nil {
		return fileStats{}, err
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO usage_cursors(path,device,inode,offset,prefix,suffix,discarding,skipped,size,mtime_ns) VALUES(?,?,?,?,?,?,?,?,?,?)", path, int64(info.Dev), int64(info.Ino), offset, prefix, suffix, discarding, skipped, st.Size(), st.ModTime().UnixNano())
	if err != nil {
		return fileStats{}, err
	}
	current, err := file.Stat()
	if err != nil {
		return fileStats{}, err
	}
	return fileStats{read, checked + extra, skipped, offset < current.Size() || grew && read >= budget}, nil
}
func (s *Store) scan() (Snapshot, error) {
	result := Snapshot{PerModel: [][2]any{}, PerDay: [][2]any{}, Recent: []Recent{}, Scan: ScanStats{MaxBytes: s.MaxScan}, Note: "按本机 worker 日志统计模型回合；轮转或截断保留既有计数。"}
	tx, err := s.DB.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	paths, err := filepath.Glob(filepath.Join(s.Runs, "*", "qodercli.log"))
	if err != nil {
		return result, err
	}
	for index, path := range paths {
		if result.Scan.BytesRead >= s.MaxScan {
			result.Scan.PendingFiles += len(paths) - index
			break
		}
		if _, err = tx.Exec("SAVEPOINT usage_file"); err != nil {
			return result, err
		}
		value, e := scanFile(tx, path, min(s.MaxFile, s.MaxScan-result.Scan.BytesRead))
		if e != nil {
			var pathErr *os.PathError
			if !errors.Is(e, unsafeLog) && !errors.As(e, &pathErr) {
				return result, e
			}
			if _, err = tx.Exec("ROLLBACK TO usage_file"); err != nil {
				return result, err
			}
			result.Scan.UnavailableFiles++
		} else {
			result.Scan.BytesRead += value.bytes
			result.Scan.GuardBytesRead += value.guards
			result.Scan.SkippedLines += value.skipped
			if value.pending {
				result.Scan.PendingFiles++
			}
		}
		if _, err = tx.Exec("RELEASE usage_file"); err != nil {
			return result, err
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	groups := func(query string) ([][2]any, error) {
		rows, e := s.DB.Query(query)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := [][2]any{}
		for rows.Next() {
			var name any
			var count int64
			if e = rows.Scan(&name, &count); e != nil {
				return nil, e
			}
			out = append(out, [2]any{name, count})
		}
		return out, rows.Err()
	}
	result.PerModel, err = groups("SELECT model,COUNT(*) FROM usage_events GROUP BY model ORDER BY COUNT(*) DESC")
	if err != nil {
		return result, err
	}
	result.PerDay, err = groups("SELECT day,COUNT(*) FROM usage_events WHERE day>=date('now','-29 days') GROUP BY day ORDER BY day")
	if err != nil {
		return result, err
	}
	rows, err := s.DB.Query("SELECT timestamp,run,model FROM usage_events ORDER BY timestamp DESC LIMIT 12")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var row Recent
		var model string
		if err = rows.Scan(&row.Day, &row.Run, &model); err != nil {
			rows.Close()
			return result, err
		}
		row.Models = []string{model}
		result.Recent = append(result.Recent, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	result.Scan.Complete = result.Scan.PendingFiles == 0 && result.Scan.UnavailableFiles == 0 && result.Scan.SkippedLines == 0
	s.cache = &result
	s.at = time.Now()
	s.probe = s.signal()
	return result, nil
}
