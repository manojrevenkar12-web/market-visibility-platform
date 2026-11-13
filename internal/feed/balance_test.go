package feed

import (
	"math/rand/v2"
	"testing"
)

func list(rng *rand.Rand, n int, cross bool, base uint64) []Item {
	out := make([]Item, n)
	s := 1.0
	for i := range out {
		s -= rng.Float64() * 0.05
		out[i] = Item{ID: base + uint64(i), Score: s, CrossBorder: cross}
	}
	return out
}

func TestMergeKeepsWindowBoundsAndSourceOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for trial := 0; trial < 2000; trial++ {
		r := Rules{Window: 2 + rng.IntN(12)}
		r.Max = rng.IntN(r.Window + 1)
		r.Min = rng.IntN(r.Max + 1)
		dom := list(rng, rng.IntN(60), false, 0)
		cross := list(rng, rng.IntN(60), true, 1_000)
		k := 1 + rng.IntN(50)
		out := Merge(dom, cross, k, r)
		if len(out) > k {
			t.Fatalf("returned %d > k=%d", len(out), k)
		}
		seen := map[uint64]bool{}
		for _, it := range out {
			if seen[it.ID] {
				t.Fatal("duplicate item")
			}
			seen[it.ID] = true
		}
		// source order kept: the domestic items appear in their ranked order, and so do the cross-border ones
		di, ci := 0, 0
		for _, it := range out {
			if it.CrossBorder {
				if it.ID != cross[ci].ID {
					t.Fatal("cross-border order changed")
				}
				ci++
			} else {
				if it.ID != dom[di].ID {
					t.Fatal("domestic order changed")
				}
				di++
			}
		}
		// every window of positions holds at most Max cross-border items
		for start := 0; start+r.Window <= len(out); start++ {
			c := 0
			for _, it := range out[start : start+r.Window] {
				if it.CrossBorder {
					c++
				}
			}
			if c > r.Max {
				t.Fatalf("window at %d has %d > max %d (%+v)", start, c, r.Max, r)
			}
			// and at least Min, as long as cross-border items had not run out before the window ended
			if c < r.Min && ci < len(cross) {
				t.Fatalf("window at %d has %d < min %d with items left (%+v)", start, c, r.Min, r)
			}
		}
	}
}

func TestMergePicksTheBetterHeadWhenFree(t *testing.T) {
	dom := []Item{{ID: 1, Score: 0.9}, {ID: 2, Score: 0.5}}
	cross := []Item{{ID: 10, Score: 0.7, CrossBorder: true}}
	out := Merge(dom, cross, 3, Rules{Window: 3, Min: 0, Max: 3})
	if out[0].ID != 1 || out[1].ID != 10 || out[2].ID != 2 {
		t.Fatalf("got %+v", out)
	}
}

func TestMinGuaranteesExposureForANewMarket(t *testing.T) {
	dom := []Item{{ID: 1, Score: 0.9}, {ID: 2, Score: 0.8}, {ID: 3, Score: 0.7}, {ID: 4, Score: 0.6}}
	cross := []Item{{ID: 10, Score: 0.1, CrossBorder: true}}
	out := Merge(dom, cross, 4, Rules{Window: 4, Min: 1, Max: 2})
	if out[3].ID != 10 {
		t.Fatalf("the low-scored cross-border item must take the last slot of the window: %+v", out)
	}
}

func TestValidate(t *testing.T) {
	if (Rules{Window: 10, Min: 3, Max: 2}).Validate() == nil || (Rules{Window: 10, Min: 1, Max: 3}).Validate() != nil {
		t.Fatal("validate")
	}
}
