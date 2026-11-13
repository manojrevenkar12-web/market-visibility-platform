package visibility

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/index"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/obs"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/stream"
)

var markets = []string{"FR", "DE", "BE", "ES"}

func newPolicy(t *testing.T, version int, rules ...policy.Rule) *policy.Policy {
	t.Helper()
	p := &policy.Policy{Version: version, Markets: markets, Rules: rules}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func rule(b, s string, bp int, salt string) policy.Rule {
	st := policy.Ramping
	switch bp {
	case 0:
		st = policy.Closed
	case policy.Buckets:
		st = policy.Open
	}
	return policy.Rule{Buyer: b, Seller: s, State: st, ExposureBP: bp, Salt: salt, Channels: policy.Channels}
}

func listings(rng *rand.Rand, n int) []policy.Listing {
	cats := []string{"bags", "shoes", "furniture"}
	out := make([]policy.Listing, n)
	for i := range out {
		out[i] = policy.Listing{ID: uint64(i + 1), Seller: markets[rng.IntN(len(markets))], Category: cats[rng.IntN(3)], WeightG: rng.IntN(5000), Active: rng.IntN(10) > 0}
	}
	return out
}

// After any sequence of policy activations, the incremental state equals a full recomputation.
func TestIncrementalEqualsFullRecompute(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	m := NewMaterializer(newPolicy(t, 0))
	for _, l := range listings(rng, 5000) {
		m.Upsert(l)
	}
	for v := 1; v <= 60; v++ {
		var rules []policy.Rule
		for _, b := range markets {
			for _, s := range markets {
				if b == s || rng.IntN(3) == 0 {
					continue
				}
				r := rule(b, s, []int{0, 500, 2000, 6000, policy.Buckets}[rng.IntN(5)], []string{"x", "y"}[rng.IntN(2)])
				if rng.IntN(5) == 0 {
					r.ExcludedCategories = []string{"furniture"}
				}
				if rng.IntN(5) == 0 {
					r.MaxWeightGrams = 2000
				}
				rules = append(rules, r)
			}
		}
		m.Activate(newPolicy(t, v, rules...))
		if bad := m.RecomputeAll(); bad != 0 {
			t.Fatalf("after activation %d, %d listings disagree with a full recompute", v, bad)
		}
	}
}

func TestARampStepTouchesOnlyItsBand(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 1))
	m := NewMaterializer(newPolicy(t, 1, rule("DE", "FR", 1000, "s")))
	for _, l := range listings(rng, 40_000) {
		l.Active = true
		m.Upsert(l)
	}
	changes, stats := m.Activate(newPolicy(t, 2, rule("DE", "FR", 2000, "s")))
	fr := stats.Scanned
	if stats.Recomputed > fr/8 || stats.Recomputed < fr/12 {
		t.Fatalf("a 10-point ramp step should recompute about a tenth of FR listings: %+v", stats)
	}
	if len(changes) != stats.Changed || stats.Changed != stats.Recomputed {
		t.Fatalf("every recomputed listing in the band gains visibility: %+v", stats)
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	m := NewMaterializer(newPolicy(t, 1, rule("DE", "FR", policy.Buckets, "")))
	l := policy.Listing{ID: 9, Seller: "FR", Category: "bags", Active: true}
	if _, changed := m.Upsert(l); !changed {
		t.Fatal("first sight is a change")
	}
	if _, changed := m.Upsert(l); changed {
		t.Fatal("a replay must not change anything")
	}
}

func pipeline(t *testing.T) (*Pipeline, *stream.Memory, *index.Memory, *obs.Registry) {
	log := stream.NewMemory(4)
	idx := index.NewMemory()
	reg := obs.NewRegistry()
	return &Pipeline{Log: log, Mat: NewMaterializer(newPolicy(t, 0)), Index: idx, Metrics: NewMetrics(reg)}, log, idx, reg
}

