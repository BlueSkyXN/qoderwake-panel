package deletion

import (
	"errors"
	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"testing"
	"time"
)

func journalFixture(t *testing.T) (*Journal, Impact) {
	t.Helper()
	store, e := auth.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	scanner := Scanner{Source: func(name, target string) (any, error) {
		switch name {
		case "waker_detail":
			return map[string]any{"agentId": target, "skills": []any{}}, nil
		case "activity":
			return map[string]any{"runningConversationTasks": 0, "runningTriggerTasks": 0, "queuedTriggerTasks": 0}, nil
		}
		return []any{}, nil
	}}
	impact, e := scanner.Scan("waker", "w1", "")
	if e != nil || !impact.Deletable {
		t.Fatal("fixture deletion not clear", e)
	}
	return NewJournal(store.DB), impact
}
func assertError(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func TestDeletionPreviewWarningsAndReplay(t *testing.T) {
	j, impact := journalFixture(t)
	raw, _, e := j.Create("user:a", impact)
	if e != nil {
		t.Fatal(e)
	}
	_, e = j.Begin(raw, "user:a", impact.Kind, impact.Target, impact.ImpactDigest, nil, "request-0001")
	assertError(t, e, "deletion_warnings_unconfirmed")
	result, e := j.Begin(raw, "user:a", impact.Kind, impact.Target, impact.ImpactDigest, impact.WarningsRequired, "request-0001")
	if e != nil || result != nil {
		t.Fatal("first operation not admitted", e)
	}
	again, e := j.Begin(raw, "user:a", impact.Kind, impact.Target, impact.ImpactDigest, impact.WarningsRequired, "request-0001")
	if e != nil || again["replay"] != true {
		t.Fatal("replay missing", e)
	}
	_, e = j.Replay("user:a", "request-0001", impact.Kind, "other", impact.ImpactDigest, impact.WarningsRequired)
	assertError(t, e, "idempotency_key_conflict")
	if e = j.Finish("user:a", "request-0001", "unknown", map[string]any{"ok": false, "status": "unknown"}); e != nil {
		t.Fatal(e)
	}
	if status, e := j.Unresolved("waker", "w1"); e != nil || status != "unknown" {
		t.Fatal("unknown deletion lock lost", e)
	}
	if e = j.Resolve("waker", "w1", "succeeded", map[string]any{"ok": true, "status": "succeeded"}); e != nil {
		t.Fatal(e)
	}
	if status, e := j.Unresolved("waker", "w1"); e != nil || status != "" {
		t.Fatal("reconciled lock retained", e)
	}
}
func TestDeletionPlansAreScopedExpiredAndSingleUse(t *testing.T) {
	j, impact := journalFixture(t)
	raw, _, e := j.Create("user:a", impact)
	if e != nil {
		t.Fatal(e)
	}
	_, e = j.Preview(raw, "user:b", "waker", "w1")
	assertError(t, e, "invalid_deletion_preview")
	j.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	_, e = j.Preview(raw, "user:a", "waker", "w1")
	assertError(t, e, "invalid_deletion_preview")
	j.Now = time.Now
	raw, _, e = j.Create("user:a", impact)
	if e != nil {
		t.Fatal(e)
	}
	_, e = j.Begin(raw, "user:a", "waker", "w1", impact.ImpactDigest, impact.WarningsRequired, "request-0001")
	if e != nil {
		t.Fatal(e)
	}
	_, e = j.Preview(raw, "user:a", "waker", "w1")
	assertError(t, e, "invalid_deletion_preview")
	other, _, e := j.Create("user:b", impact)
	if e != nil {
		t.Fatal(e)
	}
	_, e = j.Begin(other, "user:b", "waker", "w1", impact.ImpactDigest, impact.WarningsRequired, "request-0002")
	assertError(t, e, "deletion_operation_unresolved")
}
