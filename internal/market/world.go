// Package market is a simulated second-hand marketplace: sellers list unique items, buyers browse a feed of what
// they may see, and each item can be bought once. It holds the truth (what buyers like, how much cross-border
// shipping puts them off) and lets the platform's own policy code decide visibility. Only internal/market,
// internal/experiment and cmd/lab import it; the platform packages never do.
package market

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"slices"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/feed"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
)

// Config is a world's truth and size.
type Config struct {
	Name           string             `json:"name"`
	Markets        []MarketConfig     `json:"markets"`
	Categories     int                `json:"categories"`
	WarmupDays     int                `json:"warmup_days"`
	TestDays       int                `json:"test_days"`
	ListingTTLDays int                `json:"listing_ttl_days"`
	STRHorizonDays int                `json:"str_horizon_days"`
	FeedSize       int                `json:"feed_size"`
	Candidates     int                `json:"candidates"`
	Examination    float64            `json:"examination_decay"`
	BuyThreshold   float64            `json:"buy_threshold"`
	PriceWeight    float64            `json:"price_weight"`
	CrossBorder    CrossBorderConfig  `json:"cross_border"`
	Extra          map[string]float64 `json:"extra,omitempty"`
}

type MarketConfig struct {
	Code            string    `json:"code"`
	ListingsPerDay  int       `json:"listings_per_day"`
	Buyers          int       `json:"buyers"`
	SessionRate     float64   `json:"session_rate"`
	SupplyMix       []float64 `json:"supply_mix"`  // share of listings per category
	DemandMix       []float64 `json:"demand_mix"`  // share of buyer interest per category
	PriceLevel      float64   `json:"price_level"` // median price, in euros
	QualityMean     float64   `json:"quality_mean"`
	MaxWeightGrams  int       `json:"max_weight_grams"`
	HeavyCategories []int     `json:"heavy_categories"`
}

