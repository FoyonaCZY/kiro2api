// Package adminui 把管理界面构建产物嵌进二进制。编译前先在本目录跑 pnpm build。
package adminui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

func cacheControl(p string) string {
	switch {
	case strings.HasSuffix(p, ".html"):
		return "no-cache"
	case strings.HasPrefix(p, "assets/"):
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=3600"
}

func contentType(p string) string {
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// Handler 挂在 /admin 下：带扩展名的走静态文件，其余回退 index.html（SPA）。
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	serveIndex := func(w http.ResponseWriter) {
		b, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "Admin UI not built. Run 'pnpm build' in admin-ui directory.", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/admin"), "/")
		if strings.Contains(p, "..") {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		if p == "" {
			serveIndex(w)
			return
		}
		if b, err := fs.ReadFile(root, p); err == nil {
			w.Header().Set("Content-Type", contentType(p))
			w.Header().Set("Cache-Control", cacheControl(p))
			_, _ = w.Write(b)
			return
		}
		if !strings.Contains(path.Base(p), ".") {
			serveIndex(w)
			return
		}
		http.Error(w, "Not found", http.StatusNotFound)
	})
}
