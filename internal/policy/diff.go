package policy

import "slices"

// Change describes how one seller market's visibility moved between two policies. A listing of Seller can change
// visibility only if Whole is set, or if its bucket under Salt lies in [Lo, Hi).
type Change struct {
	Buyer  string
	Seller string
	Whole  bool
	Salt   string
	Lo, Hi int
}

// Diff lists the (buyer, seller) pairs whose visibility can differ between old and next, and for a pure ramp
// move, the band of buckets that changes. Anything else (channels, exclusions, weight limit, salt) marks the whole
// pair, because then any listing of the seller market may change.
func Diff(old, next *Policy) []Change {
	var out []Change
	seen := map[[2]string]bool{}
	visit := func(buyer, seller string) {
		key := [2]string{buyer, seller}
		if seen[key] {
			return
		}
		seen[key] = true
		a, okA := old.Rule(buyer, seller)
		b, okB := next.Rule(buyer, seller)
		if !okA {
			a = Rule{Buyer: buyer, Seller: seller, State: Closed}
		}
		if !okB {
			b = Rule{Buyer: buyer, Seller: seller, State: Closed}
		}
		band := func(lo, hi int, salt string) {
			out = append(out, Change{Buyer: buyer, Seller: seller, Salt: salt, Lo: lo, Hi: hi})
		}
		switch {
		case a.State == Closed && b.State == Closed, equalRules(a, b):
			// nothing visible changes (a closed rule's other fields do not matter)
		case a.State == Closed:
			band(0, b.ExposureBP, b.Salt)
		case b.State == Closed:
			band(0, a.ExposureBP, a.Salt)
		case sameShape(a, b):
			salt := b.Salt
			if a.State == Ramping {
				salt = a.Salt
			}
			band(min(a.ExposureBP, b.ExposureBP), max(a.ExposureBP, b.ExposureBP), salt)
		default:
			out = append(out, Change{Buyer: buyer, Seller: seller, Whole: true})
		}
	}
	for _, r := range old.Rules {
		visit(r.Buyer, r.Seller)
	}
	for _, r := range next.Rules {
		visit(r.Buyer, r.Seller)
	}
	return out
}

// Affects reports whether a listing's visibility may change under this change.
func (c Change) Affects(l Listing) bool {
	if l.Seller != c.Seller {
		return false
	}
	if c.Whole {
		return true
	}
	b := Bucket(l.ID, c.Salt)
	return b >= c.Lo && b < c.Hi
}

func equalRules(a, b Rule) bool {
	return a.State == b.State && a.ExposureBP == b.ExposureBP && a.Salt == b.Salt && sameShape(a, b)
}

// sameShape: everything but the exposure agrees, and both rules bucket the same way, so only a band moves.
// Open and closed rules bucket like a ramp at 10000 and 0, whatever their salt.
func sameShape(a, b Rule) bool {
	if !slices.Equal(sorted(a.Channels), sorted(b.Channels)) ||
		!slices.Equal(sortedS(a.ExcludedCategories), sortedS(b.ExcludedCategories)) ||
		a.MaxWeightGrams != b.MaxWeightGrams {
		return false
	}
	saltMatters := a.State == Ramping && b.State == Ramping
	return !saltMatters || a.Salt == b.Salt
}

func sorted(c []Channel) []Channel {
	out := slices.Clone(c)
	slices.Sort(out)
	return out
}

func sortedS(c []string) []string {
	out := slices.Clone(c)
	slices.Sort(out)
	return out
}