type CrossBorderConfig struct {
	Penalty      float64 `json:"penalty"`       // mean utility cost of shipping across the border
	PenaltySD    float64 `json:"penalty_sd"`    // spread across buyers: some barely care
	ShippingEUR  float64 `json:"shipping_eur"`  // added to the price a cross-border buyer pays
	DelayPenalty float64 `json:"delay_penalty"` // extra cost for slower delivery
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// Listing is an item in the world, with its truth.
type Listing struct {
	ID        uint64
	Market    int
	Category  int
	Price     float64
	Quality   float64 // how much buyers like it beyond price; not visible to the platform
	WeightG   int
	CreatedOn int // day index, warmup days are negative
	SoldOn    int // -1 while unsold
	SoldTo    int // buyer market, -1 while unsold
}

// Buyer is a person who browses.
type Buyer struct {
	Market    int
	Taste     []float64 // probability of browsing each category
	CrossCost float64   // this buyer's dislike of cross-border shipping
	Bias      float64   // general willingness to buy
}

// World is one replication. Everything random is drawn from streams keyed by (seed, day, purpose, entity), so
// forks of the same world under different policies see the same listings, buyers, sessions and coin flips.
type World struct {
	Cfg       Config
	Seed      uint64
	Listings  []*Listing
	Buyers    []Buyer
	codes     []string
	pools     [][]int // per (market, category): indices into Listings of active listings
	posInPool []int   // position of each listing in its pool, -1 when removed

	// Balance, when set, caps cross-border items in each feed with the platform's balancer.
	Balance *feed.Rules
}

func stream(seed uint64, parts ...uint64) *rand.Rand {
	h := seed*0x9E3779B97F4A7C15 + 0x632BE59BD9B4E019
	for _, p := range parts {
		h ^= p + 0x9E3779B97F4A7C15 + (h << 6) + (h >> 2)
	}
	return rand.New(rand.NewPCG(seed, h))
}

const (
	purposeBuyers = iota + 1
	purposeListings
	purposeSessions
)

// New builds a world: buyers now, listings day by day as the simulation runs.
func New(cfg Config, seed uint64) *World {
	w := &World{Cfg: cfg, Seed: seed}
	for _, m := range cfg.Markets {
		w.codes = append(w.codes, m.Code)
	}
	w.pools = make([][]int, len(cfg.Markets)*cfg.Categories)
	for mi, m := range cfg.Markets {
		rng := stream(seed, purposeBuyers, uint64(mi))
		for b := 0; b < m.Buyers; b++ {
			taste := make([]float64, cfg.Categories)
			total := 0.0
			for c := range taste {
				// gamma(0.6)-like spread around the market's demand mix: each buyer has a few favourite categories
				taste[c] = m.DemandMix[c] * math.Pow(rng.Float64(), 1.8)
				total += taste[c]
			}
			for c := range taste {
				taste[c] /= total
			}
			cost := cfg.CrossBorder.Penalty + cfg.CrossBorder.PenaltySD*rng.NormFloat64()
			w.Buyers = append(w.Buyers, Buyer{Market: mi, Taste: taste, CrossCost: math.Max(0, cost), Bias: 0.3 * rng.NormFloat64()})
		}
	}
	return w
}

// Codes are the market codes, in config order.
func (w *World) Codes() []string { return w.codes }

func (w *World) pool(market, category int) int { return market*w.Cfg.Categories + category }

func (w *World) addListing(l *Listing) {
	idx := len(w.Listings)
	w.Listings = append(w.Listings, l)
	p := w.pool(l.Market, l.Category)
	w.posInPool = append(w.posInPool, len(w.pools[p]))
	w.pools[p] = append(w.pools[p], idx)
}

func (w *World) remove(idx int) {
	l := w.Listings[idx]
	p := w.pool(l.Market, l.Category)
	pos := w.posInPool[idx]
	if pos < 0 {
		return
	}
	last := w.pools[p][len(w.pools[p])-1]
	w.pools[p][pos] = last
	w.posInPool[last] = pos
	w.pools[p] = w.pools[p][:len(w.pools[p])-1]
	w.posInPool[idx] = -1
}

// List creates the day's new listings (identical in every fork) and expires old ones.
func (w *World) List(day int) {
	for mi, m := range w.Cfg.Markets {
		rng := stream(w.Seed, purposeListings, uint64(mi), uint64(day+10_000))
		cdf := cumulative(m.SupplyMix)
		for i := 0; i < m.ListingsPerDay; i++ {
			c := pick(cdf, rng.Float64())
			weight := 200 + rng.IntN(1500)
			if slices.Contains(m.HeavyCategories, c) {
				weight = 3000 + rng.IntN(12_000)
			}
			id := uint64(day+10_000)<<32 | uint64(mi)<<24 | uint64(i)
			w.addListing(&Listing{
				ID: id, Market: mi, Category: c,
				Price:     m.PriceLevel * math.Exp(0.6*rng.NormFloat64()+0.15*float64(c%4)),
				Quality:   m.QualityMean + rng.NormFloat64(),
				WeightG:   weight,
				CreatedOn: day, SoldOn: -1, SoldTo: -1,
			})
		}
	}
	ttl := w.Cfg.ListingTTLDays
	for idx, l := range w.Listings {
		if l.SoldOn < 0 && w.posInPool[idx] >= 0 && day-l.CreatedOn >= ttl {
			w.remove(idx)
		}
	}
}

// PolicyListing is what the platform sees of a listing.
func (w *World) PolicyListing(l *Listing) policy.Listing {
	return policy.Listing{ID: l.ID, Seller: w.codes[l.Market], Category: categoryName(l.Category), WeightG: l.WeightG, Active: l.SoldOn < 0}
}

func categoryName(c int) string { return "c" + string(rune('a'+c)) }

// Visibility decides, for one buyer and one listing, whether the buyer may see it. Experiments that randomise
// buyers pass a function that depends on the buyer; policies that randomise listings use the platform's policy.
type Visibility func(buyer int, l *Listing) bool

type scored struct {
	idx  int
	util float64
}

// Purchase is one sale.
type Purchase struct {
	Buyer   int
	Listing int
	Price   float64
	Cross   bool
}

// Browse runs the day's sessions under a visibility rule and returns the purchases. Sold items leave every pool
// immediately: the next buyer cannot buy them, which is the interference a marketplace experiment has to face.
func (w *World) Browse(day int, visible Visibility) []Purchase {
	var out []Purchase
	k, cand := w.Cfg.FeedSize, w.Cfg.Candidates
	buf := make([]scored, 0, cand)
	exam := make([]float64, k)
	for i := range exam {
		exam[i] = math.Pow(1/float64(1+i), w.Cfg.Examination)
	}
	order := make([]int, len(w.Buyers))
	for i := range order {
		order[i] = i
	}
	shuffle := stream(w.Seed, purposeSessions, uint64(day+10_000), 0)
	shuffle.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	for _, b := range order {
		buyer := &w.Buyers[b]
		mc := w.Cfg.Markets[buyer.Market]
		rng := stream(w.Seed, purposeSessions, uint64(day+10_000), uint64(b)+1)
		if rng.Float64() >= mc.SessionRate {
			continue
		}
		c := pick(cumulative(buyer.Taste), rng.Float64())
		buf = buf[:0]
		// candidates: a random draw from every pool of this category, kept if the buyer may see the listing
		for m := range w.Cfg.Markets {
			p := w.pools[w.pool(m, c)]
			if len(p) == 0 {
				continue
			}
			share := cand / len(w.Cfg.Markets)
			for i := 0; i < share; i++ {
				idx := p[rng.IntN(len(p))]
				l := w.Listings[idx]
				if l.SoldOn >= 0 || !visible(b, l) {
					continue
				}
				buf = append(buf, scored{idx, w.utility(buyer, l)})
			}
		}
		// the platform's ranker sees a noisy version of the utility
		noise := make([]float64, len(buf))
		for i := range buf {
			noise[i] = buf[i].util + 0.8*rng.NormFloat64()
		}
		ranked := make([]int, len(buf))
		for i := range ranked {
			ranked[i] = i
		}
		slices.SortFunc(ranked, func(a, b int) int {
			switch {
			case noise[a] > noise[b]:
				return -1
			case noise[a] < noise[b]:
				return 1
			}
			return 0
		})
		if w.Balance != nil {
			ranked = w.balance(buyer.Market, buf, noise, ranked, k)
		}
		seen := map[int]bool{}
		for pos := 0; pos < k && pos < len(ranked); pos++ {
			s := buf[ranked[pos]]
			if seen[s.idx] {
				continue
			}
			seen[s.idx] = true
			if rng.Float64() >= exam[pos] {
				continue
			}
			l := w.Listings[s.idx]
			if l.SoldOn >= 0 {
				continue
			}
			if rng.Float64() < sigmoid(s.util-w.Cfg.BuyThreshold+buyer.Bias) {
				l.SoldOn, l.SoldTo = day, buyer.Market
				w.remove(s.idx)
				out = append(out, Purchase{Buyer: b, Listing: s.idx, Price: l.Price, Cross: l.Market != buyer.Market})
				break // one purchase per session
			}
		}
	}
	return out
}

// balance runs the platform's feed balancer over the ranked candidates (positions into buf).
func (w *World) balance(market int, buf []scored, noise []float64, ranked []int, k int) []int {
	var dom, cross []feed.Item
	for _, r := range ranked {
		it := feed.Item{ID: uint64(r), Score: noise[r], CrossBorder: w.Listings[buf[r].idx].Market != market}
		if it.CrossBorder {
			cross = append(cross, it)
		} else {
			dom = append(dom, it)
		}
	}
	merged := feed.Merge(dom, cross, k, *w.Balance)
	out := make([]int, len(merged))
	for i, it := range merged {
		out[i] = int(it.ID)
	}
	return out
}

func (w *World) utility(b *Buyer, l *Listing) float64 {
	price := l.Price
	u := l.Quality + 2*b.Taste[l.Category]
	if l.Market != b.Market {
		price += w.Cfg.CrossBorder.ShippingEUR
		u -= b.CrossCost + w.Cfg.CrossBorder.DelayPenalty
	}
	ref := w.Cfg.Markets[b.Market].PriceLevel
	return u - w.Cfg.PriceWeight*math.Log(price/ref)
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

func cumulative(p []float64) []float64 {
	out := make([]float64, len(p))
	s := 0.0
	for i, v := range p {
		s += v
		out[i] = s
	}
	for i := range out {
		out[i] /= s
	}
	return out
}

func pick(cdf []float64, u float64) int {
	i, _ := slices.BinarySearch(cdf, u)
	return min(i, len(cdf)-1)
}
