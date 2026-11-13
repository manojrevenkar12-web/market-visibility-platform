package index

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
)

func TestMemorySearchFiltersByMarketAndChannel(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	now := time.Now()
	_ = m.Upsert(ctx, []Doc{
		{ID: 1, Title: "Red leather jacket", Seller: "FR", CreatedAt: now, Visible: map[policy.Channel][]string{policy.Search: {"FR", "DE"}}},
		{ID: 2, Title: "Blue jacket", Seller: "FR", CreatedAt: now.Add(time.Minute), Visible: map[policy.Channel][]string{policy.Search: {"FR"}, policy.Feed: {"DE"}}},
		{ID: 3, Title: "Leather boots", Seller: "DE", CreatedAt: now, Visible: map[policy.Channel][]string{policy.Search: {"DE"}}},
	})
	hits, _ := m.Search(ctx, Query{Market: "DE", Channel: policy.Search, Text: "leather jacket"})
	if len(hits) != 2 || hits[0].ID != 1 || hits[1].ID != 3 {
		t.Fatalf("got %+v", hits)
	}
	_ = m.SetVisibility(ctx, 2, map[policy.Channel][]string{policy.Search: {"DE"}})
	hits, _ = m.Search(ctx, Query{Market: "DE", Channel: policy.Search, Text: "blue"})
	if len(hits) != 1 {
		t.Fatal("partial update not visible")
	}
}

func TestYQLAlwaysFiltersOnTheMarketAndEscapes(t *testing.T) {
	q := YQL(Query{Market: "DE", Channel: policy.Feed, Text: `jacket "x" or true`, Category: "bags"})
	if !strings.HasPrefix(q, `select listing_id, seller, title from listing where visible_feed contains "DE"`) {
		t.Fatal(q)
	}
	if !strings.Contains(q, `title contains "\"x\""`) || !strings.Contains(q, `category contains "bags"`) {
		t.Fatal(q)
	}
}

func TestVespaRequests(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		bodies = append(bodies, body)
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/search/") {
			if !strings.Contains(r.URL.Query().Get("yql"), `visible_search contains "DE"`) {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"root":{"children":[{"relevance":2,"fields":{"listing_id":7,"seller":"FR","title":"jacket"}}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	v := NewVespa(srv.URL)
	if err := v.Upsert(ctx, []Doc{{ID: 7, Title: "jacket", Seller: "FR", Visible: map[policy.Channel][]string{policy.Search: {"DE"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := v.SetVisibility(ctx, 7, map[policy.Channel][]string{policy.Search: {"DE", "FR"}}); err != nil {
		t.Fatal(err)
	}
	hits, err := v.Search(ctx, Query{Market: "DE", Channel: policy.Search, Text: "jacket"})
	if err != nil || len(hits) != 1 || hits[0].ID != 7 {
		t.Fatalf("hits %+v err %v", hits, err)
	}
	want := []string{"POST /document/v1/marketplace/listing/docid/7", "PUT /document/v1/marketplace/listing/docid/7", "GET /search/"}
	for i, w := range want {
		if calls[i] != w {
			t.Fatalf("call %d: %s, want %s", i, calls[i], w)
		}
	}
	upd := bodies[1]["fields"].(map[string]any)["visible_search"].(map[string]any)["assign"].([]any)
	if len(upd) != 2 {
		t.Fatalf("partial update body %v", bodies[1])
	}
	if _, ok := bodies[1]["fields"].(map[string]any)["title"]; ok {
		t.Fatal("a visibility change must not resend the document")
	}
}

// Against a real Vespa (CI deploys deploy/vespa into a container and sets CVP_VESPA_URL): the same contract as
// Memory, including that a partial update changes what a market finds.
func TestVespaContract(t *testing.T) {
	url := os.Getenv("CVP_VESPA_URL")
	if url == "" {
		t.Skip("CVP_VESPA_URL not set")
	}
	ctx := context.Background()
	v := NewVespa(url)
	docs := []Doc{
		{ID: 101, Title: "vintage leather jacket", Category: "women", Seller: "FR", CreatedAt: time.Now(), Visible: map[policy.Channel][]string{policy.Search: {"FR"}}},
		{ID: 102, Title: "leather boots", Category: "women", Seller: "DE", CreatedAt: time.Now(), Visible: map[policy.Channel][]string{policy.Search: {"DE", "FR"}}},
	}
	if err := v.Upsert(ctx, docs); err != nil {
		t.Fatal(err)
	}
	ids := func(market string) map[uint64]bool {
		hits, err := v.Search(ctx, Query{Market: market, Channel: policy.Search, Text: "leather", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out := map[uint64]bool{}
		for _, h := range hits {
			out[h.ID] = true
		}
		return out
	}
	if got := ids("DE"); got[101] || !got[102] {
		t.Fatalf("DE before: %v", got)
	}
	if err := v.SetVisibility(ctx, 101, map[policy.Channel][]string{policy.Search: {"FR", "DE"}}); err != nil {
		t.Fatal(err)
	}
	if got := ids("DE"); !got[101] {
		t.Fatalf("DE after the partial update: %v", got)
	}
	if err := v.Delete(ctx, 101); err != nil {
		t.Fatal(err)
	}
	if got := ids("FR"); got[101] {
		t.Fatal("deleted document still found")
	}
}
