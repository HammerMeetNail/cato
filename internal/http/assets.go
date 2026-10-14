package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// assetIndex is automatic asset versioning (docs/cato_smooth.md §4.2), done
// at runtime rather than by a build step: Cato hashes every file under the
// static root and
//
//   - rewrites local src/href URLs in HTML to "?v=<hash>" (served immutable),
//   - injects an import map that pins every JS module to its versioned URL,
//     so relative imports between modules are versioned too, plus
//     modulepreload hints for the modules the entry point imports eagerly,
//   - stamps the service worker with the overall version and serves
//     /asset-manifest.json, its precache list.
//
// A changed file changes its URL, so no manual cache-name bump is needed.
type assetIndex struct {
	root string
	// minCheck bounds how often the tree is re-scanned for edits.
	minCheck time.Duration

	mu        sync.Mutex
	checked   time.Time
	files     map[string]assetFile // URL path → file state
	version   string
	importMap string // the importmap <script> body
	preload   []string
}

type assetFile struct {
	size    int64
	modTime time.Time
	hash    string
}

func newAssetIndex(root string) *assetIndex {
	return &assetIndex{root: root, minCheck: time.Second, files: map[string]assetFile{}}
}

// entryModule is the app's module entry point (its static imports are
// preloaded).
const entryModule = "/js/app.js"

var staticImport = regexp.MustCompile(`(?m)^\s*(?:import|export)\s+(?:[^'";]*?\sfrom\s+)?['"]([^'"]+)['"]`)

// refresh re-scans the tree (at most once per minCheck) and recomputes
// hashes of files whose size or mtime changed.
func (a *assetIndex) refresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.checked) < a.minCheck && a.version != "" {
		return
	}
	a.checked = time.Now()
	next := map[string]assetFile{}
	changed := false
	filepath.WalkDir(a.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(a.root, p)
		url := "/" + filepath.ToSlash(rel)
		old, ok := a.files[url]
		if ok && old.size == info.Size() && old.modTime.Equal(info.ModTime()) {
			next[url] = old
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(raw)
		next[url] = assetFile{size: info.Size(), modTime: info.ModTime(), hash: hex.EncodeToString(sum[:5])}
		changed = true
		return nil
	})
	if len(next) != len(a.files) {
		changed = true
	}
	if !changed && a.version != "" {
		return
	}
	a.files = next
	urls := make([]string, 0, len(next))
	for u := range next {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	all := sha256.New()
	imports := map[string]string{}
	for _, u := range urls {
		all.Write([]byte(u + "=" + next[u].hash + "\n"))
		if strings.HasPrefix(u, "/js/") && strings.HasSuffix(u, ".js") {
			imports[u] = u + "?v=" + next[u].hash
		}
	}
	a.version = hex.EncodeToString(all.Sum(nil)[:6])
	b, _ := json.Marshal(map[string]any{"imports": imports})
	a.importMap = string(b)
	a.preload = a.eagerModules()
}

// eagerModules follows static imports from the entry module.
func (a *assetIndex) eagerModules() []string {
	seen := map[string]bool{}
	var order []string
	var visit func(u string)
	visit = func(u string) {
		if seen[u] {
			return
		}
		seen[u] = true
		raw, err := os.ReadFile(filepath.Join(a.root, filepath.FromSlash(strings.TrimPrefix(u, "/"))))
		if err != nil {
			return
		}
		for _, m := range staticImport.FindAllSubmatch(raw, -1) {
			spec := string(m[1])
			var dep string
			switch {
			case strings.HasPrefix(spec, "/"):
				dep = spec
			case strings.HasPrefix(spec, "."):
				dep = path.Join(path.Dir(u), spec)
			default:
				continue
			}
			visit(dep)
		}
		order = append(order, u)
	}
	visit(entryModule)
	return order
}

// Hash returns the content hash of a static URL path ("" when unknown).
func (a *assetIndex) Hash(url string) string {
	a.refresh()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.files[url].hash
}

// Version identifies the whole static tree.
func (a *assetIndex) Version() string {
	a.refresh()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.version
}

// ScriptHash is the CSP source for the injected import map.
func (a *assetIndex) ScriptHash() string {
	a.refresh()
	a.mu.Lock()
	defer a.mu.Unlock()
	sum := sha256.Sum256([]byte(a.importMap))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

var localAssetAttr = regexp.MustCompile(`\b(src|href)="(/[^"?#]+)"`)

// rewriteHTML versions local asset URLs and injects the import map and
// modulepreload hints at the <!-- assets --> marker in <head>.
func (a *assetIndex) rewriteHTML(raw []byte) []byte {
	a.refresh()
	a.mu.Lock()
	defer a.mu.Unlock()
	out := localAssetAttr.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := localAssetAttr.FindSubmatch(m)
		url := string(sub[2])
		f, ok := a.files[url]
		if !ok || strings.HasSuffix(url, ".html") || url == "/service-worker.js" {
			return m
		}
		return []byte(string(sub[1]) + `="` + url + "?v=" + f.hash + `"`)
	})
	var inject bytes.Buffer
	inject.WriteString(`<script type="importmap">` + a.importMap + `</script>`)
	for _, u := range a.preload {
		if f, ok := a.files[u]; ok {
			inject.WriteString("\n  <link rel=\"modulepreload\" href=\"" + u + "?v=" + f.hash + "\">")
		}
	}
	return bytes.Replace(out, []byte("<!-- assets -->"), inject.Bytes(), 1)
}

// rewriteServiceWorker stamps the tree version into the worker, so any
// asset change installs a new worker that precaches the new URLs.
func (a *assetIndex) rewriteServiceWorker(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte("__CATO_ASSET_VERSION__"), []byte(a.Version()))
}

// precacheable are the static files the service worker stores up front:
// the shell's code, styles and icons (covers and screenshots are cached as
// they are viewed).
func precacheable(url string) bool {
	switch {
	case url == "/service-worker.js", strings.HasSuffix(url, ".map"):
		return false
	case strings.HasPrefix(url, "/js/"), strings.HasPrefix(url, "/css/"):
		return true
	case strings.HasPrefix(url, "/icons/") && (strings.HasSuffix(url, ".png") || strings.HasSuffix(url, ".svg")):
		return true
	}
	return url == "/offline.html" || url == "/manifest.webmanifest" || url == "/favicon.svg"
}

// handleManifest serves /asset-manifest.json: the version and the URLs the
// service worker precaches ("/" is the app shell, rendered with these URLs).
func (a *assetIndex) handleManifest(w http.ResponseWriter, r *http.Request) {
	a.refresh()
	a.mu.Lock()
	urls := []string{"/"}
	for u, f := range a.files {
		if precacheable(u) {
			if u == "/offline.html" || u == "/manifest.webmanifest" {
				urls = append(urls, u)
			} else {
				urls = append(urls, u+"?v="+f.hash)
			}
		}
	}
	version := a.version
	a.mu.Unlock()
	sort.Strings(urls)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(map[string]any{"version": version, "precache": urls})
}

// versioned reports whether r asks for the current version of its path.
func (a *assetIndex) versioned(r *http.Request) bool {
	v := r.URL.Query().Get("v")
	return v != "" && v == a.Hash(r.URL.Path)
}
