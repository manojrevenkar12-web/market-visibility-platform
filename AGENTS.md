# Working in this repository

## Fast feedback

- `gofmt -l .`, `go vet ./...` and `go test -race ./...` before every commit (about 40 s). Set `CVP_POSTGRES_DSN` and
  `CVP_REDIS_ADDR` to run the store tests; CI always does.
- `go run ./cmd/report -check` when a report changes: the README results come from `reports/*.json`.
- `cd adapters/kafka && go test ./...` with `CVP_KAFKA_BROKERS` set, when the event log contract changes.
- `cd web && npm test` for the dashboard.

## Invariants

- **One policy, every channel.** Search, feed and similar items read visibility from the same materialized masks;
  no channel evaluates rules of its own. `GET /v1/search` and `POST /v1/feed` filter on the buyer's market always.
- **Ramps only grow by adding listings.** Buckets come from a fixed salt per connection; changing the salt reshuffles
  the market and `Diff` treats it as touching every listing of that seller market.
- **Commit after the write.** The pipeline commits offsets only after the index and the cache have the batch;
  replays must be no-ops (`TestReplayAfterCrashChangesNothing`).
- **Policies are validated before they are stored or published,** versions are append-only, and every activation
  names who made it (`X-Actor`).
- **Truth stays in the world.** Only `internal/market`, `internal/experiment` and `cmd/lab` know what buyers like.
  The platform packages never import them; the world uses the platform's own policy code to decide visibility.
- **Same world for every design.** Forks share listings, buyers and random draws; a comparison that breaks this is
  not a comparison.
- **Settings in config.** World truth in `config/world*.json`, study settings in `config/lab.json`.
- **Reports are generated.** `reports/*.json`, the README results and `web/public/data.json` come from commands.

## The held-out rule

`config/world_heldout.json` is a world nobody tuned against. `go run ./cmd/lab -world heldout` checks the lock in
`config/heldout.lock`, refuses a second run, and writes `reports/heldout.json`. Decisions are made on the dev world.
