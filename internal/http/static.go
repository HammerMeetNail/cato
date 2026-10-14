package http

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// compressibleTypes are the static file types worth gzipping. Images and
// fonts are already compressed.
var compressibleTypes = map[string]bool{
	".js": true, ".css": true, ".html": true, ".svg": true,
	".json": true, ".webmanifest": true, ".txt": true,
}

// gzipStatic serves text assets gzipped from an in-memory cache, compressing
// each file once (and again only when its size or mtime changes, so edits
// during development show up without a restart). The gzip bytes are served
// with http.ServeContent, so Range, If-None-Match and If-Modified-Since all
// apply to the gzip representation consistently; the earlier response-
// wrapping middleware labelled raw range bytes as gzip and had to skip static
// files entirely. Anything else falls through to next.
type gzipStatic struct {
	root string
	next http.Handler

	mu    sync.Mutex
	cache map[string]*gzEntry
}

type gzEntry struct {
	size    int64
	modTime time.Time
	gz      []byte
	etag    string // of the identity bytes; the gzip ETag adds a suffix
}

func newGzipStatic(root string, next http.Handler) *gzipStatic {
	return &gzipStatic{root: root, next: next, cache: map[string]*gzEntry{}}
}

func (g *gzipStatic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.serve(w, r, r.URL.Path)
}

// serve sends the file at urlPath ("/" → /index.html).
func (g *gzipStatic) serve(w http.ResponseWriter, r *http.Request, urlPath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		g.next.ServeHTTP(w, r)
		return
	}
	clean := path.Clean("/" + urlPath)
	if strings.HasSuffix(urlPath, "/") {
		clean = path.Join(clean, "index.html")
	}
	ext := strings.ToLower(path.Ext(clean))
	if !compressibleTypes[ext] {
		g.next.ServeHTTP(w, r)
		return
	}
	file := filepath.Join(g.root, filepath.FromSlash(clean))
	info, err := os.Stat(file)
	if err != nil || info.IsDir() {
		g.next.ServeHTTP(w, r)
		return
	}
	entry, err := g.entry(file, info)
	if err != nil {
		g.next.ServeHTTP(w, r)
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if ctype := mime.TypeByExtension(ext); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	} else if ext == ".webmanifest" {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	if !acceptsGzip(r) {
		w.Header().Set("ETag", `"`+entry.etag+`"`)
		http.ServeFile(w, r, file)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("ETag", `"`+entry.etag+`-gz"`)
	// ServeContent leaves Content-Length unset once Content-Encoding is set;
	// for whole-body responses it is simply the gzip length (a 304 drops it).
	if r.Header.Get("Range") == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.gz)))
	}
	http.ServeContent(w, r, "", entry.modTime, bytes.NewReader(entry.gz))
}

func (g *gzipStatic) entry(file string, info os.FileInfo) (*gzEntry, error) {
	g.mu.Lock()
	cached := g.cache[file]
	g.mu.Unlock()
	if cached != nil && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(raw)
	if err := zw.Close(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	e := &gzEntry{size: info.Size(), modTime: info.ModTime(), gz: buf.Bytes(), etag: hex.EncodeToString(sum[:8])}
	g.mu.Lock()
	g.cache[file] = e
	g.mu.Unlock()
	return e, nil
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}
