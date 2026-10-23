package api

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:ui/dist
var uiFS embed.FS

func (s *Server) uiHandler() http.Handler {
	sub, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		panic(err)
	}
	fsrv := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<h1>monitord</h1><p>Dashboard not built. API: <a href=\"/api/v1/info\">/api/v1/info</a>, <a href=\"/api/v1/charts\">/api/v1/charts</a></p>")
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil { // SPA fallback
			r.URL.Path = "/"
		}
		fsrv.ServeHTTP(w, r)
	})
}
