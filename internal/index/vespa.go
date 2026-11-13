package index

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
)

// Vespa writes listings through /document/v1 and queries them through /search/ with YQL. The application package
// it expects is deploy/vespa: schema `listing` with array<string> attributes visible_search, visible_feed and
// visible_similar (fast-search), so a visibility change is a partial update of one small field, not a re-feed.
type Vespa struct {
	Endpoint  string // e.g. http://localhost:8080
	Namespace string // document namespace, e.g. "marketplace"
	Client    *http.Client
}

func NewVespa(endpoint string) *Vespa {
	return &Vespa{Endpoint: strings.TrimRight(endpoint, "/"), Namespace: "marketplace", Client: &http.Client{Timeout: 5 * time.Second}}
}

func visibleField(ch policy.Channel) string { return "visible_" + string(ch) }

func (v *Vespa) docURL(id uint64) string {
	return fmt.Sprintf("%s/document/v1/%s/listing/docid/%d", v.Endpoint, v.Namespace, id)
}

func (v *Vespa) do(ctx context.Context, method, u string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("vespa %s %s: %s: %s", method, u, resp.Status, bytes.TrimSpace(out))
	}
	return out, nil
}

// Fields renders a document in the schema's field names.
func Fields(d Doc) map[string]any {
	f := map[string]any{
		"listing_id":  d.ID,
		"title":       d.Title,
		"category":    d.Category,
		"seller":      d.Seller,
		"price_cents": d.PriceCents,
		"created_at":  d.CreatedAt.Unix(),
	}
	for _, ch := range policy.Channels {
		m := d.Visible[ch]
		if m == nil {
			m = []string{}
		}
		f[visibleField(ch)] = m
	}
	return f
}

func (v *Vespa) Upsert(ctx context.Context, docs []Doc) error {
	for _, d := range docs {
		if _, err := v.do(ctx, http.MethodPost, v.docURL(d.ID), map[string]any{"fields": Fields(d)}); err != nil {
			return err
		}
	}
	return nil
}

// VisibilityUpdate is the partial-update body for a visibility change.
func VisibilityUpdate(visible map[policy.Channel][]string) map[string]any {
	fields := map[string]any{}
	for _, ch := range policy.Channels {
		m := visible[ch]
		if m == nil {
			m = []string{}
		}
		fields[visibleField(ch)] = map[string]any{"assign": m}
	}
	return map[string]any{"fields": fields}
}

func (v *Vespa) SetVisibility(ctx context.Context, id uint64, visible map[policy.Channel][]string) error {
	// create=false: an update for a document that is not fed yet is dropped, as in Memory
	_, err := v.do(ctx, http.MethodPut, v.docURL(id), VisibilityUpdate(visible))
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil
	}
	return err
}

func (v *Vespa) Delete(ctx context.Context, id uint64) error {
	_, err := v.do(ctx, http.MethodDelete, v.docURL(id), nil)
	return err
}

// YQL builds the query: the market filter is always present, so no channel can forget it.
func YQL(q Query) string {
	conds := []string{fmt.Sprintf(`%s contains %s`, visibleField(q.Channel), quote(q.Market))}
	if q.Category != "" {
		conds = append(conds, fmt.Sprintf(`category contains %s`, quote(q.Category)))
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		var terms []string
		for _, w := range strings.Fields(strings.ToLower(t)) {
			terms = append(terms, fmt.Sprintf(`title contains %s`, quote(w)))
		}
		conds = append(conds, "("+strings.Join(terms, " or ")+")")
	}
	return "select listing_id, seller, title from listing where " + strings.Join(conds, " and ")
}

// quote escapes a string literal for YQL.
func quote(s string) string { return strconv.Quote(s) }

func (v *Vespa) Search(ctx context.Context, q Query) ([]Hit, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	params := url.Values{"yql": {YQL(q)}, "hits": {strconv.Itoa(limit)}, "ranking": {"by_terms"}}
	body, err := v.do(ctx, http.MethodGet, v.Endpoint+"/search/?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Root struct {
			Children []struct {
				Relevance float64 `json:"relevance"`
				Fields    struct {
					ID     uint64 `json:"listing_id"`
					Seller string `json:"seller"`
					Title  string `json:"title"`
				} `json:"fields"`
			} `json:"children"`
		} `json:"root"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(res.Root.Children))
	for _, c := range res.Root.Children {
		hits = append(hits, Hit{ID: c.Fields.ID, Seller: c.Fields.Seller, Title: c.Fields.Title, Score: int(c.Relevance)})
	}
	return hits, nil
}
