// Command bench measures the platform: policy evaluation, materialising visibility for a large catalogue, the cost
// of a ramp step done incrementally against a full recompute, the event pipeline's throughput and lag, and the
// feed endpoint's latency. Writes reports/bench.json.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/api"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/feed"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/index"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/obs"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/stream"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/visibility"
)

var markets = []string{"FR", "DE", "BE", "ES", "IT", "NL", "PL", "LT"}

func network(fr2de int) *policy.Policy {
	p := &policy.Policy{Version: 1, Markets: markets}
	for _, b := range markets {
		for _, s := range markets {
			if b == s {
				continue
			}
			r := policy.Rule{Buyer: b, Seller: s, State: policy.Open, ExposureBP: policy.Buckets, Channels: policy.Channels, MaxWeightGrams: 5000}
			if b == "DE" && s == "FR" {
				r.State, r.ExposureBP, r.Salt = policy.Ramping, fr2de, "fr-de"
			}
			if s == "LT" || b == "LT" {
				r = policy.Rule{Buyer: b, Seller: s, State: policy.Closed}
			}
			p.Rules = append(p.Rules, r)
		}
	}
	if err := p.Validate(); err != nil {
		log.Fatal(err)
	}
	return p
}

func listings(n int) []policy.Listing {
	rng := rand.New(rand.NewPCG(1, 2))
	cats := []string{"women", "men", "kids", "home", "electronics", "entertainment"}
	out := make([]policy.Listing, n)
	for i := range out {
		out[i] = policy.Listing{ID: uint64(i + 1), Seller: markets[rng.IntN(len(markets))], Category: cats[rng.IntN(len(cats))], WeightG: rng.IntN(8000), Active: true}
	}
	return out
}

func pct(xs []time.Duration, q float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return float64(s[int(q*float64(len(s)-1))].Microseconds())
}

