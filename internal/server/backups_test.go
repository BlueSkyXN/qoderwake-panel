package server

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupsKeepLegacyContentsPrivateAndDoNotFollowLinks(t *testing.T) {
	s, _ := testServer(t)
	for path, body := range map[string]string{s.Providers.Path: `{"providers":{}}`, filepath.Join(s.Config.Home, ".auth/token"): "synthetic-auth-token", filepath.Join(s.Config.Home, "config/settings.json"): `{"example":true}`, filepath.Join(s.Config.Home, "unrelated.txt"): "must-not-copy"} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "other-secret")
	os.WriteFile(outside, []byte("must-not-copy"), 0600)
	os.Symlink(outside, filepath.Join(s.Config.Home, ".auth/link"))
	name, err := s.createBackup()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Config.Root, "backups", name)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("backup mode is not private")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := map[string]bool{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		found[h.Name] = true
		data, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if string(data) == "must-not-copy" {
			t.Fatal("backup escaped configured sources")
		}
		if h.Name == "auth/link" && h.Typeflag != tar.TypeSymlink {
			t.Fatal("backup followed auth link")
		}
	}
	for _, name := range []string{"settings.json", "auth/token", "daemon-settings.json"} {
		if !found[name] {
			t.Fatal("legacy backup member missing", name)
		}
	}
	if err = s.deleteBackup("../" + name); err == nil {
		t.Fatal("backup path escaped")
	}
	symlink := filepath.Join(s.Config.Root, "backups", "qw-backup-link.tar.gz")
	os.Symlink(path, symlink)
	if err = s.deleteBackup(filepath.Base(symlink)); err == nil {
		t.Fatal("backup symlink accepted")
	}
	if err = s.deleteBackup(name); err != nil {
		t.Fatal(err)
	}
}
