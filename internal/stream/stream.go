// Package stream is the event log the platform reads and writes: keyed records in partitioned topics, offsets,
// and consumer groups that commit what they have processed. Log is satisfied by the in-process log below and by the
// Kafka adapter in adapters/kafka; the pipeline is written against the interface and tested on both.
package stream

import (
	"context"
	"errors"
	"hash/fnv"
	"sync"
	"time"
)

// Record is one event. Records with the same key go to the same partition and keep their order.
type Record struct {
	Topic     string
	Key       []byte
	Value     []byte
	Partition int
	Offset    int64
	Time      time.Time
}

// Log is the subset of Kafka the platform relies on.
type Log interface {
	Publish(ctx context.Context, topic string, key, value []byte) error
	// Poll returns up to max records for the group, waiting up to wait for the first one.
	Poll(ctx context.Context, group, topic string, max int, wait time.Duration) ([]Record, error)
	// Commit marks records up to and including these as processed for the group.
	Commit(ctx context.Context, group string, records []Record) error
	// Lag is the number of records the group has not committed yet, summed over partitions.
	Lag(group, topic string) (int64, error)
}

// Partition picks the partition of a key, like Kafka's default partitioner picks one stable partition per key.
func Partition(key []byte, partitions int) int {
	h := fnv.New32a()
	_, _ = h.Write(key)
	return int(h.Sum32() % uint32(partitions))
}

// Memory is an in-process Log: a slice per partition, offsets per group. Safe for concurrent use.
type Memory struct {
	mu         sync.Mutex
	cond       *sync.Cond
	partitions int
	topics     map[string][][]Record
	committed  map[string]map[string][]int64 // group -> topic -> next offset per partition
	delivered  map[string]map[string][]int64 // group -> topic -> next offset to hand out
	now        func() time.Time
}

// NewMemory makes a log whose topics have `partitions` partitions.
func NewMemory(partitions int) *Memory {
	m := &Memory{
		partitions: partitions,
		topics:     map[string][][]Record{},
		committed:  map[string]map[string][]int64{},
		delivered:  map[string]map[string][]int64{},
		now:        time.Now,
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

func (m *Memory) topic(name string) [][]Record {
	t, ok := m.topics[name]
	if !ok {
		t = make([][]Record, m.partitions)
		m.topics[name] = t
	}
	return t
}

func (m *Memory) offsets(table map[string]map[string][]int64, group, topic string) []int64 {
	g, ok := table[group]
	if !ok {
		g = map[string][]int64{}
		table[group] = g
	}
	o, ok := g[topic]
	if !ok {
		o = make([]int64, m.partitions)
		g[topic] = o
	}
	return o
}

func (m *Memory) Publish(_ context.Context, topic string, key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.topic(topic)
	p := Partition(key, m.partitions)
	t[p] = append(t[p], Record{Topic: topic, Key: key, Value: value, Partition: p, Offset: int64(len(t[p])), Time: m.now()})
	m.cond.Broadcast()
	return nil
}

func (m *Memory) Poll(ctx context.Context, group, topic string, max int, wait time.Duration) ([]Record, error) {
	deadline := time.Now().Add(wait)
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t := m.topic(topic)
		next := m.offsets(m.delivered, group, topic)
		var out []Record
		for p := 0; p < m.partitions && len(out) < max; p++ {
			for next[p] < int64(len(t[p])) && len(out) < max {
				out = append(out, t[p][next[p]])
				next[p]++
			}
		}
		if len(out) > 0 || !time.Now().Before(deadline) {
			return out, nil
		}
		// wake up on a publish or when the wait runs out
		timer := time.AfterFunc(time.Until(deadline), m.cond.Broadcast)
		m.cond.Wait()
		timer.Stop()
	}
}

func (m *Memory) Commit(_ context.Context, group string, records []Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range records {
		c := m.offsets(m.committed, group, r.Topic)
		if r.Partition < 0 || r.Partition >= m.partitions {
			return errors.New("record from an unknown partition")
		}
		if r.Offset+1 > c[r.Partition] {
			c[r.Partition] = r.Offset + 1
		}
	}
	return nil
}

// Rewind hands records from the last commit out again, as a restarted consumer would see them.
func (m *Memory) Rewind(group, topic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy(m.offsets(m.delivered, group, topic), m.offsets(m.committed, group, topic))
}

func (m *Memory) Lag(group, topic string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.topic(topic)
	c := m.offsets(m.committed, group, topic)
	var lag int64
	for p := range t {
		lag += int64(len(t[p])) - c[p]
	}
	return lag, nil
}
