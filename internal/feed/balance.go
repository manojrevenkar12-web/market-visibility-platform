// Package feed balances domestic and cross-border listings in a ranked channel. Ranking decides the order inside
// each source; the balancer decides how many cross-border items appear and where, so a newly connected market
// gets a guaranteed share of exposure without flooding the buyer's first screen.
package feed

import "errors"

// Item is a ranked candidate.
type Item struct {
	ID          uint64  `json:"id"`
	Score       float64 `json:"score"`
	CrossBorder bool    `json:"cross_border"`
}

// Rules bound the cross-border share in every window of consecutive positions.
type Rules struct {
	Window int `json:"window"` // positions per window, e.g. 10
	Min    int `json:"min"`    // at least this many cross-border items per full window, when there are any left
	Max    int `json:"max"`    // at most this many cross-border items per window
}

func (r Rules) Validate() error {
	if r.Window <= 0 || r.Min < 0 || r.Max < r.Min || r.Max > r.Window {
		return errors.New("need 0 <= min <= max <= window and window > 0")
	}
	return nil
}

// Merge interleaves two lists, each already sorted by score, into k positions. At each position it takes the
// higher-scored head unless that would break the window bounds. Within each source the ranking order is kept.
func Merge(domestic, cross []Item, k int, r Rules) []Item {
	out := make([]Item, 0, k)
	i, j := 0, 0
	for len(out) < k && (i < len(domestic) || j < len(cross)) {
		pos := len(out)
		start := max(0, pos-r.Window+1)
		inWindow := 0 // cross-border items among the positions that share a window with this one
		for _, it := range out[start:] {
			if it.CrossBorder {
				inWindow++
			}
		}
		left := r.Window - (pos - start) // positions left in the current window, this one included
		mustCross := j < len(cross) && inWindow+left <= r.Min
		mayCross := j < len(cross) && inWindow < r.Max
		takeCross := false
		switch {
		case mustCross:
			takeCross = true
		case !mayCross:
			takeCross = false
		case i >= len(domestic):
			takeCross = true
		default:
			takeCross = cross[j].Score > domestic[i].Score
		}
		if takeCross {
			out = append(out, cross[j])
			j++
		} else if i < len(domestic) {
			out = append(out, domestic[i])
			i++
		} else {
			break // only cross-border items are left and the window is full of them
		}
	}
	return out
}
