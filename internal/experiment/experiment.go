// Package experiment asks the question a content-visibility team faces before connecting two markets: what will
// the connection do to GMV and to each side's sell-through, and which experiment design measures that honestly?
// Every replication runs the same world several times under different visibility (forks share listings, buyers
// and coin flips), so the true effect of a full connection is known and each design's estimate can be scored.
package experiment

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"sync"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/feed"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/market"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
)

// Lab is the study's configuration.
type Lab struct {
	Seeds               int        `json:"seeds"`
	Workers             int        `json:"workers"`
	Salt                string     `json:"salt"`
	RampBP              []int      `json:"ramp_bp"`
	ListingSplitBP      int        `json:"listing_split_bp"`
	SwitchbackBlockDays int        `json:"switchback_block_days"`
	FeedCap             feed.Rules `json:"feed_cap"`
	Bootstrap           int        `json:"bootstrap"`
	CI                  float64    `json:"ci"`
	Guardrail           struct {
		MaxDomesticSTRDropPP float64 `json:"max_domestic_str_drop_pp"`
	} `json:"guardrail"`
}

func LoadLab(path string) (Lab, error) {
	var l Lab
	b, err := os.ReadFile(path)
	if err != nil {
		return l, err
	}
	return l, json.Unmarshal(b, &l)
}

// Run is one simulation of the test window under one design.
type Run struct {
	GMV          float64
	CrossGMV     float64
	BuyerGMV     []float64
	STR          []float64 // per seller market
	DailyGMV     []float64
	DayOpen      []bool
	ListingValue []float64 // per listing created in the STR window: value sold within the horizon (0 if not)
	ListingCross []bool    // per listing: in the cross-border treated half
	ListingMkt   []int
}

// Design decides visibility for one day.
type Design struct {
	Name    string
	Balance *feed.Rules
	vis     func(w *market.World, day int) market.Visibility
	open    func(day int) bool // switchback: whether the connection is open on a day
}

// ConnectionPolicy connects every pair of markets at exposure bp, on every channel, with heavy items excluded.
func ConnectionPolicy(cfg market.Config, bp int, salt string) *policy.Policy {
	p := &policy.Policy{Version: 1}
	for _, m := range cfg.Markets {
		p.Markets = append(p.Markets, m.Code)
	}
	for _, b := range cfg.Markets {
		for _, s := range cfg.Markets {
			if b.Code == s.Code {
				continue
			}
			r := policy.Rule{Buyer: b.Code, Seller: s.Code, ExposureBP: bp, Salt: salt, Channels: policy.Channels, MaxWeightGrams: s.MaxWeightGrams}
			switch {
			case bp <= 0:
				r.State, r.ExposureBP = policy.Closed, 0
			case bp >= policy.Buckets:
				r.State, r.ExposureBP = policy.Open, policy.Buckets
			default:
				r.State = policy.Ramping
			}
			p.Rules = append(p.Rules, r)
		}
	}
	if err := p.Validate(); err != nil {
		panic(err)
	}
	return p
}

func viaPolicy(p *policy.Policy) func(w *market.World, day int) market.Visibility {
	return func(w *market.World, _ int) market.Visibility {
		codes := w.Codes()
		return func(b int, l *market.Listing) bool {
			return p.Visible(w.PolicyListing(l), codes[w.Buyers[b].Market], policy.Feed)
		}
	}
}

func domesticOnly(w *market.World, _ int) market.Visibility {
	return func(b int, l *market.Listing) bool { return l.Market == w.Buyers[b].Market }
}

