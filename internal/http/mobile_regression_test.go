package http

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper to read a static file from the repo root
func readStaticFile(t *testing.T, relPath string) string {
	t.Helper()
	// Tests are run from the package directory, but `go test ./...` runs with
	// the repo root as the working directory for the `go` tool. Use a
	// relative path that works from both `internal/http` and repo root by
	// trying a few locations.
	candidates := []string{
		filepath.Join("..", "..", relPath), // from internal/http
		relPath,                            // from repo root
		filepath.Join("web", "static", filepath.Base(relPath)), // fallback
	}
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
	}
	t.Fatalf("could not read %s from any candidate %v", relPath, candidates)
	return ""
}

// TestSearchDoesNotBlockBottomTabs ensures the search outside handlers
// never block bottom-tab / topbar navigation. This was a mobile UX
// regression where tapping a bottom tab while the search input was focused
// was swallowed by the search's pointerdown/click capture handlers,
// leaving the hash at #library instead of navigating to #stats.
func TestSearchDoesNotBlockBottomTabs(t *testing.T) {
	content := readStaticFile(t, "web/static/js/search.js")
	// The fix introduces isNavChrome that explicitly allows bottom-tabs/topbar
	if !strings.Contains(content, "isNavChrome") {
		t.Fatalf("search.js missing isNavChrome helper: bottom-tab navigation will be blocked while search is focused")
	}
	if !strings.Contains(content, "#bottom-tabs") {
		t.Fatalf("search.js must reference #bottom-tabs to exclude it from outside handling")
	}
	if !strings.Contains(content, ".topbar") {
		t.Fatalf("search.js must reference .topbar to exclude it from outside handling")
	}
	// Both pointer and click handlers must have the early-return for nav chrome
	if !strings.Contains(content, "if (isNavChrome(e.target))") {
		t.Fatalf("search.js must early-return for nav chrome in outside handlers")
	}
	// Ensure the comment explaining the fix is present (prevents accidental removal)
	if !strings.Contains(content, "Navigation chrome") {
		t.Fatalf("search.js missing comment about navigation chrome not being blocked")
	}
}

// TestInfiniteScrollListensToAppShell ensures library infinite scroll
// works on mobile where .app-shell is the scroll container, not window.
// The original bug used only window.addEventListener('scroll', ...) which
// never fired on mobile (appShell.scrollHeight > clientHeight, window.scrollY==0).
func TestInfiniteScrollListensToAppShell(t *testing.T) {
	content := readStaticFile(t, "web/static/js/library.js")
	if !strings.Contains(content, "app-shell") {
		t.Fatalf("library.js must reference .app-shell for mobile infinite scroll")
	}
	if !strings.Contains(content, "appShell.addEventListener('scroll'") && !strings.Contains(content, `appShell.addEventListener("scroll"`) {
		t.Fatalf("library.js must attach scroll listener to .app-shell, not just window")
	}
	if !strings.Contains(content, "window.addEventListener('scroll'") && !strings.Contains(content, `window.addEventListener("scroll"`) {
		t.Fatalf("library.js must still keep window scroll listener as fallback for desktop")
	}
	// The fix checks appShell.scrollHeight > clientHeight before using window
	if !strings.Contains(content, "appShell.scrollHeight > appShell.clientHeight") {
		t.Fatalf("library.js must check appShell scrollHeight vs clientHeight to detect mobile scroll container")
	}
	if !strings.Contains(content, "appShell.scrollTop + appShell.clientHeight") {
		t.Fatalf("library.js must use appShell.scrollTop + clientHeight for threshold")
	}
}

// TestSearchDropdownMaxHeightAccountsForBottomTabs ensures the mobile
// search dropdown does not extend behind the bottom tab bar.
func TestSearchDropdownMaxHeightAccountsForBottomTabs(t *testing.T) {
	content := readStaticFile(t, "web/static/css/app.css")
	// The mobile media query must subtract both the 66px tab bar and safe area
	if !strings.Contains(content, "max-height: calc(100dvh - 118px - 66px - var(--safe-bottom))") {
		t.Fatalf("app.css mobile .search-results max-height must account for bottom tabs: expected 'calc(100dvh - 118px - 66px - var(--safe-bottom))'")
	}
	// Ensure the old buggy value is not present as the sole definition
	if strings.Contains(content, "@media (max-width: 600px)") {
		// Find the block and ensure it contains the fixed value
		idx := strings.Index(content, "@media (max-width: 600px)")
		snippet := content[idx:]
		// Cut to next closing brace of that media query's first rule
		end := strings.Index(snippet, "}")
		if end != -1 {
			snippet = snippet[:end+200] // a bit more
		}
		if strings.Contains(snippet, "max-height: calc(100dvh - 118px);") && !strings.Contains(snippet, "66px") {
			t.Fatalf("app.css still contains old buggy max-height without bottom-tab accounting")
		}
	}
}

