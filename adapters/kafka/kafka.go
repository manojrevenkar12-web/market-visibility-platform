// Package kafka implements the platform's stream.Log on Apache Kafka (or Redpanda) with franz-go. It lives in its
// own module so the core service builds without a Kafka client; CI runs the same contract tests against Redpanda.
package kafka

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/stream"
)

// Log talks to a Kafka cluster. One producer client is shared; each (group, topic) gets its own consumer client.
type Log struct {
	brokers  []string
	producer *kgo.Client
	admin    *kadm.Client

	mu        sync.Mutex
	consumers map[[2]string]*kgo.Client
}

var _ stream.Log = (*Log)(nil)

func New(brokers ...string) (*Log, error) {
	p, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.AllowAutoTopicCreation(),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(2*time.Millisecond),
	)
	if err != nil {
		return nil, err
	}
	return &Log{brokers: brokers, producer: p, admin: kadm.NewClient(p), consumers: map[[2]string]*kgo.Client{}}, nil
}

func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.consumers {
		c.Close()
	}
	l.producer.Close()
}

// Publish writes synchronously: the call returns once the record is acknowledged by all in-sync replicas.
func (l *Log) Publish(ctx context.Context, topic string, key, value []byte) error {
	return l.producer.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: key, Value: value}).FirstErr()
}

func (l *Log) consumer(group, topic string) (*kgo.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := [2]string{group, topic}
	if c, ok := l.consumers[k]; ok {
		return c, nil
	}
	c, err := kgo.NewClient(
		kgo.SeedBrokers(l.brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		return nil, err
	}
	l.consumers[k] = c
	return c, nil
}

func (l *Log) Poll(ctx context.Context, group, topic string, max int, wait time.Duration) ([]stream.Record, error) {
	c, err := l.consumer(group, topic)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, max1(wait))
	defer cancel()
	fetches := c.PollRecords(pctx, max)
	for _, fe := range fetches.Errors() {
		if errors.Is(fe.Err, context.DeadlineExceeded) || errors.Is(fe.Err, context.Canceled) {
			continue
		}
		return nil, fe.Err
	}
	var out []stream.Record
	fetches.EachRecord(func(r *kgo.Record) {
		out = append(out, stream.Record{
			Topic: r.Topic, Key: r.Key, Value: r.Value, Partition: int(r.Partition), Offset: r.Offset, Time: r.Timestamp,
		})
	})
	return out, ctx.Err()
}

// max1 keeps a zero wait from becoming an already-expired context, which would return nothing ever.
func max1(d time.Duration) time.Duration {
	if d < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	return d
}

func (l *Log) Commit(ctx context.Context, group string, records []stream.Record) error {
	if len(records) == 0 {
		return nil
	}
	c, err := l.consumer(group, records[0].Topic)
	if err != nil {
		return err
	}
	recs := make([]*kgo.Record, len(records))
	for i, r := range records {
		recs[i] = &kgo.Record{Topic: r.Topic, Partition: int32(r.Partition), Offset: r.Offset, LeaderEpoch: -1}
	}
	return c.CommitRecords(ctx, recs...)
}

func (l *Log) Lag(group, topic string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lags, err := l.admin.Lag(ctx, group)
	if err != nil {
		return 0, err
	}
	g, ok := lags[group]
	if !ok {
		return 0, nil
	}
	if g.Error() != nil {
		return 0, g.Error()
	}
	return g.Lag.TotalByTopic()[topic].Lag, nil
}
