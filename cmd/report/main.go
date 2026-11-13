// Command report writes the README results block and web/public/data.json from reports/*.json.
// With -check it fails when the README is out of date, which CI runs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strings"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/experiment"
)

const (
	start = "<!-- results:start -->"
	end   = "<!-- results:end -->"
)

type file struct {
	Report  experiment.Report `json:"report"`
	Seconds float64           `json:"seconds"`
}

func load(path string) (experiment.Report, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return experiment.Report{}, false
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		log.Fatal(err)
	}
	return f.Report, true
}

func pct(x float64) string { return fmt.Sprintf("%+.1f%%", 100*x) }

func pp(x float64) string { return fmt.Sprintf("%+.2f pp", x) }

func iv(i experiment.Interval) string {
	return fmt.Sprintf("%s (%s to %s)", pct(i.Mean), pct(i.Lo), pct(i.Hi))
}

// Recommend is the corrected rule: the largest exposure whose GMV lift is positive at the lower end of its
// interval and whose worst domestic sell-through change stays within the limit. The rule the study shipped
// with (Report.Guardrail) checked sell-through only; the held-out world showed why that is not enough.
func Recommend(r experiment.Report) (bp int, lift float64) {
	for _, e := range r.Ramp {
		worst := math.Inf(1)
		for _, s := range e.STRpp {
			worst = math.Min(worst, s.Mean)
		}
		if e.GMVLift.Lo > 0 && -worst <= r.Guardrail.MaxDropPP && e.ExposureBP > bp {
			bp, lift = e.ExposureBP, e.GMVLift.Mean
		}
	}
	return bp, lift
}

func exposure(bp int) string {
	if bp == 0 {
		return "stay closed"
	}
	return fmt.Sprintf("%d%%", bp/100)
}

func designLabel(d string) string {
	return map[string]string{
		"buyer_split":   "Buyer split (half of the buyers see the other market)",
		"listing_split": "Listing split (half of the listings cross: a 50% ramp)",
		"switchback":    "Switchback (connection on and off in 2-day blocks)",
	}[d]
}

func world(r experiment.Report, held bool) []string {
	var l []string
	t := r.Truth
	l = append(l,
		fmt.Sprintf("%d replications; %d buyers and %d new listings a day across %s; %d test days after a closed warm-up.",
			r.Seeds, r.Buyers, r.ListingsDay, strings.Join(r.Markets, " and "), r.TestDays),
		"",
		"| Exposure of each market to the other | GMV vs closed (95% CI) | Sell-through, FR sellers | Sell-through, DE sellers | Cross-border share of GMV |",
		"|---:|---:|---:|---:|---:|",
	)
	for _, e := range r.Ramp {
		l = append(l, fmt.Sprintf("| %s | %s | %s | %s | %.1f%% |", exposure(e.ExposureBP), iv(e.GMVLift), pp(e.STRpp["FR"].Mean), pp(e.STRpp["DE"].Mean), 100*e.CrossShare.Mean))
	}
	c := r.Capped
	l = append(l, fmt.Sprintf("| 100%%, feed capped at %s | %s | %s | %s | %.1f%% |", "6 of 24", iv(c.GMVLift), pp(c.STRpp["FR"].Mean), pp(c.STRpp["DE"].Mean), 100*c.CrossShare.Mean))
	l = append(l, "",
		fmt.Sprintf("**Which experiment finds the true effect of a full connection (%s)?**", pct(t.GMVLift.Mean)),
		"",
		"| Design | Bias (points) | RMSE (points) | Its 95% interval holds the truth | Mean interval width |",
		"|---|---:|---:|---:|---:|",
	)
	for _, d := range r.Designs {
		l = append(l, fmt.Sprintf("| %s | %+.1f | %.1f | %d of %d | %.1f points |", designLabel(d.Design), 100*d.Bias, 100*d.RMSE, int(math.Round(d.Coverage*float64(r.Seeds))), r.Seeds, 100*d.Width))
	}
	bp, lift := Recommend(r)
	l = append(l, "",
		fmt.Sprintf("**Ramp decision.** Rule as first written (domestic sell-through drop ≤ %.1f pp): %s, at %s GMV. Corrected rule (GMV lift above zero at the low end of its interval, and the same sell-through limit): **%s**%s.",
			r.Guardrail.MaxDropPP, exposure(r.Guardrail.ExposureBP), pct(r.Guardrail.GMVLift), exposure(bp), map[bool]string{true: fmt.Sprintf(", at %s GMV", pct(lift)), false: ""}[bp > 0]),
	)
	_ = held
	return l
}

type benchFile map[string]any

func num(m benchFile, path ...string) float64 {
	var cur any = map[string]any(m)
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return math.NaN()
		}
		cur = mm[p]
	}
	f, _ := cur.(float64)
	return f
}