// TestTagChipEllipsis ensures long tag names are truncated with ellipsis
// instead of causing horizontal overflow on 320px viewports.
func TestTagChipEllipsis(t *testing.T) {
	content := readStaticFile(t, "web/static/css/app.css")
	// Find the .tag-chip rule
	idx := strings.Index(content, ".tag-chip {")
	if idx == -1 {
		t.Fatal("app.css missing .tag-chip rule")
	}
	// Look ahead a bit for the properties
	snippet := content[idx : idx+800]
	if !strings.Contains(snippet, "max-width: 100%") {
		t.Fatal(".tag-chip must have max-width: 100% to prevent overflow")
	}
	if !strings.Contains(snippet, "overflow: hidden") {
		t.Fatal(".tag-chip must have overflow: hidden")
	}
	if !strings.Contains(snippet, "text-overflow: ellipsis") {
		t.Fatal(".tag-chip must have text-overflow: ellipsis for long tags")
	}
}

// TestServiceWorkerCacheBumped ensures shipped asset changes bump the
// service worker cache version so clients don't serve stale JS/CSS.
func TestServiceWorkerCacheBumped(t *testing.T) {
	content := readStaticFile(t, "web/static/service-worker.js")
	if !strings.Contains(content, `CACHE_NAME = "cato-static-v18"`) {
		t.Fatalf("service-worker.js must have CACHE_NAME v18 after mobile fixes; got: %s", snippet(content, "CACHE_NAME", 60))
	}
}

// TestStatusFilterAppendsToSearch ensures that when a search is active,
// changing the library status via the status FAB appends to the existing
// search instead of discarding it and switching to library view.
func TestStatusFilterAppendsToSearch(t *testing.T) {
	content := readStaticFile(t, "web/static/js/library.js")
	if !strings.Contains(content, "getSearchStatuses") {
		t.Fatalf("library.js missing getSearchStatuses helper for search-aware status filtering")
	}
	if !strings.Contains(content, "setSearchStatuses") {
		t.Fatalf("library.js missing setSearchStatuses helper for search-aware status filtering")
	}
	// The wire handler must branch on search mode and call loadSearchResults
	if !strings.Contains(content, "if (paginationState.mode === 'search')") {
		t.Fatalf("library.js wireStatusFilterPanel must branch on search mode")
	}
	if !strings.Contains(content, "loadSearchResults(paginationState.searchQuery)") {
		t.Fatalf("library.js status handler in search mode must reload search via loadSearchResults, not loadLibrary")
	}
	if !strings.Contains(content, "searchFilters.inLibrary") || !strings.Contains(content, "searchFilters.libraryStatus") {
		t.Fatalf("library.js search status handling must update searchFilters.inLibrary/libraryStatus")
	}
}

// TestStatusFilterReflectsSearch ensures the status FAB reflects an
// existing search that is already filtered by library status.
func TestStatusFilterReflectsSearch(t *testing.T) {
	content := readStaticFile(t, "web/static/js/library.js")
	// syncStatusFilterPanel must read from searchFilters when in search mode
	if !strings.Contains(content, "paginationState.mode === 'search' ? getSearchStatuses() : currentStatuses()") {
		t.Fatalf("library.js syncStatusFilterPanel must use getSearchStatuses() when in search mode")
	}
}

// TestStatusFilterSupportsMultiple ensures the library filter icon
// supports selecting multiple libraries (e.g. playing + backlog) when
// filtering a search, not just a single status.
func TestStatusFilterSupportsMultiple(t *testing.T) {
	// Frontend must handle array of statuses
	libContent := readStaticFile(t, "web/static/js/library.js")
	if !strings.Contains(libContent, "libraryStatuses") {
		t.Fatalf("library.js must handle libraryStatuses array for multi-library filtering")
	}
	if !strings.Contains(libContent, "searchFilters.libraryStatuses") {
		t.Fatalf("library.js searchFilters must include libraryStatuses for multi")
	}
	// Wire handler must append, not replace (multi)
	if !strings.Contains(libContent, "next = [...cur, status]") {
		t.Fatalf("library.js status handler must support multi-append (next = [...cur, status]) for search mode")
	}
	// API must send multiple library_status params
	apiContent := readStaticFile(t, "web/static/js/api.js")
	if !strings.Contains(apiContent, "Array.isArray(libraryStatus)") {
		t.Fatalf("api.js must handle libraryStatus as array for multi-library search")
	}
	// Backend must handle multiple statuses
	storeContent := readStaticFile(t, "internal/games/store.go")
	if !strings.Contains(storeContent, "libraryStatuses") {
		t.Fatalf("store.go must have libraryStatuses field for multi-library search")
	}
	if !strings.Contains(storeContent, "libraryStatuses") || !strings.Contains(storeContent, "IN (") {
		t.Fatalf("store.go must handle IN clause for multiple library statuses")
	}
	handlerContent := readStaticFile(t, "internal/http/handler_games.go")
	if !strings.Contains(handlerContent, `r.URL.Query()["library_status"]`) && !strings.Contains(handlerContent, "library_status") {
		t.Fatalf("handler_games.go must parse multiple library_status values")
	}
}

func snippet(s, needle string, radius int) string {
	idx := strings.Index(s, needle)
	if idx == -1 {
		return ""
	}
	start := idx - radius
	if start < 0 {
		start = 0
	}
	end := idx + radius
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
