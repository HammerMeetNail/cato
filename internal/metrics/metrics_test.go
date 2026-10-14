package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestExposition(t *testing.T) {
	r := NewRegistry()
	r.Add("cato_http_requests_total", "Requests.", Labels{"route", "/api/v1/library", "code", "200"}, 1)
	r.Add("cato_http_requests_total", "Requests.", Labels{"route", "/api/v1/library", "code", "200"}, 2)
	r.ObserveDuration("cato_http_request_seconds", "Latency.", Labels{"route", "/x"}, 30*time.Millisecond)
	r.Collect(func(r *Registry) { r.Set("cato_db_bytes", "DB size.", nil, 4096) })
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE cato_http_requests_total counter",
		`cato_http_requests_total{route="/api/v1/library",code="200"} 3`,
		`cato_http_request_seconds_bucket{route="/x",le="0.025"} 0`,
		`cato_http_request_seconds_bucket{route="/x",le="0.05"} 1`,
		`cato_http_request_seconds_count{route="/x"} 1`,
		"cato_db_bytes 4096",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
