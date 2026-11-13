// Package obs exposes metrics in the Prometheus text format: counters, gauges and histograms with labels.
// Written against the standard library so the service has no metrics dependency to keep current.
package obs

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

// Registry holds every metric of the process.
type Registry struct {
	mu      sync.Mutex
	metrics map[string]metric
	order   []string
}

type metric interface {
	write(w io.Writer, name string)
}

func NewRegistry() *Registry { return &Registry{metrics: map[string]metric{}} }

func (r *Registry) add(name string, m metric) metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.metrics[name]; ok {
		return old
	}
	r.metrics[name] = m
	r.order = append(r.order, name)
	return m
}

// Write renders every metric.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	r.mu.Unlock()
	for _, n := range names {
		r.metrics[n].write(w, n)
	}
}

func labelKey(labels []string) string { return strings.Join(labels, "\x00") }

func renderLabels(names, values []string, extra ...string) string {
	var parts []string
	for i, n := range names {
		parts = append(parts, fmt.Sprintf(`%s=%q`, n, values[i]))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s=%q`, extra[i], extra[i+1]))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Counter is a monotonically increasing value per label set.
type Counter struct {
	help, kind string
	labels     []string
	mu         sync.Mutex
	values     map[string]float64
	keys       map[string][]string
}

func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	return r.add(name, &Counter{help: help, kind: "counter", labels: labels, values: map[string]float64{}, keys: map[string][]string{}}).(*Counter)
}

// Gauge is a value that goes up and down.
func (r *Registry) Gauge(name, help string, labels ...string) *Counter {
	return r.add(name, &Counter{help: help, kind: "gauge", labels: labels, values: map[string]float64{}, keys: map[string][]string{}}).(*Counter)
}

func (c *Counter) Add(v float64, labels ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := labelKey(labels)
	c.values[k] += v
	c.keys[k] = labels
}

func (c *Counter) Set(v float64, labels ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := labelKey(labels)
	c.values[k] = v
	c.keys[k] = labels
}

func (c *Counter) Value(labels ...string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[labelKey(labels)]
}

func (c *Counter) write(w io.Writer, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, c.help, name, c.kind)
	keys := sortedKeys(c.values)
	for _, k := range keys {
		fmt.Fprintf(w, "%s%s %g\n", name, renderLabels(c.labels, c.keys[k]), c.values[k])
	}
}

// Histogram counts observations into fixed buckets (upper bounds, seconds or any unit).
type Histogram struct {
	help    string
	bounds  []float64
	labels  []string
	mu      sync.Mutex
	buckets map[string][]uint64
	sums    map[string]float64
	counts  map[string]uint64
	keys    map[string][]string
}

func (r *Registry) Histogram(name, help string, bounds []float64, labels ...string) *Histogram {
	return r.add(name, &Histogram{
		help: help, bounds: bounds, labels: labels,
		buckets: map[string][]uint64{}, sums: map[string]float64{}, counts: map[string]uint64{}, keys: map[string][]string{},
	}).(*Histogram)
}

func (h *Histogram) Observe(v float64, labels ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := labelKey(labels)
	b, ok := h.buckets[k]
	if !ok {
		b = make([]uint64, len(h.bounds))
		h.buckets[k] = b
		h.keys[k] = labels
	}
	for i, ub := range h.bounds {
		if v <= ub {
			b[i]++
		}
	}
	h.sums[k] += v
	h.counts[k]++
}

// Quantile estimates a quantile from the buckets, interpolating linearly inside a bucket as Prometheus does.
func (h *Histogram) Quantile(q float64, labels ...string) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := labelKey(labels)
	n := h.counts[k]
	if n == 0 {
		return math.NaN()
	}
	rank := q * float64(n)
	prevBound, prevCount := 0.0, 0.0
	for i, ub := range h.bounds {
		c := float64(h.buckets[k][i])
		if c >= rank {
			if c == prevCount {
				return ub
			}
			return prevBound + (ub-prevBound)*(rank-prevCount)/(c-prevCount)
		}
		prevBound, prevCount = ub, c
	}
	return math.Inf(1)
}

func (h *Histogram) write(w io.Writer, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, h.help, name)
	for _, k := range sortedKeys(h.counts) {
		for i, ub := range h.bounds {
			fmt.Fprintf(w, "%s_bucket%s %d\n", name, renderLabels(h.labels, h.keys[k], "le", fmt.Sprintf("%g", ub)), h.buckets[k][i])
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", name, renderLabels(h.labels, h.keys[k], "le", "+Inf"), h.counts[k])
		fmt.Fprintf(w, "%s_sum%s %g\n", name, renderLabels(h.labels, h.keys[k]), h.sums[k])
		fmt.Fprintf(w, "%s_count%s %d\n", name, renderLabels(h.labels, h.keys[k]), h.counts[k])
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LatencyBuckets are seconds, from 50 microseconds to 10 seconds.
var LatencyBuckets = []float64{0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
