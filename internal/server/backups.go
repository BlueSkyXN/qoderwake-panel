package server

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"qoderwake-panel/internal/apierror"
)

func (s *Server) backups() ([]map[string]any, error) {
	root := filepath.Join(s.Config.Root, "backups")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	type entry struct {
		name     string
		size     int64
		modified time.Time
	}
	items := []entry{}
	for _, value := range entries {
		st, e := value.Info()
		if e != nil {
			return nil, e
		}
		if st.Mode().IsRegular() {
			items = append(items, entry{value.Name(), st.Size(), st.ModTime()})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].modified.After(items[j].modified) })
	out := []map[string]any{}
	for _, item := range items[:min(len(items), 30)] {
		out = append(out, map[string]any{"name": item.name, "size": item.size, "mtime": item.modified.In(time.Local).Format("01-02 15:04")})
	}
	return out, nil
}
func addBackup(tw *tar.Writer, source, arc string) error {
	if _, err := os.Lstat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(arc, relative))
		if info.IsDir() {
			header.Name += "/"
		}
		if !info.Mode().IsRegular() {
			return tw.WriteHeader(header)
		}
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		current, err := file.Stat()
		if err != nil {
			return err
		}
		if !current.Mode().IsRegular() || !os.SameFile(info, current) {
			return apierror.Invalid("backup_source_changed")
		}
		header.Size = current.Size()
		if err = tw.WriteHeader(header); err != nil {
			return err
		}
		_, err = io.CopyN(tw, file, current.Size())
		return err
	})
}
func (s *Server) createBackup() (string, error) {
	root := filepath.Join(s.Config.Root, "backups")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	name := "qw-backup-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".tar.gz"
	path := filepath.Join(root, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(path)
		}
	}()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, source := range [][2]string{{s.Providers.Path, "settings.json"}, {filepath.Join(s.Config.Home, ".auth"), "auth"}, {filepath.Join(s.Config.Home, "config/settings.json"), "daemon-settings.json"}} {
		if err = addBackup(tw, source[0], source[1]); err != nil {
			tw.Close()
			gz.Close()
			return "", err
		}
	}
	if err = tw.Close(); err != nil {
		gz.Close()
		return "", err
	}
	if err = gz.Close(); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	complete = true
	return name, nil
}

var backupName = regexp.MustCompile(`^qw-backup-[A-Za-z0-9-]+\.tar\.gz$`)

func (s *Server) deleteBackup(name string) error {
	if !backupName.MatchString(name) || strings.ContainsAny(name, "/\\") {
		return apierror.Invalid("invalid_backup_name")
	}
	path := filepath.Join(s.Config.Root, "backups", name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return apierror.Invalid("backup_not_found")
	}
	return os.Remove(path)
}
