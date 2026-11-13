// Package api is the HTTP interface of the content visibility platform: preview and activate policies, read a
// listing's visibility, search and balance a feed under the active policy, and expose metrics.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/feed"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/index"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/obs"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/stream"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/visibility"
)

// PolicyStore persists policies; nil keeps versions in memory only.
type PolicyStore interface {
	Create(ctx context.Context, p policy.Policy, author string) (policy.Policy, error)
	Activate(ctx context.Context, version int, by string, at time.Time) error
}

type Server struct {
	Log       stream.Log
	Mat       *visibility.Materializer
	Index     index.Index
	Store     PolicyStore
	Registry  *obs.Registry
	FeedRules map[string]feed.Rules // per buyer market
	Now       func() time.Time

	latency *obs.Histogram
	errors  *obs.Counter
	nextVer int
}

func (s *Server) Handler() http.Handler {
	s.latency = s.Registry.Histogram("cvp_http_request_seconds", "HTTP request latency.", obs.LatencyBuckets, "route", "code")
	s.errors = s.Registry.Counter("cvp_http_errors_total", "HTTP responses with a 5xx code.", "route")
	mux := http.NewServeMux()
	route := func(pattern, name string, h http.HandlerFunc) {
		mux.Handle(pattern, s.instrument(name, h))
	}
	route("GET /healthz", "healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		s.Registry.Write(w)
	})
	route("GET /v1/policy", "policy", s.activePolicy)
	route("POST /v1/policies/preview", "preview", s.preview)
	route("POST /v1/policies/activate", "activate", s.activate)
	route("GET /v1/listings/{id}/visibility", "visibility", s.visibility)
	route("GET /v1/search", "search", s.search)
	route("POST /v1/feed", "feed", s.feed)
	return mux
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	w.code = c
	w.ResponseWriter.WriteHeader(c)
}

func (s *Server) instrument(name string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		h(sw, r)
		s.latency.Observe(time.Since(start).Seconds(), name, strconv.Itoa(sw.code/100)+"xx")
		if sw.code >= 500 {
			s.errors.Add(1, name)
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) activePolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.Mat.Policy())
}

// Impact is what a policy would change, per buyer/seller pair, computed without activating it.
type Impact struct {
	Version int              `json:"current_version"`
	Changes []PairImpact     `json:"changes"`
	Total   map[string]int   `json:"total"`
	Errors  []string         `json:"errors,omitempty"`
	Stats   visibility.Stats `json:"stats"`
	byPair  map[[2]string]*PairImpact
}

type PairImpact struct {
	Buyer     string `json:"buyer"`
	Seller    string `json:"seller"`
	Gained    int    `json:"gained"`
	Lost      int    `json:"lost"`
	WholePair bool   `json:"whole_pair"`
}

func decodePolicy(r *http.Request) (policy.Policy, error) {
	var p policy.Policy
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, errors.New("policy JSON: " + err.Error())
	}
	return p, nil
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	next, err := decodePolicy(r)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	if err := next.Validate(); err != nil {
		writeJSON(w, 422, Impact{Errors: []string{err.Error()}})
		return
	}
	writeJSON(w, 200, PreviewImpact(s.Mat, &next))
}

// PreviewImpact counts, per changed pair and channel feed, the listings that would appear or disappear.
func PreviewImpact(m *visibility.Materializer, next *policy.Policy) Impact {
	cur := m.Policy()
	imp := Impact{Version: cur.Version, Total: map[string]int{"gained": 0, "lost": 0}, byPair: map[[2]string]*PairImpact{}}
	for _, c := range policy.Diff(cur, next) {
		pi := &PairImpact{Buyer: c.Buyer, Seller: c.Seller, WholePair: c.Whole}
		imp.byPair[[2]string{c.Buyer, c.Seller}] = pi
	}
	m.Each(func(l policy.Listing) {
		for key, pi := range imp.byPair {
			if l.Seller != key[1] {
				continue
			}
			imp.Stats.Scanned++
			before := cur.Visible(l, key[0], policy.Feed)
			after := next.Visible(l, key[0], policy.Feed)
			switch {
			case after && !before:
				pi.Gained++
				imp.Total["gained"]++
			case before && !after:
				pi.Lost++
				imp.Total["lost"]++
			}
		}
	})
	for _, pi := range imp.byPair {
		imp.Changes = append(imp.Changes, *pi)
	}
	return imp
}

