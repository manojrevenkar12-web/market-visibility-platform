// Package index is where visibility meets search: listing documents carry, per channel, the buyer markets that may
// see them, and every query filters on the buyer's market. Memory is the reference implementation; Vespa
// (vespa.go) speaks the same contract to a Vespa content cluster through its document and search APIs.
package index

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
)

// Doc is one listing as the search channel stores it.
type Doc struct {
	ID         uint64                      `json:"id"`
	Title      string                      `json:"title"`
	Category   string                      `json:"category"`
	Seller     string                      `json:"seller"`
	PriceCents int64                       `json:"price_cents"`
	CreatedAt  time.Time                   `json:"created_at"`
	Visible    map[policy.Channel][]string `json:"visible"`
}

// Query is a search from a buyer in one market on one channel.
type Query struct {
	Market   string
	Channel  policy.Channel
	Text     string
	Category string
	Limit    int
}

// Hit is a matching document.
type Hit struct {
	ID     uint64 `json:"id"`
	Seller string `json:"seller"`
	Title  string `json:"title"`
	Score  int    `json:"score"`
}

// Index is the contract every search backend implements.
type Index interface {
	Upsert(ctx context.Context, docs []Doc) error
	// SetVisibility replaces the visibility fields of one document without resending it (a partial update).
	SetVisibility(ctx context.Context, id uint64, visible map[policy.Channel][]string) error
	Delete(ctx context.Context, id uint64) error
	Search(ctx context.Context, q Query) ([]Hit, error)
}

// Memory is an in-process Index.
type Memory struct {
	mu   sync.RWMutex
	docs map[uint64]Doc
}

func NewMemory() *Memory { return &Memory{docs: map[uint64]Doc{}} }

func (m *Memory) Upsert(_ context.Context, docs []Doc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range docs {
		m.docs[d.ID] = d
	}
	return nil
}

func (m *Memory) SetVisibility(_ context.Context, id uint64, visible map[policy.Channel][]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.docs[id]
	if !ok {
		return nil // the document arrives later with its visibility; nothing to update yet
	}
	d.Visible = visible
	m.docs[id] = d
	return nil
}

func (m *Memory) Delete(_ context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, id)
	return nil
}

// Search ranks by the number of query terms in the title, then newest first.
func (m *Memory) Search(_ context.Context, q Query) ([]Hit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	terms := strings.Fields(strings.ToLower(q.Text))
	type scored struct {
		Hit
		created time.Time
	}
	var out []scored
	for _, d := range m.docs {
		if !slices.Contains(d.Visible[q.Channel], q.Market) {
			continue
		}
		if q.Category != "" && d.Category != q.Category {
			continue
		}
		score := 0
		title := strings.ToLower(d.Title)
		for _, t := range terms {
			if strings.Contains(title, t) {
				score++
			}
		}
		if len(terms) > 0 && score == 0 {
			continue
		}
		out = append(out, scored{Hit{ID: d.ID, Seller: d.Seller, Title: d.Title, Score: score}, d.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if !out[i].created.Equal(out[j].created) {
			return out[i].created.After(out[j].created)
		}
		return out[i].ID < out[j].ID
	})
	limit := q.Limit
	if limit <= 0 || limit > len(out) {
		limit = len(out)
	}
	hits := make([]Hit, limit)
	for i := range hits {
		hits[i] = out[i].Hit
	}
	return hits, nil
}

// Len is the number of documents held.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.docs)
}
