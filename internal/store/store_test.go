package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/visibility"
)

// Integration tests: CVP_POSTGRES_DSN and CVP_REDIS_ADDR point at real servers (CI starts both as services).

func pg(t *testing.T) *Policies {
	dsn := os.Getenv("CVP_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CVP_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	s, err := OpenPolicies(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `TRUNCATE policies`); err != nil {
		t.Fatal(err)
	}
	return s
}

func base() policy.Policy {
	return policy.Policy{Markets: []string{"FR", "DE"}, Rules: []policy.Rule{{
		Buyer: "DE", Seller: "FR", State: policy.Ramping, ExposureBP: 1000, Salt: "fr-de", Channels: policy.Channels,
	}}}
}

func TestPoliciesAreVersionedAndActivationIsRecorded(t *testing.T) {
	s := pg(t)
	ctx := context.Background()
	if _, err := s.Active(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no active policy yet, got %v", err)
	}
	a, err := s.Create(ctx, base(), "ds-anna")
	if err != nil || a.Version != 1 {
		t.Fatalf("v1: %+v %v", a, err)
	}
	next := base()
	next.Rules[0].ExposureBP = 2500
	b, _ := s.Create(ctx, next, "ds-anna")
	if b.Version != 2 {
		t.Fatalf("v2 got %d", b.Version)
	}
	now := time.Now()
	_ = s.Activate(ctx, 2, "ds-anna", now)
	_ = s.Activate(ctx, 1, "oncall", now.Add(time.Second)) // rollback
	act, _ := s.Active(ctx)
	if act.Version != 1 || act.Rules[0].ExposureBP != 1000 {
		t.Fatalf("rollback to v1 expected, got %+v", act)
	}
	if err := s.Activate(ctx, 99, "x", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown version")
	}
	bad := base()
	bad.Rules[0].Salt = ""
	if _, err := s.Create(ctx, bad, "x"); err == nil {
		t.Fatal("invalid policies are never stored")
	}
}

func TestConcurrentCreatesGetDistinctVersions(t *testing.T) {
	s := pg(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	versions := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.Create(ctx, base(), "x")
			if err != nil {
				t.Error(err)
				return
			}
			versions <- p.Version
		}()
	}
	wg.Wait()
	close(versions)
	seen := map[int]bool{}
	for v := range versions {
		if seen[v] {
			t.Fatalf("version %d issued twice", v)
		}
		seen[v] = true
	}
	if len(seen) != 20 {
		t.Fatalf("%d versions", len(seen))
	}
}

func TestRedisMasksRoundTrip(t *testing.T) {
	addr := os.Getenv("CVP_REDIS_ADDR")
	if addr == "" {
		t.Skip("CVP_REDIS_ADDR not set")
	}
	ctx := context.Background()
	r, err := DialRedis(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.TTL = time.Hour
	base := uint64(time.Now().UnixNano()) // keys of this run only
	in := map[uint64]visibility.Masks{base + 1: {1, 2, 3}, base + 2: {0xffffffffffffffff, 0, 5}}
	if err := r.SetMasks(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := r.GetMasks(ctx, []uint64{base + 1, base + 2, base + 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[base+1] != in[base+1] || out[base+2] != in[base+2] {
		t.Fatalf("got %v", out)
	}
	if _, err := r.Pipeline(ctx, [][]string{{"NOSUCHCOMMAND"}}); err == nil {
		t.Fatal("error replies surface as errors")
	}
}
