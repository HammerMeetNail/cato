// Package metrics keeps a few in-process counters and histograms and writes
// them in the Prometheus text exposition format, without a client library.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Labels is an ordered label set ("route", "/api/v1/library", …).
type Labels []string

func (l Labels) key() string { return strings.Join(l, "\x00") }

func (l Labels) format(extra ...string) string {
	all := append(append([]string{}, l...), extra...)
	if len(all) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(all); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", all[i], all[i+1])
	}
	b.WriteByte('}')
	return b.String()
}

type series struct {
	labels Labels
	value  float64
	// histogram state
	counts []uint64
	sum    float64
	count  uint64
}

type family struct {
	name, help, kind string
	buckets          []float64
	series           map[string]*series
}

// Registry holds metric families.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
	// collectors run at scrape time to set gauges (DB size, queue depth…).
	collectors []func(*Registry)
}

func NewRegistry() *Registry { return &Registry{families: map[string]*family{}} }

func (r *Registry) family(name, help, kind string, buckets []float64) *family {
	f, ok := r.families[name]
	if !ok {
		f = &family{name: name, help: help, kind: kind, buckets: buckets, series: map[string]*series{}}
		r.families[name] = f
	}
	return f
}

func (f *family) get(l Labels) *series {
	s, ok := f.series[l.key()]
	if !ok {
		s = &series{labels: l}
		if f.kind == "histogram" {
			s.counts = make([]uint64, len(f.buckets))
		}
		f.series[l.key()] = s
	}
	return s
}

// Add increments a counter.
func (r *Registry) Add(name, help string, l Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.family(name, help, "counter", nil).get(l).value += v
}

// Set sets a gauge.
func (r *Registry) Set(name, help string, l Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.family(name, help, "gauge", nil).get(l).value = v
}

// DefaultBuckets suit request and query latencies, in seconds.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// Observe records a histogram sample.
func (r *Registry) Observe(name, help string, l Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.family(name, help, "histogram", DefaultBuckets).get(l)
	for i, b := range DefaultBuckets {
		if v <= b {
			s.counts[i]++
		}
	}
	s.sum += v
	s.count++
}

// ObserveDuration records a duration in seconds.
func (r *Registry) ObserveDuration(name, help string, l Labels, d time.Duration) {
	r.Observe(name, help, l, d.Seconds())
}

// Collect registers a scrape-time callback.
func (r *Registry) Collect(fn func(*Registry)) {
	r.mu.Lock()
	r.collectors = append(r.collectors, fn)
	r.mu.Unlock()
}

// Write renders every family in the Prometheus text format.
func (r *Registry) Write(w io.Writer) error {
	r.mu.Lock()
	collectors := append([]func(*Registry){}, r.collectors...)
	r.mu.Unlock()
	for _, c := range collectors {
		c(r)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.families))
	for n := range r.families {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := r.families[n]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		keys := make([]string, 0, len(f.series))
		for k := range f.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.series[k]
			if f.kind != "histogram" {
				fmt.Fprintf(w, "%s%s %s\n", f.name, s.labels.format(), num(s.value))
				continue
			}
			for i, b := range f.buckets {
				fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, s.labels.format("le", num(b)), s.counts[i])
			}
			fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, s.labels.format("le", "+Inf"), s.count)
			fmt.Fprintf(w, "%s_sum%s %s\n", f.name, s.labels.format(), num(s.sum))
			fmt.Fprintf(w, "%s_count%s %d\n", f.name, s.labels.format(), s.count)
		}
	}
	return nil
}

func num(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}