func bench() ([]string, benchFile) {
	b, err := os.ReadFile("reports/bench.json")
	if err != nil {
		return nil, nil
	}
	var m benchFile
	if err := json.Unmarshal(b, &m); err != nil {
		log.Fatal(err)
	}
	return []string{
		fmt.Sprintf("Measured by `go run ./cmd/bench` on %v CPUs, in process (no network between the pieces).", m["cpus"]),
		"",
		"| What | Result |",
		"|---|---|",
		fmt.Sprintf("| One visibility decision | %.0f ns |", num(m, "visible_ns_per_call")),
		fmt.Sprintf("| Materialise visibility for 1,000,000 listings, 8 markets, 3 channels | %.1f s (%.0f listings/s) |", num(m, "materialise", "seconds"), num(m, "materialise", "listings_per_second")),
		fmt.Sprintf("| Ramp FR→DE from 20%% to 30%% on that catalogue | %.0f ms incremental vs %.0f ms full recompute; %.0f index writes; %v mismatches |", num(m, "ramp_step", "incremental_ms"), num(m, "ramp_step", "full_recompute_ms"), num(m, "ramp_step", "index_writes"), m["ramp_step"].(map[string]any)["mismatches_after"]),
		fmt.Sprintf("| Listing events through the log into the index, backlog | %.0f events/s |", num(m, "pipeline_backlog", "events_per_second")),
		fmt.Sprintf("| Visibility lag at %.0f events/s | p50 %.2f ms, p99 %.2f ms |", num(m, "pipeline_steady", "events_per_second"), num(m, "pipeline_steady", "lag_p50_ms"), num(m, "pipeline_steady", "lag_p99_ms")),
		fmt.Sprintf("| Policy activation on 200,000 listings, publish to last index write | %.0f ms (%.0f partial updates) |", num(m, "policy_activation", "ms_to_last_index_write"), num(m, "policy_activation", "partial_updates")),
		fmt.Sprintf("| Feed endpoint, 200 candidates filtered and balanced | p50 %.0f µs, p99 %.0f µs |", num(m, "feed_http", "p50_us"), num(m, "feed_http", "p99_us")),
	}, m
}

func block() (string, map[string]any) {
	dev, ok := load("reports/dev.json")
	if !ok {
		log.Fatal("reports/dev.json missing: run go run ./cmd/lab")
	}
	lines := []string{start, "**Development world**", ""}
	lines = append(lines, world(dev, false)...)
	data := map[string]any{"dev": webWorld(dev)}
	if held, ok := load("reports/heldout.json"); ok {
		lines = append(lines, "", "**Held-out world, first and only run** (more cross-border friction, flatter supply)", "")
		lines = append(lines, world(held, true)...)
		data["heldout"] = webWorld(held)
	}
	if b, m := bench(); b != nil {
		lines = append(lines, "", "**The platform**", "")
		lines = append(lines, b...)
		data["bench"] = m
	}
	lines = append(lines, end)
	return strings.Join(lines, "\n"), data
}

func webWorld(r experiment.Report) map[string]any {
	bp, lift := Recommend(r)
	designs := []map[string]any{}
	for _, d := range r.Designs {
		designs = append(designs, map[string]any{
			"id": d.Design, "label": designLabel(d.Design), "bias": d.Bias, "rmse": d.RMSE, "coverage": d.Coverage,
			"width": d.Width, "estimates": d.Estimates,
		})
	}
	return map[string]any{
		"world": r.World, "seeds": r.Seeds, "markets": r.Markets, "buyers": r.Buyers, "listingsPerDay": r.ListingsDay,
		"testDays": r.TestDays, "truth": r.Truth, "ramp": r.Ramp, "capped": r.Capped, "designs": designs,
		"guardrail": r.Guardrail, "recommendation": map[string]any{"exposureBP": bp, "gmvLift": lift},
	}
}

func main() {
	check := flag.Bool("check", false, "fail if README.md is out of date")
	flag.Parse()
	readme, err := os.ReadFile("README.md")
	if err != nil {
		log.Fatal(err)
	}
	text := string(readme)
	i, j := strings.Index(text, start), strings.Index(text, end)
	if i < 0 || j < 0 {
		log.Fatal("README.md has no results markers")
	}
	b, data := block()
	next := text[:i] + b + text[j+len(end):]
	if *check {
		if next != text {
			log.Fatal("README results are out of date: run go run ./cmd/report")
		}
		return
	}
	if err := os.WriteFile("README.md", []byte(next), 0o644); err != nil {
		log.Fatal(err)
	}
	js, _ := json.MarshalIndent(data, "", " ")
	if err := os.MkdirAll("web/public", 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("web/public/data.json", append(js, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("README results and web/public/data.json updated")
}
