// Package visibility turns the policy into per-listing visibility and keeps it current as listings and policies
// change. The result is written to every channel's store, so search, feed and similar items read one decision.
package visibility

import (
	"sync"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
)

// Masks holds, per channel (in policy.Channels order), the bitmask of buyer markets that see a listing.
type Masks [3]uint64

// Materializer keeps the current listings and their masks under the active policy.
type Materializer struct {
	mu       sync.RWMutex
	policy   *policy.Policy
	listings map[uint64]policy.Listing
	masks    map[uint64]Masks
	bySeller map[string]map[uint64]struct{}
}

func NewMaterializer(p *policy.Policy) *Materializer {
	return &Materializer{
		policy:   p,
		listings: map[uint64]policy.Listing{},
		masks:    map[uint64]Masks{},
		bySeller: map[string]map[uint64]struct{}{},
	}
}

// Policy returns the active policy.
func (m *Materializer) Policy() *policy.Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.policy
}

// Masks returns a listing's masks and whether the listing is known.
func (m *Materializer) Masks(id uint64) (Masks, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.masks[id]
	return v, ok
}

// Listing returns a known listing.
func (m *Materializer) Listing(id uint64) (policy.Listing, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.listings[id]
	return l, ok
}

// Upsert records a listing and returns its masks and whether they changed. Replaying the same listing changes
// nothing, which is what makes at-least-once delivery safe.
func (m *Materializer) Upsert(l policy.Listing) (Masks, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.listings[l.ID]; ok && old.Seller != l.Seller {
		delete(m.bySeller[old.Seller], l.ID)
	}
	m.listings[l.ID] = l
	if m.bySeller[l.Seller] == nil {
		m.bySeller[l.Seller] = map[uint64]struct{}{}
	}
	m.bySeller[l.Seller][l.ID] = struct{}{}
	next := Masks(m.policy.Mask(l))
	prev, known := m.masks[l.ID]
	m.masks[l.ID] = next
	return next, !known || prev != next
}

// Stats describes the work a policy change caused.
type Stats struct {
	Pairs      int `json:"pairs"`      // (buyer, seller) pairs whose rules changed
	Scanned    int `json:"scanned"`    // listings of the affected seller markets looked at
	Recomputed int `json:"recomputed"` // listings whose bucket fell in a changed band
	Changed    int `json:"changed"`    // listings whose visibility actually changed: the index writes
}

// Change is a listing whose visibility moved.
type Change struct {
	ID    uint64
	Masks Masks
}

// Activate switches to the next policy and recomputes only the listings a change can touch.
func (m *Materializer) Activate(next *policy.Policy) ([]Change, Stats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	diff := policy.Diff(m.policy, next)
	m.policy = next
	stats := Stats{Pairs: len(diff)}
	sellers := map[string][]policy.Change{}
	for _, c := range diff {
		sellers[c.Seller] = append(sellers[c.Seller], c)
	}
	var out []Change
	for seller, changes := range sellers {
		for id := range m.bySeller[seller] {
			stats.Scanned++
			l := m.listings[id]
			touched := false
			for _, c := range changes {
				if c.Affects(l) {
					touched = true
					break
				}
			}
			if !touched {
				continue
			}
			stats.Recomputed++
			nm := Masks(next.Mask(l))
			if nm != m.masks[id] {
				m.masks[id] = nm
				out = append(out, Change{ID: id, Masks: nm})
			}
		}
	}
	stats.Changed = len(out)
	return out, stats
}

// RecomputeAll recomputes every listing under the active policy and reports any that disagree with the
// incremental state. Used by tests and the consistency job; it should always return zero mismatches.
func (m *Materializer) RecomputeAll() (mismatches int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, l := range m.listings {
		if Masks(m.policy.Mask(l)) != m.masks[id] {
			mismatches++
		}
	}
	return mismatches
}

// Each calls f for every known listing, under a read lock.
func (m *Materializer) Each(f func(policy.Listing)) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, l := range m.listings {
		f(l)
	}
}

// Size is the number of known listings.
func (m *Materializer) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.listings)
}

// Expand turns masks into market lists per channel.
func Expand(p *policy.Policy, masks Masks) map[policy.Channel][]string {
	out := map[policy.Channel][]string{}
	for i, ch := range policy.Channels {
		out[ch] = p.MarketsOf(masks[i])
	}
	return out
}
