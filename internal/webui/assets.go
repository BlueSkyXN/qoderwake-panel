// Package webui embeds the frontend build into the Panel binary.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:public
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "public")
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if _, err := fs.Stat(root, "index.html"); err != nil {
			http.Error(w, "请先构建前端资源后再启动 Panel。", 503)
			return
		}
		if strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.Path, "/.git") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
