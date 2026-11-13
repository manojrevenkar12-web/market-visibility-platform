package visibility

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/index"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/obs"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/stream"
)

// Topics the pipeline reads and writes.
const (
	TopicListings = "listings.v1"
	TopicPolicies = "visibility-policies.v1"
	TopicChanges  = "visibility-changes.v1"
	Group         = "visibility-materializer"
)

// ListingEvent is a listing created, edited, sold or removed.
type ListingEvent struct {
	Listing    policy.Listing `json:"listing"`
	Title      string         `json:"title"`
	PriceCents int64          `json:"price_cents"`
	CreatedAt  time.Time      `json:"created_at"`
	At         time.Time      `json:"at"` // when it happened, for the visibility lag
}

// PolicyEvent activates a policy.
type PolicyEvent struct {
	Policy policy.Policy `json:"policy"`
	At     time.Time     `json:"at"`
}

// ChangeEvent tells downstream channels (feed caches, recommendation candidate stores) that a listing's
// visibility moved.
type ChangeEvent struct {
	ID      uint64                      `json:"id"`
	Version int                         `json:"policy_version"`
	Visible map[policy.Channel][]string `json:"visible"`
}

// Cache is a fast store of masks for request-time checks (Redis in production).
type Cache interface {
	SetMasks(ctx context.Context, masks map[uint64]Masks) error
}

// Metrics the pipeline reports.
type Metrics struct {
	ListingLag      *obs.Histogram
	PolicyApply     *obs.Histogram
	Events          *obs.Counter
	IndexWrites     *obs.Counter
	PolicyVersion   *obs.Counter
	ActivationStats *obs.Counter
}

func NewMetrics(r *obs.Registry) *Metrics {
	return &Metrics{
		ListingLag:      r.Histogram("cvp_listing_visibility_lag_seconds", "From a listing event to its visibility in the index.", obs.LatencyBuckets),
		PolicyApply:     r.Histogram("cvp_policy_apply_seconds", "From a policy activation to the last index write it caused.", obs.LatencyBuckets),
		Events:          r.Counter("cvp_events_total", "Events processed.", "topic"),
		IndexWrites:     r.Counter("cvp_index_writes_total", "Index writes.", "kind"),
		PolicyVersion:   r.Gauge("cvp_policy_version", "Active policy version."),
		ActivationStats: r.Counter("cvp_policy_activation_listings_total", "Listings scanned, recomputed and changed by policy activations.", "stage"),
	}
}

// Pipeline consumes listing and policy events and keeps the index and cache in line with the policy.
type Pipeline struct {
	Log     stream.Log
	Mat     *Materializer
	Index   index.Index
	Cache   Cache
	Metrics *Metrics
	Now     func() time.Time
	Batch   int
}

// Step processes what is waiting on both topics and commits it. It returns the number of records processed.
func (p *Pipeline) Step(ctx context.Context, wait time.Duration) (int, error) {
	n := 0
	pol, err := p.Log.Poll(ctx, Group, TopicPolicies, p.batch(), 0)
	if err != nil {
		return 0, err
	}
	for _, r := range pol {
		if err := p.policy(ctx, r); err != nil {
			return n, err
		}
		n++
	}
	if err := p.Log.Commit(ctx, Group, pol); err != nil {
		return n, err
	}
	recs, err := p.Log.Poll(ctx, Group, TopicListings, p.batch(), wait)
	if err != nil {
		return n, err
	}
	cache := map[uint64]Masks{}
	for _, r := range recs {
		if err := p.listing(ctx, r, cache); err != nil {
			return n, err
		}
		n++
	}
	if p.Cache != nil && len(cache) > 0 {
		if err := p.Cache.SetMasks(ctx, cache); err != nil {
			return n, err
		}
	}
	// commit only after the index and cache have the batch: a crash replays it, and replays are no-ops
	return n, p.Log.Commit(ctx, Group, recs)
}

// Run steps until the context ends.
func (p *Pipeline) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if _, err := p.Step(ctx, 200*time.Millisecond); err != nil && ctx.Err() == nil {
			return err
		}
	}
	return nil
}

func (p *Pipeline) batch() int {
	if p.Batch <= 0 {
		return 500
	}
	return p.Batch
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pipeline) listing(ctx context.Context, r stream.Record, cache map[uint64]Masks) error {
	var ev ListingEvent
	if err := json.Unmarshal(r.Value, &ev); err != nil {
		return fmt.Errorf("listing event at %s/%d/%d: %w", r.Topic, r.Partition, r.Offset, err)
	}
	masks, _ := p.Mat.Upsert(ev.Listing)
	if !ev.Listing.Active {
		if err := p.Index.Delete(ctx, ev.Listing.ID); err != nil {
			return err
		}
		p.Metrics.IndexWrites.Add(1, "delete")
	} else {
		doc := index.Doc{
			ID: ev.Listing.ID, Title: ev.Title, Category: ev.Listing.Category, Seller: ev.Listing.Seller,
			PriceCents: ev.PriceCents, CreatedAt: ev.CreatedAt, Visible: Expand(p.Mat.Policy(), masks),
		}
		if err := p.Index.Upsert(ctx, []index.Doc{doc}); err != nil {
			return err
		}
		p.Metrics.IndexWrites.Add(1, "document")
	}
	cache[ev.Listing.ID] = masks
	p.Metrics.Events.Add(1, TopicListings)
	if !ev.At.IsZero() {
		p.Metrics.ListingLag.Observe(p.now().Sub(ev.At).Seconds())
	}
	return nil
}

func (p *Pipeline) policy(ctx context.Context, r stream.Record) error {
	var ev PolicyEvent
	if err := json.Unmarshal(r.Value, &ev); err != nil {
		return fmt.Errorf("policy event: %w", err)
	}
	next := ev.Policy
	if err := next.Validate(); err != nil {
		// an invalid policy is refused, not applied halfway, and must not block the topic behind it;
		// the API validates before publishing, so this is a producer bug worth alerting on
		p.Metrics.Events.Add(1, "refused_policy")
		return nil
	}
	if next.Version <= p.Mat.Policy().Version {
		return nil // replay of an older activation
	}
	changes, stats := p.Mat.Activate(&next)
	cache := make(map[uint64]Masks, len(changes))
	for _, c := range changes {
		vis := Expand(&next, c.Masks)
		if err := p.Index.SetVisibility(ctx, c.ID, vis); err != nil {
			return err
		}
		cache[c.ID] = c.Masks
		b, _ := json.Marshal(ChangeEvent{ID: c.ID, Version: next.Version, Visible: vis})
		if err := p.Log.Publish(ctx, TopicChanges, []byte(strconv.FormatUint(c.ID, 10)), b); err != nil {
			return err
		}
	}
	if p.Cache != nil && len(cache) > 0 {
		if err := p.Cache.SetMasks(ctx, cache); err != nil {
			return err
		}
	}
	p.Metrics.IndexWrites.Add(float64(len(changes)), "visibility")
	p.Metrics.PolicyVersion.Set(float64(next.Version))
	p.Metrics.ActivationStats.Add(float64(stats.Scanned), "scanned")
	p.Metrics.ActivationStats.Add(float64(stats.Recomputed), "recomputed")
	p.Metrics.ActivationStats.Add(float64(stats.Changed), "changed")
	p.Metrics.Events.Add(1, TopicPolicies)
	if !ev.At.IsZero() {
		p.Metrics.PolicyApply.Observe(p.now().Sub(ev.At).Seconds())
	}
	return nil
}
