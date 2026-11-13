// Command cvp runs the content visibility platform: the HTTP API and the pipeline that keeps the index in line
// with the active policy.
//
//	go run ./cmd/cvp -demo 50000
//	go run ./cmd/cvp -postgres "$DSN" -redis localhost:6379 -vespa http://localhost:8080
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/api"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/feed"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/index"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/obs"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/store"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/stream"
	"github.com/manojrevenkar12-web/market-visibility-platform/internal/visibility"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	markets := flag.String("markets", "FR,DE,BE,ES,IT,NL", "markets the platform serves")
	pgDSN := flag.String("postgres", "", "PostgreSQL DSN for policies (empty: in memory)")
	redisAddr := flag.String("redis", "", "Redis address for the visibility cache (empty: none)")
	vespaURL := flag.String("vespa", "", "Vespa endpoint (empty: in-memory index)")
	demo := flag.Int("demo", 0, "publish this many demo listings at start")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, *addr, strings.Split(*markets, ","), *pgDSN, *redisAddr, *vespaURL, *demo); err != nil {
		logger.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, addr string, markets []string, pgDSN, redisAddr, vespaURL string, demo int) error {
	initial := &policy.Policy{Version: 0, Markets: markets}
	var policies *store.Policies
	if pgDSN != "" {
		var err error
		if policies, err = store.OpenPolicies(ctx, pgDSN); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		if p, err := policies.Active(ctx); err == nil {
			initial = &p
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if err := initial.Validate(); err != nil {
		return err
	}
	reg := obs.NewRegistry()
	log := stream.NewMemory(16)
	var idx index.Index = index.NewMemory()
	if vespaURL != "" {
		idx = index.NewVespa(vespaURL)
	}
	mat := visibility.NewMaterializer(initial)
	pipe := &visibility.Pipeline{Log: log, Mat: mat, Index: idx, Metrics: visibility.NewMetrics(reg)}
	if redisAddr != "" {
		r, err := store.DialRedis(ctx, redisAddr)
		if err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		defer r.Close()
		r.TTL = 48 * time.Hour
		pipe.Cache = r
	}
	srv := &api.Server{Log: log, Mat: mat, Index: idx, Registry: reg, FeedRules: map[string]feed.Rules{}}
	if policies != nil {
		srv.Store = policies
	}
	for _, m := range markets {
		srv.FeedRules[m] = feed.Rules{Window: 12, Min: 1, Max: 4}
	}
	lag := reg.Gauge("cvp_consumer_lag_records", "Records not yet processed by the materializer.", "topic")
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, topic := range []string{visibility.TopicListings, visibility.TopicPolicies} {
					n, _ := log.Lag(visibility.Group, topic)
					lag.Set(float64(n), topic)
				}
			}
		}
	}()
	go func() {
		if err := pipe.Run(ctx); err != nil {
			logger.Error("pipeline stopped", "err", err)
		}
	}()
	if demo > 0 {
		publishDemo(ctx, log, markets, demo)
	}
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shut)
	}()
	logger.Info("listening", "addr", addr, "policy_version", initial.Version)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func publishDemo(ctx context.Context, log stream.Log, markets []string, n int) {
	rng := rand.New(rand.NewPCG(42, 42))
	cats := []string{"women", "men", "kids", "home", "entertainment"}
	words := []string{"vintage", "denim", "jacket", "leather", "bag", "sneakers", "dress", "wool", "coat", "lamp"}
	for i := 1; i <= n; i++ {
		l := policy.Listing{ID: uint64(i), Seller: markets[rng.IntN(len(markets))], Category: cats[rng.IntN(len(cats))], WeightG: 200 + rng.IntN(6000), Active: true}
		title := words[rng.IntN(len(words))] + " " + words[rng.IntN(len(words))]
		b, _ := json.Marshal(visibility.ListingEvent{Listing: l, Title: title, PriceCents: int64(500 + rng.IntN(6000)), CreatedAt: time.Now(), At: time.Now()})
		_ = log.Publish(ctx, visibility.TopicListings, []byte(strconv.Itoa(i)), b)
	}
}