func publish(t *testing.T, log stream.Log, topic string, key string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := log.Publish(context.Background(), topic, []byte(key), b); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineKeepsSearchInLineWithThePolicy(t *testing.T) {
	ctx := context.Background()
	p, log, idx, reg := pipeline(t)
	rng := rand.New(rand.NewPCG(5, 6))
	all := listings(rng, 3000)
	for _, l := range all {
		publish(t, log, TopicListings, strconv.FormatUint(l.ID, 10), ListingEvent{Listing: l, Title: "jacket " + l.Category, At: time.Now()})
	}
	for {
		n, err := p.Step(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	next := newPolicy(t, 1, rule("DE", "FR", 2500, "fr-de"), rule("FR", "DE", policy.Buckets, ""))
	publish(t, log, TopicPolicies, "policy", PolicyEvent{Policy: *next, At: time.Now()})
	if _, err := p.Step(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// what DE buyers find in search is exactly what the policy says they may see
	hits, _ := idx.Search(ctx, index.Query{Market: "DE", Channel: policy.Search, Text: "jacket"})
	got := map[uint64]bool{}
	for _, h := range hits {
		got[h.ID] = true
	}
	for _, l := range all {
		if want := next.Visible(l, "DE", policy.Search); want != got[l.ID] {
			t.Fatalf("listing %d: policy says %v, search says %v", l.ID, want, got[l.ID])
		}
	}
	if lag, _ := log.Lag(Group, TopicListings); lag != 0 {
		t.Fatalf("lag %d after processing", lag)
	}
	if p.Metrics.IndexWrites.Value("visibility") == 0 || reg == nil {
		t.Fatal("the activation must have written partial updates")
	}
	changes, _ := log.Poll(ctx, "test", TopicChanges, 100_000, 0)
	if float64(len(changes)) != p.Metrics.IndexWrites.Value("visibility") {
		t.Fatal("every visibility write is announced on the changes topic")
	}
}

// crashingLog loses the first commit, as a consumer that dies after writing the index but before committing.
type crashingLog struct {
	*stream.Memory
	crashed bool
}

func (c *crashingLog) Commit(ctx context.Context, group string, records []stream.Record) error {
	if !c.crashed && len(records) > 0 {
		c.crashed = true
		return context.Canceled
	}
	return c.Memory.Commit(ctx, group, records)
}

func TestReplayAfterCrashChangesNothing(t *testing.T) {
	ctx := context.Background()
	p, mem, idx, _ := pipeline(t)
	log := &crashingLog{Memory: mem}
	p.Log = log
	rng := rand.New(rand.NewPCG(9, 9))
	for _, l := range listings(rng, 500) {
		publish(t, log, TopicListings, strconv.FormatUint(l.ID, 10), ListingEvent{Listing: l, Title: "t"})
	}
	if _, err := p.Step(ctx, 0); err == nil {
		t.Fatal("expected the simulated crash")
	}
	before := idx.Len()
	if lag, _ := log.Lag(Group, TopicListings); lag != 500 {
		t.Fatalf("nothing should be committed, lag %d", lag)
	}
	mem.Rewind(Group, TopicListings) // the restarted consumer starts from the last commit
	if n, err := p.Step(ctx, 0); err != nil || n != 500 {
		t.Fatalf("replay processed %d, err %v", n, err)
	}
	if idx.Len() != before || p.Mat.RecomputeAll() != 0 {
		t.Fatal("replay changed state")
	}
	if lag, _ := log.Lag(Group, TopicListings); lag != 0 {
		t.Fatalf("lag %d after the replay", lag)
	}
}

func TestOlderAndInvalidPoliciesAreIgnored(t *testing.T) {
	ctx := context.Background()
	p, log, _, _ := pipeline(t)
	publish(t, log, TopicPolicies, "policy", PolicyEvent{Policy: *newPolicy(t, 3, rule("DE", "FR", policy.Buckets, ""))})
	publish(t, log, TopicPolicies, "policy", PolicyEvent{Policy: *newPolicy(t, 2)})
	bad := policy.Policy{Version: 4, Markets: markets, Rules: []policy.Rule{{Buyer: "DE", Seller: "DE", State: policy.Open, ExposureBP: policy.Buckets}}}
	publish(t, log, TopicPolicies, "policy", PolicyEvent{Policy: bad})
	if _, err := p.Step(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if v := p.Mat.Policy().Version; v != 3 {
		t.Fatalf("active version %d", v)
	}
	if p.Metrics.Events.Value("refused_policy") != 1 {
		t.Fatal("the invalid policy is counted")
	}
}

func TestExpandOrdersMarkets(t *testing.T) {
	p := newPolicy(t, 1)
	got := Expand(p, Masks{0b1010, 0, 0})
	if !slices.Equal(got[policy.Search], []string{"DE", "ES"}) {
		t.Fatalf("got %v", got)
	}
}
