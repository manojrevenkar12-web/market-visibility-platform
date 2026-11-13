package policy

import (
	"math/rand/v2"
	"strings"
	"testing"
)

var markets = []string{"FR", "DE", "BE", "ES"}

func ramp(buyer, seller string, bp int, salt string) Rule {
	state := Ramping
	switch bp {
	case 0:
		state = Closed
	case Buckets:
		state = Open
	}
	return Rule{Buyer: buyer, Seller: seller, State: state, ExposureBP: bp, Salt: salt, Channels: Channels}
}

func mustPolicy(t *testing.T, rules ...Rule) *Policy {
	t.Helper()
	p := &Policy{Version: 1, Markets: markets, Rules: rules}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateRejectsInconsistentRules(t *testing.T) {
	bad := &Policy{Markets: markets, Rules: []Rule{
		{Buyer: "DE", Seller: "DE", State: Open, ExposureBP: Buckets},
		{Buyer: "DE", Seller: "FR", State: Ramping, ExposureBP: 0},
		{Buyer: "DE", Seller: "FR", State: Open, ExposureBP: 5},
		{Buyer: "XX", Seller: "FR", State: Closed},
		{Buyer: "ES", Seller: "FR", State: Ramping, ExposureBP: 100, Channels: []Channel{"radio"}},
	}}
	err := bad.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"always sees itself", "strictly between", "duplicate", "unknown market", "unknown channel", "needs a salt", "exposure 10000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestDomesticAlwaysVisibleAndInactiveNever(t *testing.T) {
	p := mustPolicy(t)
	l := Listing{ID: 7, Seller: "DE", Category: "shoes", Active: true}
	for _, ch := range Channels {
		if !p.Visible(l, "DE", ch) || p.Visible(l, "FR", ch) {
			t.Fatal("domestic must be visible, cross-border closed by default")
		}
	}
	l.Active = false
	if p.Visible(l, "DE", Search) {
		t.Fatal("a sold or deleted listing is visible nowhere")
	}
}

func TestRampHitsItsShareAndOnlyGrows(t *testing.T) {
	ids := make([]Listing, 200_000)
	for i := range ids {
		ids[i] = Listing{ID: uint64(i + 1), Seller: "FR", Active: true}
	}
	var prev map[uint64]bool
	for _, bp := range []int{500, 1000, 2500, 5000, 9000} {
		p := mustPolicy(t, ramp("DE", "FR", bp, "fr-de-2026"))
		now := map[uint64]bool{}
		for _, l := range ids {
			if p.Visible(l, "DE", Feed) {
				now[l.ID] = true
			}
		}
		share := float64(len(now)) / float64(len(ids))
		if d := share - float64(bp)/Buckets; d > 0.005 || d < -0.005 {
			t.Fatalf("ramp %d bp shows %.4f of listings", bp, share)
		}
		for id := range prev {
			if !now[id] {
				t.Fatalf("listing %d disappeared when the ramp grew to %d bp", id, bp)
			}
		}
		prev = now
	}
}

func TestExclusionsAndWeight(t *testing.T) {
	r := ramp("DE", "FR", Buckets, "")
	r.ExcludedCategories = []string{"furniture"}
	r.MaxWeightGrams = 2000
	r.Channels = []Channel{Search}
	p := mustPolicy(t, r)
	ok := Listing{ID: 1, Seller: "FR", Category: "shoes", WeightG: 900, Active: true}
	if !p.Visible(ok, "DE", Search) || p.Visible(ok, "DE", Feed) {
		t.Fatal("channel list not honoured")
	}
	if p.Visible(Listing{ID: 2, Seller: "FR", Category: "furniture", Active: true}, "DE", Search) {
		t.Fatal("excluded category shown")
	}
	if p.Visible(Listing{ID: 3, Seller: "FR", Category: "shoes", WeightG: 2500, Active: true}, "DE", Search) {
		t.Fatal("too heavy to ship, shown")
	}
}

// Every listing whose visibility differs between two policies must be covered by Diff. Random policy pairs.
func TestDiffCoversEveryChange(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	salts := []string{"a", "b"}
	randomRule := func(b, s string) Rule {
		bp := []int{0, 300, 2500, 5000, Buckets}[rng.IntN(5)]
		r := ramp(b, s, bp, salts[rng.IntN(2)])
		if rng.IntN(4) == 0 {
			r.Channels = []Channel{Search}
		}
		if rng.IntN(5) == 0 {
			r.ExcludedCategories = []string{"bags"}
		}
		return r
	}
	randomPolicy := func() *Policy {
		var rules []Rule
		for _, b := range markets {
			for _, s := range markets {
				if b != s && rng.IntN(3) > 0 {
					rules = append(rules, randomRule(b, s))
				}
			}
		}
		return mustPolicy(t, rules...)
	}
	cats := []string{"bags", "shoes"}
	for trial := 0; trial < 200; trial++ {
		a, b := randomPolicy(), randomPolicy()
		changes := Diff(a, b)
		for i := 0; i < 400; i++ {
			l := Listing{ID: uint64(rng.Int64N(1 << 40)), Seller: markets[rng.IntN(4)], Category: cats[rng.IntN(2)], Active: true}
			for _, buyer := range markets {
				for _, ch := range Channels {
					if a.Visible(l, buyer, ch) == b.Visible(l, buyer, ch) {
						continue
					}
					covered := false
					for _, c := range changes {
						if c.Buyer == buyer && c.Affects(l) {
							covered = true
						}
					}
					if !covered {
						t.Fatalf("trial %d: listing %+v changed for %s/%s but no change covers it", trial, l, buyer, ch)
					}
				}
			}
		}
	}
}

func TestRampDiffIsANarrowBand(t *testing.T) {
	a := mustPolicy(t, ramp("DE", "FR", 1000, "s"))
	b := mustPolicy(t, ramp("DE", "FR", 2000, "s"))
	ch := Diff(a, b)
	if len(ch) != 1 || ch[0].Whole || ch[0].Lo != 1000 || ch[0].Hi != 2000 {
		t.Fatalf("got %+v", ch)
	}
	c := mustPolicy(t, ramp("DE", "FR", 2000, "other"))
	if d := Diff(b, c); len(d) != 1 || !d[0].Whole {
		t.Fatalf("a new salt reshuffles the whole market: %+v", d)
	}
}
