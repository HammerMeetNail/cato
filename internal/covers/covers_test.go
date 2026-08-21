package covers

import (
	"testing"
	"time"
)

// TestBackoffNextUsesAttemptNumber verifies the exponential backoff is keyed
// on the real attempt count (it used to always be called with 0, collapsing
// to a flat 1-minute retry).
func TestBackoffNextUsesAttemptNumber(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	// backoffNext(now, n) schedules the nth attempt 2^n minutes out.
	wantDelays := map[int]time.Duration{
		1: 2 * time.Minute,
		2: 4 * time.Minute,
		3: 8 * time.Minute,
		4: 16 * time.Minute,
	}
	for attempt, delay := range wantDelays {
		got := backoffNext(now, attempt)
		parsed, err := time.Parse(time.RFC3339, got)
		if err != nil {
			t.Fatalf("attempt %d: parse %q: %v", attempt, got, err)
		}
		if parsed.Sub(now) != delay {
			t.Errorf("attempt %d: delay %v, want %v", attempt, parsed.Sub(now), delay)
		}
	}
}

func TestLooksLikeImage(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, true},
		{"png", append([]byte{0x89}, []byte("PNG\x0d\x0a\x1a\x0a")...), true},
		{"webp", append(append([]byte("RIFF"), make([]byte, 4)...), []byte("WEBP")...), true},
		{"html", []byte("<html><body>404</body></html>"), false},
		{"truncated jpeg header", []byte{0xFF, 0xD8}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		if got := looksLikeImage(tc.data); got != tc.want {
			t.Errorf("%s: looksLikeImage = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTruncateErr(t *testing.T) {
	short := errorString("dial tcp: lookup failed")
	if got := truncateErr(short); got != string(short) {
		t.Errorf("short error should pass through unchanged, got %q", got)
	}

	long := make([]byte, 500)
	for i := range long {
		long[i] = 'a'
	}
	got := truncateErr(errorString(string(long)))
	if len(got) != 300 {
		t.Errorf("expected truncation to 300 chars, got %d", len(got))
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
