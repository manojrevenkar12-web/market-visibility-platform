package experiment

import (
	"testing"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/feed"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/market"
)

func tiny(t *testing.T) (market.Config, Lab) {
	t.Helper()
	cfg, err := market.LoadConfig("../../config/world.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.WarmupDays, cfg.TestDays, cfg.STRHorizonDays = 6, 8, 4
	for i := range cfg.Markets {
		cfg.Markets[i].ListingsPerDay /= 10
		cfg.Markets[i].Buyers /= 10
	}
	lab, err := LoadLab("../../config/lab.json")
	if err != nil {
		t.Fatal(err)
	}
	lab.Seeds, lab.Bootstrap = 3, 50
	return cfg, lab
}

func TestForksShareListingsAndClosedMeansDomestic(t *testing.T) {
	cfg, lab := tiny(t)
	d := Designs(cfg, lab)
	a := Simulate(cfg, lab, 1, d["control"])
	b := Simulate(cfg, lab, 1, d["open"])
	if a.CrossGMV != 0 {
		t.Fatal("the closed control sold across the border")
	}
	if b.CrossGMV == 0 {
		t.Fatal("an open connection sold nothing across the border")
	}
	if len(a.ListingValue) != len(b.ListingValue) {
		t.Fatal("forks must list the same items")
	}
	again := Simulate(cfg, lab, 1, d["open"])
	if again.GMV != b.GMV {
		t.Fatal("a replication must be reproducible")
	}
}

func TestRampExposureOrdersCrossBorderShare(t *testing.T) {
	cfg, lab := tiny(t)
	d := Designs(cfg, lab)
	prev := -1.0
	for _, name := range []string{"control", "ramp_1000", "ramp_5000", "open"} {
		share := 0.0
		for s := uint64(1); s <= 3; s++ {
			r := Simulate(cfg, lab, s, d[name])
			share += r.CrossGMV / r.GMV
		}
		if share <= prev {
			t.Fatalf("%s: cross-border share %.4f did not grow (prev %.4f)", name, share, prev)
		}
		prev = share
	}
}

func TestBalancerCapsCrossBorderInTheSimulatedFeed(t *testing.T) {
	cfg, lab := tiny(t)
	d := Designs(cfg, lab)
	none := feed.Rules{Window: cfg.FeedSize, Min: 0, Max: 0}
	closed := d["open"]
	closed.Balance = &none
	r := Simulate(cfg, lab, 2, closed)
	if r.CrossGMV != 0 {
		t.Fatal("a cap of zero must keep cross-border items out of the feed")
	}
}

func TestSummarizeScoresEveryDesign(t *testing.T) {
	cfg, lab := tiny(t)
	rep := Summarize(cfg, lab, RunAll(cfg, lab))
	if len(rep.Designs) != 3 || len(rep.Ramp) != len(lab.RampBP) {
		t.Fatalf("designs %d ramp %d", len(rep.Designs), len(rep.Ramp))
	}
	for _, d := range rep.Designs {
		if len(d.Estimates) != lab.Seeds || d.Coverage < 0 || d.Coverage > 1 {
			t.Fatalf("%+v", d)
		}
	}
}
