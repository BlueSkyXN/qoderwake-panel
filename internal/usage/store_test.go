package usage

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testLine = "2026-10-24T08:00:00 turn.started model=\"fixture/模型\"\n"

func fixture(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	path := filepath.Join(runs, "one/qodercli.log")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	store, e := Open(filepath.Join(root, "usage.db"), runs)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func count(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if e := s.DB.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func scan(t *testing.T, s *Store) Snapshot {
	t.Helper()
	value, e := s.Scan()
	if e != nil {
		t.Fatal(e)
	}
	return value
}
func TestAppendPartialRotationAndFastPath(t *testing.T) {
	s, path := fixture(t)
	writeFile(t, path, testLine+testLine)
	scan(t, s)
	if count(t, s) != 2 {
		t.Fatal("duplicate-line events lost")
	}
	var before, after int
	s.DB.QueryRow("SELECT total_changes()").Scan(&before)
	unchanged := scan(t, s)
	s.DB.QueryRow("SELECT total_changes()").Scan(&after)
	if unchanged.Scan.BytesRead != 0 || unchanged.Scan.GuardBytesRead != 0 || before != after {
		t.Fatal("unchanged cursor read or wrote content")
	}
	writeFile(t, path, testLine+testLine+strings.TrimSuffix(testLine, "\n"))
	pending := scan(t, s)
	if count(t, s) != 2 || pending.Scan.PendingFiles != 1 {
		t.Fatal("partial line was counted")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n")
	f.Close()
	scan(t, s)
	if count(t, s) != 3 {
		t.Fatal("completed line missing")
	}
	writeFile(t, path, testLine)
	scan(t, s)
	if count(t, s) != 3 {
		t.Fatal("truncation altered history")
	}
	writeFile(t, path+".new", testLine+strings.Replace(testLine, "08:00:00", "08:01:00", 1))
	os.Rename(path+".new", path)
	scan(t, s)
	if count(t, s) != 4 {
		t.Fatal("rotation recounted history")
	}
	os.Remove(path)
	scan(t, s)
	if count(t, s) != 4 {
		t.Fatal("removed log erased history")
	}
}
func TestBoundedBacklogAndOversizeRecord(t *testing.T) {
	s, path := fixture(t)
	s.MaxScan = 80000
	s.MaxFile = 40000
	writeFile(t, path, strings.Repeat("unrelated log data\n", 20000)+testLine)
	complete := false
	for range 20 {
		value := scan(t, s)
		if value.Scan.BytesRead > 80000 {
			t.Fatal("scan exceeded budget")
		}
		if value.Scan.Complete {
			complete = true
			break
		}
	}
	if !complete || count(t, s) != 1 {
		t.Fatal("backlog did not drain")
	}
	s.MaxScan = MaxScanBytes
	s.MaxFile = MaxFileBytes
	writeFile(t, path, strings.Repeat("x", MaxLineBytes*3)+"\n"+testLine)
	value := scan(t, s)
	if count(t, s) != 1 || value.Scan.SkippedLines != 1 || value.Scan.Complete {
		t.Fatal("oversize record not disclosed")
	}
}
func TestUnsafeLogsDoNotBlockOrFollowLinks(t *testing.T) {
	s, path := fixture(t)
	target := filepath.Join(t.TempDir(), "secret")
	writeFile(t, target, testLine)
	if e := os.Symlink(target, path); e != nil {
		t.Fatal(e)
	}
	if scan(t, s).Scan.UnavailableFiles != 1 || count(t, s) != 0 {
		t.Fatal("followed symlink log")
	}
	os.Remove(path)
	if e := syscall.Mkfifo(path, 0600); e != nil {
		t.Fatal(e)
	}
	if scan(t, s).Scan.UnavailableFiles != 1 {
		t.Fatal("FIFO not reported")
	}
}
func TestPythonGoUsageCursorInterchangeAndMigration(t *testing.T) {
	py, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python reference unavailable")
	}
	root := t.TempDir()
	db := filepath.Join(root, "usage.db")
	runs := filepath.Join(root, "runs")
	path := filepath.Join(runs, "one/qodercli.log")
	os.MkdirAll(filepath.Dir(path), 0700)
	writeFile(t, path, testLine+testLine)
	panel, _ := filepath.Abs("../../panel")
	pythonScan := func() map[string]any {
		t.Helper()
		cmd := exec.Command(py, "-c", `import sys,json
sys.path.insert(0,sys.argv[1])
import usage_store
print(json.dumps(usage_store.usage_data(sys.argv[2],sys.argv[3])))`, panel, db, runs)
		output, e := cmd.Output()
		if e != nil {
			t.Fatal(e)
		}
		var value map[string]any
		if e = json.Unmarshal(output, &value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	pythonScan()
	s, e := Open(db, runs)
	if e != nil {
		t.Fatal(e)
	}
	if value := scan(t, s); value.Scan.BytesRead != 0 || count(t, s) != 2 {
		t.Fatal("Go could not continue Python cursor")
	}
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString(testLine)
	f.Close()
	scan(t, s)
	if count(t, s) != 3 {
		t.Fatal("Go append count mismatch")
	}
	s.Close()
	value := pythonScan()
	if value["scan"].(map[string]any)["bytesRead"] != float64(0) {
		t.Fatal("Python could not continue Go cursor")
	}
	s, e = Open(db, runs)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(`ALTER TABLE usage_cursors RENAME TO prior;CREATE TABLE usage_cursors(path TEXT PRIMARY KEY,device INTEGER,inode INTEGER,offset INTEGER,prefix TEXT,suffix TEXT,discarding INTEGER,skipped INTEGER,size INTEGER);INSERT INTO usage_cursors SELECT path,device,inode,offset,prefix,suffix,discarding,skipped,size FROM prior;DROP TABLE prior;`)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(db, runs)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	scan(t, s)
	if count(t, s) != 3 {
		t.Fatal("interrupted cursor migration recounted history")
	}
}
func TestBackgroundScanStartsWithoutPageRequest(t *testing.T) {
	s, path := fixture(t)
	writeFile(t, path, testLine)
	s.Start()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if count(t, s) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background scan did not start")
}
func TestDecodeMatchesPythonReplacement(t *testing.T) {
	py, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python reference unavailable")
	}
	raw := []byte{0xff, 0xff, 0xe4, 0xb8, ' ', 0xed, 0xa0, 0x80, 0xe0, 0x80, 0x80, 0xf0, 0x90, 0x80}
	cmd := exec.Command(py, "-c", `import sys,json;print(json.dumps(bytes.fromhex(sys.argv[1]).decode('utf8','replace')))`, fmtHex(raw))
	output, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	var expected string
	if e = json.Unmarshal(output, &expected); e != nil {
		t.Fatal(e)
	}
	if decode(raw) != expected {
		t.Fatal("invalid UTF-8 line hashes would differ")
	}
}
func fmtHex(raw []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(raw)*2)
	for i, v := range raw {
		out[2*i] = digits[v>>4]
		out[2*i+1] = digits[v&15]
	}
	return string(out)
}
