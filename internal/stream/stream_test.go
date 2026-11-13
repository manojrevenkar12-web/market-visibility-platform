package stream

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestKeysKeepTheirOrderAndLagCounts(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(8)
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("listing-%d", i%37)
		_ = m.Publish(ctx, "t", []byte(key), []byte(fmt.Sprint(i)))
	}
	if lag, _ := m.Lag("g", "t"); lag != 1000 {
		t.Fatalf("lag %d", lag)
	}
	last := map[string]int{}
	total := 0
	for {
		recs, _ := m.Poll(ctx, "g", "t", 64, 0)
		if len(recs) == 0 {
			break
		}
		for _, r := range recs {
			var v int
			fmt.Sscan(string(r.Value), &v)
			if prev, ok := last[string(r.Key)]; ok && v < prev {
				t.Fatalf("key %s went back from %d to %d", r.Key, prev, v)
			}
			last[string(r.Key)] = v
		}
		total += len(recs)
		_ = m.Commit(ctx, "g", recs)
	}
	if total != 1000 {
		t.Fatalf("delivered %d", total)
	}
	if lag, _ := m.Lag("g", "t"); lag != 0 {
		t.Fatalf("lag %d after commit", lag)
	}
	if lag, _ := m.Lag("other", "t"); lag != 1000 {
		t.Fatal("groups are independent")
	}
}

func TestPollWaitsForAPublish(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(2)
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = m.Publish(ctx, "t", []byte("k"), []byte("v"))
	}()
	start := time.Now()
	recs, _ := m.Poll(ctx, "g", "t", 10, 2*time.Second)
	if len(recs) != 1 || time.Since(start) > time.Second {
		t.Fatalf("got %d records after %s", len(recs), time.Since(start))
	}
	recs, _ = m.Poll(ctx, "g", "t", 10, 20*time.Millisecond)
	if len(recs) != 0 {
		t.Fatal("nothing more to deliver")
	}
}
