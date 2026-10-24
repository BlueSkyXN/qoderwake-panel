package cli

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
	"testing"
)

func TestFilesAreOwnedAndUploadsUseExpectedOffset(t *testing.T) {
	f, e := OpenFiles(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	slot, e := f.Create(owner, "input.json", false)
	if e != nil {
		t.Fatal(e)
	}
	next, e := f.Append(owner, slot.ID, 0, base64.StdEncoding.EncodeToString([]byte("sample")))
	if e != nil || next != 6 {
		t.Fatal(e)
	}
	_, e = f.Append(owner, slot.ID, 0, "eA==")
	var ae *apierror.Error
	if !errors.As(e, &ae) || ae.Code != "cli_upload_offset_changed" {
		t.Fatal("stale offset accepted", e)
	}
	other := auth.Identity{Role: "admin", Principal: "user:other"}
	if _, _, e = f.Read(other, slot.ID); e == nil {
		t.Fatal("other owner file readable")
	}
	data, name, e := f.Read(owner, slot.ID)
	if e != nil || string(data) != "sample" || name != "input.json" {
		t.Fatal("file content mismatch", e)
	}
	if e = f.Delete(owner, slot.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.Read(owner, slot.ID); e == nil {
		t.Fatal("deleted slot readable")
	}
}
func TestFileOutputAndSymlinkBoundaries(t *testing.T) {
	root := t.TempDir()
	f, e := OpenFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	slot, e := f.Create(owner, "result.txt", true)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = f.Read(owner, slot.ID)
	var ae *apierror.Error
	if !errors.As(e, &ae) || ae.Code != "cli_file_not_ready" {
		t.Fatal(e)
	}
	path, e := f.Path(owner, slot.ID)
	if e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if e = os.WriteFile(outside, []byte("outside data"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, path); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.Read(owner, slot.ID); e == nil {
		t.Fatal("symlink file accepted")
	}
	if _, e = f.Create(owner, "../escape", false); e == nil {
		t.Fatal("path escape accepted")
	}
	if _, e = f.List(auth.Identity{Role: "observer", Principal: "user:observer"}); e == nil {
		t.Fatal("observer file access")
	}
}
