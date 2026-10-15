package http

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"cato/internal/config"
)

// The vendored libraries must match js/vendor/CHECKSUMS exactly (written by
// scripts/vendor-update.sh), and nothing unlisted may sit beside them.
func TestVendoredFilesMatchChecksums(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "static", "js", "vendor")
	f, err := os.Open(filepath.Join(dir, "CHECKSUMS"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	listed := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			t.Fatalf("bad CHECKSUMS line %q", sc.Text())
		}
		name = strings.TrimPrefix(name, "./")
		listed[name] = true
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got := sha256.Sum256(raw)
		if hex.EncodeToString(got[:]) != sum {
			t.Errorf("%s changed: vendored files are edited only by scripts/vendor-update.sh", name)
		}
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "CHECKSUMS" {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if !listed[filepath.ToSlash(rel)] {
			t.Errorf("%s is not in CHECKSUMS", rel)
		}
		return nil
	})
}

func assetServer(t *testing.T) (*Server, string) {
	t.Helper()
	static := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(static, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", `<!doctype html><head><!-- assets --><link rel="stylesheet" href="/css/app.css"><link rel="icon" href="/favicon.svg"></head>
<body><script type="module" src="/js/app.js"></script><a href="/login">x</a></body>`)
	write("css/app.css", "body{}")
	write("favicon.svg", "<svg></svg>")
	write("js/app.js", "import { a } from './core/a.js';\nimport '/js/core/b.js';\nconst lazy = () => import('./later.js');\n")
	write("js/core/a.js", "export const a = 1;\n")
	write("js/core/b.js", "export {};\n")
	write("js/later.js", "export {};\n")
	// macOS metadata a copy from a Mac can leave behind.
	write("js/._app.js", "\x00\x05\x16\x07")
	write(".DS_Store", "junk")
	write("service-worker.js", `const VERSION = "__CATO_ASSET_VERSION__";`)
	write("offline.html", "<!doctype html><title>Offline</title>")
	srv := newHTTPSTestServer(t, &config.Config{StaticDir: static, AllowInsecure: true})
	srv.assets.minCheck = 0
	return srv, static
}

var versionedJS = regexp.MustCompile(`src="/js/app\.js\?v=([0-9a-f]{10})"`)

func TestHTMLGetsVersionedAssetsAndImportMap(t *testing.T) {
	srv, static := assetServer(t)
	rec := get(t, srv, "/", nil)
	body := rec.Body.String()
	m := versionedJS.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("app.js not versioned:\n%s", body)
	}
	if !strings.Contains(body, `href="/css/app.css?v=`) || !strings.Contains(body, `href="/favicon.svg?v=`) {
		t.Errorf("css/icon not versioned:\n%s", body)
	}
	if !strings.Contains(body, `href="/login"`) {
		t.Errorf("page links must stay unversioned")
	}
	// The import map pins every module, including lazily imported ones.
	start := strings.Index(body, `<script type="importmap">`) + len(`<script type="importmap">`)
	end := strings.Index(body[start:], "</script>")
	var im struct{ Imports map[string]string }
	if err := json.Unmarshal([]byte(body[start:start+end]), &im); err != nil {
		t.Fatalf("import map: %v", err)
	}
	if !strings.HasPrefix(im.Imports["/js/later.js"], "/js/later.js?v=") || !strings.HasPrefix(im.Imports["/js/core/a.js"], "/js/core/a.js?v=") {
		t.Errorf("import map %v", im.Imports)
	}
	// Dotfiles (macOS ._* metadata, .DS_Store) aren't assets.
	if _, ok := im.Imports["/js/._app.js"]; ok {
		t.Errorf("dotfile in the import map: %v", im.Imports)
	}
	if manifest := get(t, srv, "/asset-manifest.json", nil).Body.String(); strings.Contains(manifest, "._app.js") || strings.Contains(manifest, ".DS_Store") {
		t.Errorf("dotfiles in the asset manifest: %s", manifest)
	}
	// Static imports of the entry point are preloaded; dynamic ones aren't.
	for _, want := range []string{`<link rel="modulepreload" href="/js/core/a.js?v=`, `<link rel="modulepreload" href="/js/core/b.js?v=`, `<link rel="modulepreload" href="/js/app.js?v=`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, `modulepreload" href="/js/later.js`) {
		t.Error("lazily imported module preloaded")
	}
	// The inline import map is allowed by hash, and only it.
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' "+srv.assets.ScriptHash()) || strings.Contains(csp, "unsafe-inline';") && strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("CSP %q", csp)
	}

	// Versioned URLs are immutable; plain ones revalidate.
	if cc := get(t, srv, "/js/app.js?v="+m[1], nil).Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset Cache-Control %q", cc)
	}
	if cc := get(t, srv, "/js/app.js?v=stale", nil).Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("a stale version must not be cached forever: %q", cc)
	}
	if cc := get(t, srv, "/js/app.js", nil).Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("unversioned asset Cache-Control %q", cc)
	}

	// Editing a module changes its URL in the next page load.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(static, "js", "app.js"), []byte("import './core/a.js';\nconsole.log(2);\n"), 0o644)
	m2 := versionedJS.FindStringSubmatch(get(t, srv, "/", nil).Body.String())
	if m2 == nil || m2[1] == m[1] {
		t.Errorf("version did not change after an edit: %v → %v", m, m2)
	}
}

func TestServiceWorkerAndManifestTrackTheVersion(t *testing.T) {
	srv, static := assetServer(t)
	sw := get(t, srv, "/service-worker.js", nil)
	if strings.Contains(sw.Body.String(), "__CATO_ASSET_VERSION__") || !strings.Contains(sw.Body.String(), srv.assets.Version()) {
		t.Fatalf("service worker not stamped: %s", sw.Body.String())
	}
	if sw.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("service worker Cache-Control %q", sw.Header().Get("Cache-Control"))
	}
	rec := get(t, srv, "/asset-manifest.json", nil)
	var m struct {
		Version  string
		Precache []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(m.Precache, " ")
	for _, want := range []string{"/ ", "/offline.html", "/js/later.js?v=", "/css/app.css?v="} {
		if !strings.Contains(joined+" ", want) {
			t.Errorf("precache %v lacks %s", m.Precache, want)
		}
	}
	if strings.Contains(joined, "service-worker") {
		t.Error("the worker must not precache itself")
	}
	old := m.Version
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(static, "css", "app.css"), []byte("body{color:red}"), 0o644)
	if srv.assets.Version() == old {
		t.Error("asset version did not change")
	}
	if !strings.Contains(get(t, srv, "/service-worker.js", nil).Body.String(), srv.assets.Version()) {
		t.Error("the worker was not re-stamped after an asset changed")
	}
	_ = http.StatusOK
}

func TestStaticImportScanFindsMinifiedImports(t *testing.T) {
	src := "import{a as b}from\"./x.js\";import\"./y.js\";export*from'./z.js'\n" +
		"import { c } from './c.js';\nconst m = () => import('./lazy.js');\nexport const s = 'not.js';\n"
	var got []string
	for _, m := range staticImport.FindAllStringSubmatch(src, -1) {
		got = append(got, m[1])
	}
	if strings.Join(got, " ") != "./x.js ./y.js ./z.js ./c.js" {
		t.Fatalf("specifiers: %v", got)
	}
}
