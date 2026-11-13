package obs

import (
	"math"
	"strings"
	"testing"
)

func TestExpositionFormat(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("x_total", "Things.", "kind")
	c.Add(2, "a")
	c.Add(1, "a")
	h := r.Histogram("lat_seconds", "Latency.", []float64{0.1, 1}, "route")
	h.Observe(0.05, "r")
	h.Observe(0.5, "r")
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE x_total counter", `x_total{kind="a"} 3`,
		"# TYPE lat_seconds histogram", `lat_seconds_bucket{route="r",le="0.1"} 1`, `lat_seconds_bucket{route="r",le="+Inf"} 2`,
		`lat_seconds_count{route="r"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestQuantileInterpolates(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("q", "", []float64{1, 2, 3, 4})
	for i := 0; i < 100; i++ {
		h.Observe(float64(i%4) + 0.5)
	}
	if got := h.Quantile(0.5); math.Abs(got-2) > 1e-9 {
		t.Fatalf("median %v", got)
	}
	if !math.IsNaN(r.Histogram("empty", "", []float64{1}).Quantile(0.5)) {
		t.Fatal("no observations, no quantile")
	}
}