// Designs builds the designs of one study.
func Designs(cfg market.Config, lab Lab) map[string]Design {
	open := ConnectionPolicy(cfg, policy.Buckets, lab.Salt)
	ds := map[string]Design{
		"control": {Name: "control", vis: domesticOnly},
		"open":    {Name: "open", vis: viaPolicy(open)},
		"buyer_split": {Name: "buyer_split", vis: func(w *market.World, day int) market.Visibility {
			treated := viaPolicy(open)(w, day)
			return func(b int, l *market.Listing) bool {
				if buyerTreated(b) {
					return treated(b, l)
				}
				return l.Market == w.Buyers[b].Market
			}
		}},
		"switchback": {Name: "switchback"},
	}
	cap := lab.FeedCap
	ds["open_capped"] = Design{Name: "open_capped", vis: viaPolicy(open), Balance: &cap}
	for _, bp := range lab.RampBP {
		name := rampName(bp)
		if bp >= policy.Buckets {
			continue // the open design
		}
		ds[name] = Design{Name: name, vis: viaPolicy(ConnectionPolicy(cfg, bp, lab.Salt))}
	}
	return ds
}

func rampName(bp int) string {
	if bp >= policy.Buckets {
		return "open"
	}
	return "ramp_" + itoa(bp)
}

