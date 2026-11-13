// Package policy holds the visibility rules: which buyer market sees which seller market's listings, on which
// channels, and how far a connection has been ramped. One policy is evaluated by every channel (search, feed,
// similar items), so a listing is never visible in search and hidden in the feed by accident.
package policy

import (
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
)

// Channel is a surface that shows listings to buyers.
type Channel string

const (
	Search  Channel = "search"
	Feed    Channel = "feed"
	Similar Channel = "similar"
)

// Channels lists every channel, in a fixed order used for bit positions and reports.
var Channels = []Channel{Search, Feed, Similar}

// State of a connection between a buyer market and a seller market.
type State string

const (
	Closed  State = "closed"
	Ramping State = "ramping"
	Open    State = "open"
)

// Buckets is the resolution of a ramp: exposure is expressed in basis points of listings.
const Buckets = 10_000

// Rule connects one buyer market to one seller market.
type Rule struct {
	Buyer              string    `json:"buyer"`
	Seller             string    `json:"seller"`
	State              State     `json:"state"`
	ExposureBP         int       `json:"exposure_bp"` // share of the seller market's listings shown, in basis points
	Salt               string    `json:"salt"`        // fixes which listings fall in the ramp; a new salt reshuffles them
	Channels           []Channel `json:"channels"`
	ExcludedCategories []string  `json:"excluded_categories,omitempty"`
	MaxWeightGrams     int       `json:"max_weight_grams,omitempty"` // 0: no limit; heavy items may not ship across
}

// Policy is one immutable, versioned rule set.
type Policy struct {
	Version int      `json:"version"`
	Markets []string `json:"markets"`
	Rules   []Rule   `json:"rules"`
	Note    string   `json:"note,omitempty"`

	index map[[2]string]int
}

// Listing is what visibility is decided from. It carries no ranking signals.
type Listing struct {
	ID       uint64 `json:"id"`
	Seller   string `json:"seller"`
	Category string `json:"category"`
	WeightG  int    `json:"weight_g"`
	Active   bool   `json:"active"`
}

// Validate checks a policy and builds its lookup index. A policy is used only after Validate succeeds.
func (p *Policy) Validate() error {
	if len(p.Markets) == 0 || len(p.Markets) > 64 {
		return errors.New("a policy needs between 1 and 64 markets")
	}
	known := map[string]bool{}
	for _, m := range p.Markets {
		if m == "" || known[m] {
			return fmt.Errorf("market %q is empty or listed twice", m)
		}
		known[m] = true
	}
	p.index = make(map[[2]string]int, len(p.Rules))
	var errs []error
	for i, r := range p.Rules {
		key := [2]string{r.Buyer, r.Seller}
		where := fmt.Sprintf("rule %d (%s sees %s)", i, r.Buyer, r.Seller)
		switch {
		case !known[r.Buyer] || !known[r.Seller]:
			errs = append(errs, fmt.Errorf("%s: unknown market", where))
		case r.Buyer == r.Seller:
			errs = append(errs, fmt.Errorf("%s: a market always sees itself; no rule needed", where))
		}
		if _, dup := p.index[key]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate rule for this pair", where))
		}
		p.index[key] = i
		switch r.State {
		case Closed:
			if r.ExposureBP != 0 {
				errs = append(errs, fmt.Errorf("%s: a closed connection has exposure 0", where))
			}
		case Open:
			if r.ExposureBP != Buckets {
				errs = append(errs, fmt.Errorf("%s: an open connection has exposure %d", where, Buckets))
			}
		case Ramping:
			if r.ExposureBP <= 0 || r.ExposureBP >= Buckets {
				errs = append(errs, fmt.Errorf("%s: a ramp is strictly between 0 and %d bp", where, Buckets))
			}
			if r.Salt == "" {
				errs = append(errs, fmt.Errorf("%s: a ramp needs a salt", where))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: unknown state %q", where, r.State))
		}
		for _, c := range r.Channels {
			if !slices.Contains(Channels, c) {
				errs = append(errs, fmt.Errorf("%s: unknown channel %q", where, c))
			}
		}
		if r.MaxWeightGrams < 0 {
			errs = append(errs, fmt.Errorf("%s: negative weight limit", where))
		}
	}
	return errors.Join(errs...)
}

// Rule returns the rule for a buyer and seller market, if any.
func (p *Policy) Rule(buyer, seller string) (Rule, bool) {
	i, ok := p.index[[2]string{buyer, seller}]
	if !ok {
		return Rule{}, false
	}
	return p.Rules[i], true
}

// Bucket places a listing in [0, Buckets) for a ramp salt. The same listing and salt always land in the same
// bucket, so raising a ramp only adds listings and lowering it only removes them.
func Bucket(listing uint64, salt string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(salt))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write(strconv.AppendUint(nil, listing, 10))
	return int(h.Sum64() % Buckets)
}

// Visible decides whether a buyer in market `buyer` may see the listing on channel ch.
func (p *Policy) Visible(l Listing, buyer string, ch Channel) bool {
	if !l.Active {
		return false
	}
	if l.Seller == buyer {
		return true
	}
	r, ok := p.Rule(buyer, l.Seller)
	if !ok || r.State == Closed || !slices.Contains(r.Channels, ch) {
		return false
	}
	if slices.Contains(r.ExcludedCategories, l.Category) {
		return false
	}
	if r.MaxWeightGrams > 0 && l.WeightG > r.MaxWeightGrams {
		return false
	}
	return r.State == Open || Bucket(l.ID, r.Salt) < r.ExposureBP
}

// MarketBit is the bit of a market in a visibility mask.
func (p *Policy) MarketBit(market string) (uint64, bool) {
	i := slices.Index(p.Markets, market)
	if i < 0 {
		return 0, false
	}
	return 1 << uint(i), true
}

// Mask returns, per channel, the bitmask of buyer markets that may see the listing.
func (p *Policy) Mask(l Listing) [3]uint64 {
	var out [3]uint64
	for ci, ch := range Channels {
		for mi, m := range p.Markets {
			if p.Visible(l, m, ch) {
				out[ci] |= 1 << uint(mi)
			}
		}
	}
	return out
}

// MarketsOf expands a mask into market codes.
func (p *Policy) MarketsOf(mask uint64) []string {
	var out []string
	for i, m := range p.Markets {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, m)
		}
	}
	return out
}
