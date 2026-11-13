package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/index"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/obs"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/visibility"
)

func brokers(t *testing.T) string {
	b := os.Getenv("CVP_KAFKA_BROKERS")
	if b == "" {
		t.Skip("CVP_KAFKA_BROKERS not set")
	}
	return b
}

func TestKeyOrderCommitAndLag(t *testing.T) {
	l, err := New(brokers(t))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	topic := fmt.Sprintf("contract-%d", time.Now().UnixNano())
	for i := 0; i < 600; i++ {
		if err := l.Publish(ctx, topic, []byte(fmt.Sprintf("k%d", i%13)), []byte(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	last := map[string]int{}
	got := 0
	deadline := time.Now().Add(60 * time.Second)
	for got < 600 && time.Now().Before(deadline) {
		recs, err := l.Poll(ctx, "g", topic, 100, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			v, _ := strconv.Atoi(string(r.Value))
			if prev, ok := last[string(r.Key)]; ok && v < prev {
				t.Fatalf("key %s went back from %d to %d", r.Key, prev, v)
			}
			last[string(r.Key)] = v
		}
		got += len(recs)
		if err := l.Commit(ctx, "g", recs); err != nil {
			t.Fatal(err)
		}
	}
	if got != 600 {
		t.Fatalf("consumed %d of 600", got)
	}
	if lag, err := l.Lag("g", topic); err != nil || lag != 0 {
		t.Fatalf("lag %d err %v", lag, err)
	}
}

// The visibility pipeline on Kafka: the same assertions as on the in-process log.
func TestPipelineOnKafka(t *testing.T) {
	l, err := New(brokers(t))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	p0 := &policy.Policy{Markets: []string{"FR", "DE"}}
	if err := p0.Validate(); err != nil {
		t.Fatal(err)
	}
	idx := index.NewMemory()
	pipe := &visibility.Pipeline{Log: l, Mat: visibility.NewMaterializer(p0), Index: idx, Metrics: visibility.NewMetrics(obs.NewRegistry())}
	for i := uint64(1); i <= 300; i++ {
		ev := visibility.ListingEvent{Listing: policy.Listing{ID: i, Seller: []string{"FR", "DE"}[i%2], Category: "bags", Active: true}, Title: "bag"}
		b, _ := json.Marshal(ev)
		if err := l.Publish(ctx, visibility.TopicListings, []byte(strconv.FormatUint(i, 10)), b); err != nil {
			t.Fatal(err)
		}
	}
	next := policy.Policy{Version: 1, Markets: []string{"FR", "DE"}, Rules: []policy.Rule{{Buyer: "DE", Seller: "FR", State: policy.Open, ExposureBP: policy.Buckets, Channels: policy.Channels}}}
	b, _ := json.Marshal(visibility.PolicyEvent{Policy: next, At: time.Now()})
	deadline := time.Now().Add(60 * time.Second)
	published := false
	for time.Now().Before(deadline) {
		if _, err := pipe.Step(ctx, 500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if !published && idx.Len() == 300 {
			if err := l.Publish(ctx, visibility.TopicPolicies, []byte("policy"), b); err != nil {
				t.Fatal(err)
			}
			published = true
		}
		if pipe.Mat.Policy().Version == 1 {
			break
		}
	}
	hits, _ := idx.Search(ctx, index.Query{Market: "DE", Channel: policy.Search, Text: "bag", Limit: 1000})
	if len(hits) != 300 {
		t.Fatalf("DE sees %d of 300 listings after opening FR", len(hits))
	}
}
