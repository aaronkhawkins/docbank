package api

import (
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	docweb "go.kenn.io/docbank/internal/web"
)

var documentViewerPathPattern = regexp.MustCompile(
	`^/documents/([1-9][0-9]*)/versions/([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})$`,
)

func isDocumentViewerPath(path string) bool {
	match := documentViewerPathPattern.FindStringSubmatch(path)
	if match == nil {
		return false
	}
	id, err := strconv.ParseInt(match[1], 10, 64)
	return err == nil && id > 0
}

// registerWeb serves the embedded SPA without granting it a privileged data
// path. JavaScript and styles are public static bytes; every vault read still
// crosses the authenticated /api/v1 surface.
func registerWeb(mux *http.ServeMux, enabled bool, webURL string) {
	if !enabled {
		return
	}
	assets := docweb.Assets()
	indexHTML, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		panic("api: embedded web index is missing")
	}
	static := http.FileServer(http.FS(assets))
	serveIndex := func(w http.ResponseWriter) {
		setWebHeaders(w, webURL)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		serveIndex(w)
	})
	mux.HandleFunc("GET /documents/{node_id}/versions/{version_id}", func(w http.ResponseWriter, r *http.Request) {
		if !isDocumentViewerPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		serveIndex(w)
	})
	mux.Handle("GET /assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setWebHeaders(w, webURL)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		static.ServeHTTP(w, r)
	}))
}

func setWebHeaders(w http.ResponseWriter, webURL string) {
	connectSources := "'self'"
	if parsed, err := url.Parse(webURL); err == nil && parsed.Host != "" {
		switch parsed.Scheme {
		case "http":
			connectSources += " ws://" + parsed.Host
		case "https":
			connectSources += " wss://" + parsed.Host
		}
	}
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; "+
			"connect-src "+connectSources+"; worker-src 'self'; font-src 'self' blob:; frame-src 'none'; object-src 'none'; "+
			"base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
