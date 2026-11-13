# Decisions

## D1. Visibility is materialized per listing, not evaluated per query

A channel that evaluates rules at query time has to know every rule, and each channel ends up with its own copy.
Here the policy is evaluated once per listing into a bitmask per channel (bit = buyer market), written to the search
index as `visible_<channel>` and to Redis for request-time checks. Queries only filter on the buyer's market. One
decision costs about 55 ns; a million listings materialize in under 3 s on 2 cores.

## D2. Stable buckets make ramps monotonic and cheap

Each connection has a salt; a listing's bucket is FNV-1a(salt, id) mod 10,000. Exposure in basis points shows the
listings whose bucket is below it. Raising exposure only adds listings, lowering only removes them (tested over
200,000 listings). `Diff` turns two policies into bands of buckets per pair, so a ramp step recomputes only that band:
moving FR→DE from 20% to 30% on a million listings recomputed 12,273 and wrote 7,615, in 70 ms against 1.5 s for a
full recompute. Changing the salt, the channels, the exclusions or the weight limit marks the whole seller market.

## D3. Commit after the write; replays are no-ops

The pipeline writes the index and the cache, then commits. A crash between the two replays the batch; the
materializer compares masks before writing, so the replay changes nothing. The test loses a commit on purpose.
An invalid policy on the topic is counted (`cvp_events_total{topic="refused_policy"}`, with an alert) and skipped,
so it cannot block the policies behind it.

## D4. The log is an interface; Kafka is an adapter in its own module

The service is built against `stream.Log` (keyed partitions, offsets, groups, lag). The in-process log runs the
tests and the benchmark; `adapters/kafka` implements the same interface with franz-go and is tested against Redpanda
in CI, so the core module carries no Kafka client.

## D5. Vespa holds the visibility fields as attributes

`visible_search`, `visible_feed` and `visible_similar` are `array<string>` attributes with fast-search. A
visibility change is a partial `assign` of those fields, never a re-feed of the document. CI deploys
`deploy/vespa` into a container and runs the same contract as the in-memory index.

## D6. Policy versions are append-only and issued under an advisory lock

A first version issued numbers inside a serializable transaction; 20 concurrent creates failed with serialization
errors even after retries. A transaction-scoped advisory lock queues them instead: 20 concurrent creates, 20 distinct
versions. Activating an older version is a rollback and is recorded like any activation.

## D7. The feed balancer bounds every window, not the whole page

Bounding the share over the whole page lets all cross-border items land at the bottom. `Merge` keeps at most `max`
and, while there are cross-border items left, at least `min` in every window of `window` positions, and keeps each
source's ranking order. 2,000 randomized cases check the bounds.

## D8. The study scores designs against a truth it can compute

Each replication runs the same world under every design; the closed and fully open forks give that replication's
true effect. Each design then reports an estimate with its own interval, and the study counts how often the
interval holds the truth. That is how the listing split (comparing listings inside a 50% ramp) shows up as biased by
+9.8 points: treated listings take buyers from control listings, and the comparison books that as gain.

## D9. The first ramp rule was wrong, and the held-out world showed it

The rule as first written picked the largest exposure whose domestic sell-through drop stayed within 1 point. In
the held-out world, where crossing the border costs buyers more, a full connection lost 2.0% of GMV and the rule
still chose it. The corrected rule (`cmd/report`, `Recommend`) also requires the GMV lift to be above zero at the low
end of its interval; it says stay closed. The held-out world is not re-run: its report keeps the first rule's answer,
and the README shows both.
