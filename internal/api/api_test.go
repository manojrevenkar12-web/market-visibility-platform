package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/feed"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/index"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/obs"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/stream"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/visibility"
)

var markets = []string{"FR", "DE"}

func setup(t *testing.T) (*httptest.Server, *visibility.Pipeline) {
	t.Helper()
	p0 := &policy.Policy{Version: 0, Markets: markets}
	if err := p0.Validate(); err != nil {
		t.Fatal(err)
	}
	reg := obs.NewRegistry()
	log := stream.NewMemory(4)
	idx := index.NewMemory()
	mat := visibility.NewMaterializer(p0)
	pipe := &visibility.Pipeline{Log: log, Mat: mat, Index: idx, Metrics: visibility.NewMetrics(reg)}
	srv := &Server{Log: log, Mat: mat, Index: idx, Registry: reg, FeedRules: map[string]feed.Rules{"DE": {Window: 4, Min: 1, Max: 2}}}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	ctx := context.Background()
	for i := uint64(1); i <= 400; i++ {
		seller := markets[i%2]
		b, _ := json.Marshal(visibility.ListingEvent{Listing: policy.Listing{ID: i, Seller: seller, Category: "bags", Active: true}, Title: "vintage bag", CreatedAt: time.Now()})
		_ = log.Publish(ctx, visibility.TopicListings, []byte{byte(i)}, b)
	}
	for {
		n, err := pipe.Step(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	return ts, pipe
}

const rampDE = `{"markets":["FR","DE"],"rules":[{"buyer":"DE","seller":"FR","state":"ramping","exposure_bp":5000,"salt":"fr-de","channels":["search","feed","similar"]}]}`

func post(t *testing.T, url, body string, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestPreviewCountsWithoutActivating(t *testing.T) {
	ts, pipe := setup(t)
	resp, out := post(t, ts.URL+"/v1/policies/preview", rampDE, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %v", resp.StatusCode, out)
	}
	gained := out["total"].(map[string]any)["gained"].(float64)
	if gained < 70 || gained > 130 { // half of 200 FR listings
		t.Fatalf("gained %v", gained)
	}
	if pipe.Mat.Policy().Version != 0 {
		t.Fatal("preview must not activate")
	}
	resp, _ = post(t, ts.URL+"/v1/policies/preview", `{"markets":["FR","DE"],"rules":[{"buyer":"DE","seller":"DE","state":"open","exposure_bp":10000}]}`, nil)
	if resp.StatusCode != 422 {
		t.Fatalf("invalid policy: %d", resp.StatusCode)
	}
	resp, _ = post(t, ts.URL+"/v1/policies/preview", `{"markets":["FR","DE"],"surprise":1}`, nil)
	if resp.StatusCode != 400 {
		t.Fatal("unknown fields are refused")
	}
}

func TestActivateNeedsAnActorThenFlowsToSearch(t *testing.T) {
	ts, pipe := setup(t)
	resp, _ := post(t, ts.URL+"/v1/policies/activate", rampDE, nil)
	if resp.StatusCode != 400 {
		t.Fatal("activation without an actor must be refused")
	}
	resp, out := post(t, ts.URL+"/v1/policies/activate", rampDE, map[string]string{"X-Actor": "ds-anna"})
	if resp.StatusCode != 202 || out["version"].(float64) != 1 {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if _, err := pipe.Step(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	r, _ := http.Get(ts.URL + "/v1/search?market=DE&q=bag&limit=1000")
	var res struct{ Hits []index.Hit }
	_ = json.NewDecoder(r.Body).Decode(&res)
	fr := 0
	for _, h := range res.Hits {
		if h.Seller == "FR" {
			fr++
		}
	}
	if fr < 70 || fr > 130 || len(res.Hits)-fr != 200 {
		t.Fatalf("DE search shows %d FR and %d DE listings", fr, len(res.Hits)-fr)
	}
	vr, _ := http.Get(ts.URL + "/v1/listings/2/visibility")
	if vr.StatusCode != 200 {
		t.Fatal("visibility endpoint")
	}
	m, _ := http.Get(ts.URL + "/metrics")
	var b bytes.Buffer
	_, _ = b.ReadFrom(m.Body)
	if !strings.Contains(b.String(), `cvp_policy_version 1`) || !strings.Contains(b.String(), `cvp_http_request_seconds_bucket{route="activate"`) {
		t.Fatal("metrics missing")
	}
}

func TestFeedDropsHiddenCandidatesAndBalances(t *testing.T) {
	ts, pipe := setup(t)
	_, _ = post(t, ts.URL+"/v1/policies/activate", strings.Replace(rampDE, `"ramping","exposure_bp":5000`, `"open","exposure_bp":10000`, 1), map[string]string{"X-Actor": "x"})
	_, _ = pipe.Step(context.Background(), 0)
	var cands []feed.Item
	for i := uint64(1); i <= 40; i++ {
		cands = append(cands, feed.Item{ID: i, Score: 1 - float64(i)/100})
	}
	cands = append(cands, feed.Item{ID: 999_999, Score: 2}) // unknown to the platform: never shown
	body, _ := json.Marshal(FeedRequest{Market: "DE", K: 12, Candidates: cands})
	resp, out := post(t, ts.URL+"/v1/feed", string(body), nil)
	if resp.StatusCode != 200 || out["dropped"].(float64) != 1 {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	items := out["items"].([]any)
	if len(items) != 12 {
		t.Fatalf("%d items", len(items))
	}
	for s := 0; s+4 <= len(items); s++ {
		c := 0
		for _, it := range items[s : s+4] {
			if it.(map[string]any)["cross_border"].(bool) {
				c++
			}
		}
		if c < 1 || c > 2 {
			t.Fatalf("window at %d has %d cross-border items", s, c)
		}
	}
}
