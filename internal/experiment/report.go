package experiment

import (
	"math"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/market"
)

// Interval is a mean over replications with a normal-approximation interval of the mean.
type Interval struct {
	Mean float64 `json:"mean"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
}

func summarize(xs []float64) Interval {
	m := mean(xs)
	if len(xs) < 2 {
		return Interval{m, m, m}
	}
	v := 0.0
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	se := math.Sqrt(v/float64(len(xs)-1)) / math.Sqrt(float64(len(xs)))
	return Interval{m, m - 1.96*se, m + 1.96*se}
}

// Effect of one visibility setting against the closed control, paired by replication.
type Effect struct {
	Design     string              `json:"design"`
	ExposureBP int                 `json:"exposure_bp"`
	GMVLift    Interval            `json:"gmv_lift"`
	STRpp      map[string]Interval `json:"str_change_pp"` // per seller market, percentage points
	CrossShare Interval            `json:"cross_border_share_of_gmv"`
}

// DesignScore is how one experiment design's estimate of the full-connection GMV lift compares with the truth.
type DesignScore struct {
	Design    string       `json:"design"`
	Unit      string       `json:"unit"`
	Bias      float64      `json:"bias"` // mean of estimate minus that replication's true lift
	RMSE      float64      `json:"rmse"`
	Coverage  float64      `json:"coverage"`   // share of replications whose interval holds their true lift
	Width     float64      `json:"mean_width"` // mean interval width
	Estimates []EstimateVs `json:"estimates"`
}

type EstimateVs struct {
	Estimate
	Truth float64 `json:"truth"`
}

// Report is everything the study writes.
type Report struct {
	World       string        `json:"world"`
	Seeds       int           `json:"seeds"`
	Markets     []string      `json:"markets"`
	Buyers      int           `json:"buyers"`
	ListingsDay int           `json:"listings_per_day"`
	TestDays    int           `json:"test_days"`
	ControlGMV  Interval      `json:"control_gmv_eur"`
	ControlSTR  []Interval    `json:"control_str"`
	Truth       Effect        `json:"truth"`
	Ramp        []Effect      `json:"ramp"`
	Capped      Effect        `json:"capped"`
	Designs     []DesignScore `json:"designs"`
	Guardrail   Guardrail     `json:"guardrail"`
}

// Guardrail picks the largest exposure whose domestic sell-through drop stays within the limit for every market.
type Guardrail struct {
	MaxDropPP   float64 `json:"max_domestic_str_drop_pp"`
	ExposureBP  int     `json:"recommended_exposure_bp"`
	GMVLift     float64 `json:"gmv_lift_at_recommendation"`
	WorstDropPP float64 `json:"worst_str_change_pp_at_recommendation"`
}

func effect(name string, bp int, runs, control []Run, codes []string) Effect {
	e := Effect{Design: name, ExposureBP: bp, STRpp: map[string]Interval{}}
	lift := make([]float64, len(runs))
	share := make([]float64, len(runs))
	for s := range runs {
		lift[s] = runs[s].GMV/control[s].GMV - 1
		share[s] = runs[s].CrossGMV / math.Max(runs[s].GMV, 1)
	}
	e.GMVLift = summarize(lift)
	e.CrossShare = summarize(share)
	for m, code := range codes {
		d := make([]float64, len(runs))
		for s := range runs {
			d[s] = 100 * (runs[s].STR[m] - control[s].STR[m])
		}
		e.STRpp[code] = summarize(d)
	}
	return e
}

// Summarize scores the designs and the ramp from the simulated runs.
func Summarize(cfg market.Config, lab Lab, runs map[string][]Run) Report {
	var codes []string
	buyers, listings := 0, 0
	for _, m := range cfg.Markets {
		codes = append(codes, m.Code)
		buyers += m.Buyers
		listings += m.ListingsPerDay
	}
	control := runs["control"]
	rep := Report{World: cfg.Name, Seeds: lab.Seeds, Markets: codes, Buyers: buyers, ListingsDay: listings, TestDays: cfg.TestDays}
	gmv := make([]float64, len(control))
	for s, r := range control {
		gmv[s] = r.GMV
	}
	rep.ControlGMV = summarize(gmv)
	for m := range codes {
		v := make([]float64, len(control))
		for s, r := range control {
			v[s] = r.STR[m]
		}
		rep.ControlSTR = append(rep.ControlSTR, summarize(v))
	}
	rep.Truth = effect("open", 10_000, runs["open"], control, codes)
	rep.Capped = effect("open_capped", 10_000, runs["open_capped"], control, codes)
	for _, bp := range lab.RampBP {
		rep.Ramp = append(rep.Ramp, effect(rampName(bp), bp, runs[rampName(bp)], control, codes))
	}

	truth := make([]float64, lab.Seeds)
	for s := range truth {
		truth[s] = runs["open"][s].GMV/control[s].GMV - 1
	}
	score := func(name, unit string, est func(s int) Estimate) DesignScore {
		d := DesignScore{Design: name, Unit: unit}
		var sq, bias, width float64
		hits := 0
		for s := 0; s < lab.Seeds; s++ {
			e := est(s)
			d.Estimates = append(d.Estimates, EstimateVs{e, truth[s]})
			bias += e.Value - truth[s]
			sq += (e.Value - truth[s]) * (e.Value - truth[s])
			width += e.Hi - e.Lo
			if e.Lo <= truth[s] && truth[s] <= e.Hi {
				hits++
			}
		}
		n := float64(lab.Seeds)
		d.Bias, d.RMSE, d.Coverage, d.Width = bias/n, math.Sqrt(sq/n), float64(hits)/n, width/n
		return d
	}
	split := rampName(lab.ListingSplitBP)
	rep.Designs = []DesignScore{
		score("buyer_split", "buyers, 50/50", func(s int) Estimate { return BuyerSplit(runs["buyer_split"][s], lab, uint64(s)) }),
		score("listing_split", "listings, 50/50 (a 50% ramp)", func(s int) Estimate { return ListingSplit(runs[split][s], lab, uint64(s)) }),
		score("switchback", "days, blocks of "+itoa(lab.SwitchbackBlockDays), func(s int) Estimate { return Switchback(runs["switchback"][s], lab, uint64(s)) }),
	}

	rep.Guardrail = Guardrail{MaxDropPP: lab.Guardrail.MaxDomesticSTRDropPP}
	for _, e := range rep.Ramp {
		worst := math.Inf(1)
		for _, iv := range e.STRpp {
			worst = math.Min(worst, iv.Mean)
		}
		if -worst <= lab.Guardrail.MaxDomesticSTRDropPP && e.ExposureBP > rep.Guardrail.ExposureBP {
			rep.Guardrail.ExposureBP = e.ExposureBP
			rep.Guardrail.GMVLift = e.GMVLift.Mean
			rep.Guardrail.WorstDropPP = worst
		}
	}
	return rep
}