func itoa(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func buyerTreated(b int) bool {
	h := uint64(b)*0x9E3779B97F4A7C15 + 0x7F4A7C15
	h ^= h >> 31
	return h&1 == 0
}

// Simulate runs one design in one replication.
func Simulate(cfg market.Config, lab Lab, seed uint64, d Design) Run {
	w := market.New(cfg, seed)
	w.Balance = d.Balance
	T, H := cfg.TestDays, cfg.STRHorizonDays
	r := Run{BuyerGMV: make([]float64, len(w.Buyers)), DailyGMV: make([]float64, T), DayOpen: make([]bool, T)}
	openPolicy := viaPolicy(ConnectionPolicy(cfg, policy.Buckets, lab.Salt))
	// switchback: blocks of days alternate, starting open or closed by the seed
	startOpen := seed%2 == 0
	for day := -cfg.WarmupDays; day < T; day++ {
		w.List(day)
		var vis market.Visibility
		switch {
		case day < 0:
			vis = domesticOnly(w, day)
		case d.Name == "switchback":
			isOpen := (day/lab.SwitchbackBlockDays)%2 == 0 == startOpen
			r.DayOpen[day] = isOpen
			if isOpen {
				vis = openPolicy(w, day)
			} else {
				vis = domesticOnly(w, day)
			}
		default:
			vis = d.vis(w, day)
		}
		for _, p := range w.Browse(day, vis) {
			if day < 0 {
				continue
			}
			r.GMV += p.Price
			r.BuyerGMV[p.Buyer] += p.Price
			r.DailyGMV[day] += p.Price
			if p.Cross {
				r.CrossGMV += p.Price
			}
		}
	}
	sold := make([]float64, len(cfg.Markets))
	listed := make([]float64, len(cfg.Markets))
	split := ConnectionPolicy(cfg, lab.ListingSplitBP, lab.Salt)
	codes := w.Codes()
	for _, l := range w.Listings {
		if l.CreatedOn < 0 || l.CreatedOn > T-H {
			continue
		}
		listed[l.Market]++
		value := 0.0
		if l.SoldOn >= 0 && l.SoldOn-l.CreatedOn < H {
			sold[l.Market]++
			value = l.Price
		}
		r.ListingValue = append(r.ListingValue, value)
		other := codes[(l.Market+1)%len(codes)]
		r.ListingCross = append(r.ListingCross, split.Visible(w.PolicyListing(&market.Listing{ID: l.ID, Market: l.Market, Category: l.Category, WeightG: l.WeightG, SoldOn: -1}), other, policy.Feed))
		r.ListingMkt = append(r.ListingMkt, l.Market)
	}
	r.STR = make([]float64, len(cfg.Markets))
	for m := range sold {
		r.STR[m] = sold[m] / math.Max(listed[m], 1)
	}
	return r
}

// Estimate is one design's estimate of the GMV lift of a full connection, with its own interval.
type Estimate struct {
	Value float64 `json:"value"`
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
}

func quantiles(xs []float64, ci float64) (float64, float64) {
	s := slices.Clone(xs)
	sort.Float64s(s)
	lo := s[int(math.Floor((1-ci)/2*float64(len(s)-1)))]
	hi := s[int(math.Ceil((1+ci)/2*float64(len(s)-1)))]
	return lo, hi
}

// BuyerSplit: GMV per treated buyer over GMV per control buyer, minus one. Interval: bootstrap over buyers.
func BuyerSplit(r Run, lab Lab, seed uint64) Estimate {
	var t, c []float64
	for b, v := range r.BuyerGMV {
		if buyerTreated(b) {
			t = append(t, v)
		} else {
			c = append(c, v)
		}
	}
	stat := func(t, c []float64) float64 { return mean(t)/mean(c) - 1 }
	return bootstrap2(t, c, stat, lab, seed)
}

// ListingSplit: value sold per treated listing over value per control listing, minus one. The treated half is
// visible across the border; the estimate is read as "what if every listing were". Bootstrap over listings.
func ListingSplit(r Run, lab Lab, seed uint64) Estimate {
	var t, c []float64
	for i, v := range r.ListingValue {
		if r.ListingCross[i] {
			t = append(t, v)
		} else {
			c = append(c, v)
		}
	}
	stat := func(t, c []float64) float64 { return mean(t)/mean(c) - 1 }
	return bootstrap2(t, c, stat, lab, seed)
}

// Switchback: mean daily GMV on open days over closed days, minus one. Bootstrap over blocks of days.
func Switchback(r Run, lab Lab, seed uint64) Estimate {
	type block struct {
		gmv  float64
		open bool
	}
	var blocks []block
	for d := 0; d < len(r.DailyGMV); d += lab.SwitchbackBlockDays {
		b := block{open: r.DayOpen[d]}
		for i := d; i < min(d+lab.SwitchbackBlockDays, len(r.DailyGMV)); i++ {
			b.gmv += r.DailyGMV[i]
		}
		blocks = append(blocks, b)
	}
	var t, c []float64
	for _, b := range blocks {
		if b.open {
			t = append(t, b.gmv)
		} else {
			c = append(c, b.gmv)
		}
	}
	stat := func(t, c []float64) float64 { return mean(t)/mean(c) - 1 }
	return bootstrap2(t, c, stat, lab, seed)
}

func bootstrap2(t, c []float64, stat func(t, c []float64) float64, lab Lab, seed uint64) Estimate {
	rng := rand.New(rand.NewPCG(seed, 77))
	vals := make([]float64, lab.Bootstrap)
	bt, bc := make([]float64, len(t)), make([]float64, len(c))
	for i := range vals {
		for j := range bt {
			bt[j] = t[rng.IntN(len(t))]
		}
		for j := range bc {
			bc[j] = c[rng.IntN(len(c))]
		}
		vals[i] = stat(bt, bc)
	}
	lo, hi := quantiles(vals, lab.CI)
	return Estimate{Value: stat(t, c), Lo: lo, Hi: hi}
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / math.Max(float64(len(xs)), 1)
}

// RunAll simulates every design in every replication, in parallel.
func RunAll(cfg market.Config, lab Lab) map[string][]Run {
	designs := Designs(cfg, lab)
	names := make([]string, 0, len(designs))
	for n := range designs {
		names = append(names, n)
	}
	sort.Strings(names)
	type job struct {
		name string
		seed int
	}
	out := map[string][]Run{}
	for _, n := range names {
		out[n] = make([]Run, lab.Seeds)
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < max(1, lab.Workers); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				r := Simulate(cfg, lab, uint64(j.seed+1), designs[j.name])
				mu.Lock()
				out[j.name][j.seed] = r
				mu.Unlock()
			}
		}()
	}
	for s := 0; s < lab.Seeds; s++ {
		for _, n := range names {
			jobs <- job{n, s}
		}
	}
	close(jobs)
	wg.Wait()
	return out
}