func main() {
	out := map[string]any{"go": runtime.Version(), "cpus": runtime.NumCPU()}

	// 1. one visibility decision
	p := network(2000)
	ls := listings(200_000)
	start := time.Now()
	calls := 0
	for i := 0; i < 20; i++ {
		for _, l := range ls {
			p.Visible(l, "DE", policy.Feed)
			calls++
		}
	}
	out["visible_ns_per_call"] = float64(time.Since(start).Nanoseconds()) / float64(calls)

	// 2. materialise a catalogue of a million listings
	const catalogue = 1_000_000
	big := listings(catalogue)
	m := visibility.NewMaterializer(p)
	start = time.Now()
	for _, l := range big {
		m.Upsert(l)
	}
	el := time.Since(start)
	out["materialise"] = map[string]any{"listings": catalogue, "seconds": el.Seconds(), "listings_per_second": float64(catalogue) / el.Seconds()}

	// 3. ramp FR->DE from 20% to 30%: incremental against a full recompute
	start = time.Now()
	changes, stats := m.Activate(network(3000))
	inc := time.Since(start)
	start = time.Now()
	mismatches := m.RecomputeAll()
	full := time.Since(start)
	out["ramp_step"] = map[string]any{
		"from_bp": 2000, "to_bp": 3000, "catalogue": catalogue,
		"scanned": stats.Scanned, "recomputed": stats.Recomputed, "index_writes": len(changes),
		"incremental_ms": float64(inc.Microseconds()) / 1000, "full_recompute_ms": float64(full.Microseconds()) / 1000,
		"mismatches_after": mismatches,
	}

	// 4. the event pipeline: listing events through the log into the index
	ctx := context.Background()
	reg := obs.NewRegistry()
	lg := stream.NewMemory(16)
	idx := index.NewMemory()
	pipe := &visibility.Pipeline{Log: lg, Mat: visibility.NewMaterializer(p), Index: idx, Metrics: visibility.NewMetrics(reg), Batch: 2000}
	const events = 200_000
	evs := listings(events)
	start = time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		processed := 0
		for processed < events {
			n, err := pipe.Step(ctx, 50*time.Millisecond)
			if err != nil {
				log.Fatal(err)
			}
			processed += n
		}
	}()
	for _, l := range evs {
		b, _ := json.Marshal(visibility.ListingEvent{Listing: l, Title: "listing " + strconv.FormatUint(l.ID, 10), At: time.Now(), CreatedAt: time.Now()})
		_ = lg.Publish(ctx, visibility.TopicListings, []byte(strconv.FormatUint(l.ID, 10)), b)
	}
	<-done
	el = time.Since(start)
	out["pipeline_backlog"] = map[string]any{"events": events, "seconds": el.Seconds(), "events_per_second": float64(events) / el.Seconds()}

	// steady state: events arrive at a fixed rate, the pipeline keeps up, and the lag is what a buyer would notice
	const rate, steady = 5_000, 25_000
	reg2 := obs.NewRegistry()
	pipe2 := &visibility.Pipeline{Log: lg, Mat: pipe.Mat, Index: idx, Metrics: visibility.NewMetrics(reg2), Batch: 500}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := pipe2.Step(ctx, 5*time.Millisecond); err != nil {
				log.Fatal(err)
			}
		}
	}()
	tick := time.NewTicker(time.Second / rate * 10)
	sent := 0
	for sent < steady {
		<-tick.C
		for i := 0; i < 10; i++ {
			l := evs[sent%len(evs)]
			b, _ := json.Marshal(visibility.ListingEvent{Listing: l, Title: "listing", At: time.Now(), CreatedAt: time.Now()})
			_ = lg.Publish(ctx, visibility.TopicListings, []byte(strconv.FormatUint(l.ID, 10)), b)
			sent++
		}
	}
	tick.Stop()
	for {
		if lagN, _ := lg.Lag(visibility.Group, visibility.TopicListings); lagN == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	lag := pipe2.Metrics.ListingLag
	out["pipeline_steady"] = map[string]any{
		"events": steady, "events_per_second": rate,
		"lag_p50_ms": 1000 * lag.Quantile(0.5), "lag_p99_ms": 1000 * lag.Quantile(0.99),
	}
	time.Sleep(20 * time.Millisecond)
	// a policy activation through the same pipeline: from publish to the last partial update
	next := network(5000)
	next.Version = 2
	activated := time.Now()
	b, _ := json.Marshal(visibility.PolicyEvent{Policy: *next, At: activated})
	_ = lg.Publish(ctx, visibility.TopicPolicies, []byte("policy"), b)
	if _, err := pipe.Step(ctx, 0); err != nil {
		log.Fatal(err)
	}
	out["policy_activation"] = map[string]any{
		"catalogue": events, "ms_to_last_index_write": float64(time.Since(activated).Microseconds()) / 1000,
		"partial_updates": pipe.Metrics.IndexWrites.Value("visibility"),
	}

	// 5. feed balancing alone
	rng := rand.New(rand.NewPCG(3, 3))
	dom, cross := make([]feed.Item, 150), make([]feed.Item, 50)
	for i := range dom {
		dom[i] = feed.Item{ID: uint64(i), Score: 1 - float64(i)/150}
	}
	for i := range cross {
		cross[i] = feed.Item{ID: uint64(1000 + i), Score: rng.Float64(), CrossBorder: true}
	}
	slices.SortFunc(cross, func(a, b feed.Item) int {
		if a.Score > b.Score {
			return -1
		}
		return 1
	})
	start = time.Now()
	const merges = 200_000
	for i := 0; i < merges; i++ {
		feed.Merge(dom, cross, 24, feed.Rules{Window: 12, Min: 2, Max: 4})
	}
	out["feed_merge_us"] = float64(time.Since(start).Microseconds()) / merges

	// 6. the feed endpoint over HTTP
	srv := &api.Server{Log: lg, Mat: pipe.Mat, Index: idx, Registry: obs.NewRegistry(), FeedRules: map[string]feed.Rules{"DE": {Window: 12, Min: 2, Max: 4}}}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cands := make([]feed.Item, 200)
	for i := range cands {
		cands[i] = feed.Item{ID: uint64(rng.IntN(events) + 1), Score: rng.Float64()}
	}
	body, _ := json.Marshal(api.FeedRequest{Market: "DE", K: 24, Candidates: cands})
	var lat []time.Duration
	client := ts.Client()
	for i := 0; i < 5000; i++ {
		t := time.Now()
		resp, err := client.Post(ts.URL+"/v1/feed", "application/json", bytes.NewReader(body))
		if err != nil {
			log.Fatal(err)
		}
		_, _ = (&bytes.Buffer{}).ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("feed: %d", resp.StatusCode)
		}
		lat = append(lat, time.Since(t))
	}
	out["feed_http"] = map[string]any{"requests": len(lat), "candidates": len(cands), "p50_us": pct(lat, 0.5), "p99_us": pct(lat, 0.99)}

	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		log.Fatalf("%v: %v", err, out)
	}
	if err := os.WriteFile("reports/bench.json", append(b, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(b))
}