func (s *Server) activate(w http.ResponseWriter, r *http.Request) {
	next, err := decodePolicy(r)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	if err := next.Validate(); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	actor := r.Header.Get("X-Actor")
	if actor == "" {
		problem(w, 400, "X-Actor header required: every activation records who made it")
		return
	}
	ctx := r.Context()
	if s.Store != nil {
		stored, err := s.Store.Create(ctx, next, actor)
		if err != nil {
			problem(w, 500, "storing policy: "+err.Error())
			return
		}
		next = stored
		if err := s.Store.Activate(ctx, next.Version, actor, s.now()); err != nil {
			problem(w, 500, "activating policy: "+err.Error())
			return
		}
	} else {
		if s.nextVer <= s.Mat.Policy().Version {
			s.nextVer = s.Mat.Policy().Version
		}
		s.nextVer++
		next.Version = s.nextVer
	}
	body, _ := json.Marshal(visibility.PolicyEvent{Policy: next, At: s.now()})
	if err := s.Log.Publish(ctx, visibility.TopicPolicies, []byte("policy"), body); err != nil {
		problem(w, 503, "publishing activation: "+err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"version": next.Version, "status": "published"})
}

func (s *Server) visibility(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, 400, "listing id must be a number")
		return
	}
	masks, ok := s.Mat.Masks(id)
	if !ok {
		problem(w, 404, "unknown listing")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "policy_version": s.Mat.Policy().Version, "visible": visibility.Expand(s.Mat.Policy(), masks)})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	market := q.Get("market")
	if _, ok := s.Mat.Policy().MarketBit(market); !ok {
		problem(w, 400, "unknown market")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	hits, err := s.Index.Search(r.Context(), index.Query{Market: market, Channel: policy.Search, Text: q.Get("q"), Category: q.Get("category"), Limit: limit})
	if err != nil {
		problem(w, 502, "search backend: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"market": market, "hits": hits})
}

// FeedRequest carries two ranked candidate lists from the ranking service; the platform filters them by the
// active policy and balances them.
type FeedRequest struct {
	Market     string      `json:"market"`
	K          int         `json:"k"`
	Candidates []feed.Item `json:"candidates"`
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	var req FeedRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&req); err != nil {
		problem(w, 400, "feed request JSON: "+err.Error())
		return
	}
	p := s.Mat.Policy()
	bit, ok := p.MarketBit(req.Market)
	if !ok {
		problem(w, 400, "unknown market")
		return
	}
	var dom, cross []feed.Item
	dropped := 0
	feedBit := slices.Index(policy.Channels, policy.Feed)
	for _, c := range req.Candidates {
		masks, known := s.Mat.Masks(c.ID)
		l, _ := s.Mat.Listing(c.ID)
		if !known || masks[feedBit]&bit == 0 {
			dropped++ // never shown: the ranker proposed something the policy hides here
			continue
		}
		c.CrossBorder = l.Seller != req.Market
		if c.CrossBorder {
			cross = append(cross, c)
		} else {
			dom = append(dom, c)
		}
	}
	sortByScore(dom)
	sortByScore(cross)
	rules, ok := s.FeedRules[req.Market]
	if !ok {
		rules = feed.Rules{Window: 10, Min: 0, Max: 10}
	}
	k := req.K
	if k <= 0 || k > 200 {
		k = 20
	}
	writeJSON(w, 200, map[string]any{"items": feed.Merge(dom, cross, k, rules), "dropped": dropped, "rules": rules})
}

func sortByScore(items []feed.Item) {
	slices.SortStableFunc(items, func(a, b feed.Item) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
}
