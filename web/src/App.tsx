import { useState } from "react";
import { AnimatePresence, motion } from "framer-motion";
import { hasResults, useLabData } from "./shared/data/useLabData";
import type { LabData, World } from "./shared/data/types";
import { EmptyState, ErrorState, LoadingSkeleton } from "./shared/ui/States";
import { Section } from "./shared/ui/Section";
import { exposure, pct, signedPct, thousands } from "./shared/ui/format";
import { color, font, motion as m, radius, space } from "./shared/theme/tokens";
import { DesignStrip } from "./features/designs/DesignStrip";
import { RampChart } from "./features/ramp/RampChart";
import { CapCompare } from "./features/balance/CapCompare";
import { PlatformTable } from "./features/platform/PlatformTable";

const REPO = "https://github.com/manojrevenkar12-web/market-visibility-platform";
type Key = "dev" | "heldout";

export function App() {
  const q = useLabData();
  if (q.isPending) return <LoadingSkeleton />;
  if (q.isError) return <ErrorState message={(q.error as Error).message} onRetry={() => q.refetch()} />;
  if (!hasResults(q.data)) return <EmptyState />;
  return <Report data={q.data} />;
}

function Report({ data }: { data: LabData }) {
  const [key, setKey] = useState<Key>("dev");
  const w: World = (key === "heldout" && data.heldout) || data.dev;
  const split = w.designs.find((d) => d.id === "listing_split");
  const splitMean = split ? split.estimates.reduce((s, e) => s + e.value, 0) / split.estimates.length : NaN;
  const [a, b] = w.markets;
  const rec = w.recommendation.exposureBP;
  return (
    <main className="page">
      <header style={{ display: "grid", gap: space.md, marginBottom: space.xl }}>
        <p style={{ fontSize: font.size.sm }}>
          <a href={REPO}>market-visibility-platform</a>
        </p>
        <h1 style={{ fontSize: `clamp(${font.size.xxl}px, 5vw, ${font.size.display}px)`, lineHeight: font.leading.tight, letterSpacing: font.tracking.display, fontWeight: font.weight.bold, maxWidth: "17ch" }}>
          Should {a} and {b} see each other&rsquo;s listings?
        </h1>
        <p className="prose" style={{ fontSize: font.size.lg, lineHeight: font.leading.snug, color: color.ink }}>
          A full connection {w.truth.gmv_lift.mean >= 0 ? "adds" : "costs"} {pct(Math.abs(w.truth.gmv_lift.mean), 1)} GMV in this world.
          Reading a 50% ramp as an A/B test would have reported {signedPct(splitMean)}.
        </p>
        {data.heldout && <WorldSwitch value={key} onChange={setKey} />}
      </header>
      <AnimatePresence mode="wait">
        <motion.div key={key} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: m.fade }}>
          <DesignStrip designs={w.designs} truth={w.truth.gmv_lift} />

          <div className="two-col section">
            <div>
              <div className="section-head">
                <h2>A gradual connection</h2>
                <p className="prose">
                  Each step shows a fixed share of each market&rsquo;s listings to the other; raising it only adds listings.
                  Recommended: <strong style={{ color: rec > 0 ? color.honest : color.biased }}>{rec > 0 ? exposure(rec) : "stay closed"}</strong>
                  {rec > 0 ? `, at ${signedPct(w.recommendation.gmvLift)} GMV` : ", because no step lifts GMV with confidence"}.
                </p>
              </div>
              <RampChart ramp={w.ramp} markets={w.markets} recommended={rec} />
            </div>
            <div>
              <div className="section-head">
                <h2>Capping cross-border items in the feed</h2>
                <p className="prose">
                  The balancer keeps at most 6 cross-border items in every 24 feed positions. {b} sellers keep more of
                  their buyers; the connection keeps most of its gain.
                </p>
              </div>
              <CapCompare open={w.ramp[w.ramp.length - 1]} capped={w.capped} home={b} />
            </div>
          </div>

          {data.bench && (
            <Section id="platform" title="The platform underneath" lede="One policy, materialized per listing, kept in line with search and feeds through the event log.">
              <PlatformTable bench={data.bench} />
            </Section>
          )}

          <footer style={{ marginTop: space.section, paddingTop: space.lg, borderTop: `1px solid ${color.rule}`, fontSize: font.size.sm, color: color.inkSoft }}>
            Simulated marketplace, {w.world} world: {thousands(w.buyers)} buyers, {thousands(w.listingsPerDay)} new listings a day,
            {" "}{w.testDays} test days, {w.seeds} replications. Every number is generated from <code>reports/</code>.
          </footer>
        </motion.div>
      </AnimatePresence>
    </main>
  );
}

function WorldSwitch({ value, onChange }: { value: Key; onChange: (k: Key) => void }) {
  const opts: { key: Key; label: string }[] = [
    { key: "dev", label: "Development world" },
    { key: "heldout", label: "Held-out world, run once" },
  ];
  return (
    <div role="radiogroup" aria-label="World" style={{ display: "inline-flex", gap: space.xxs, padding: space.xxs, background: color.surface, border: `1px solid ${color.rule}`, borderRadius: radius.pill, width: "fit-content" }}>
      {opts.map((o) => {
        const on = o.key === value;
        return (
          <button key={o.key} role="radio" aria-checked={on} onClick={() => onChange(o.key)}
            style={{ position: "relative", font: "inherit", fontSize: font.size.sm, padding: `${space.xs}px ${space.md}px`, border: 0, borderRadius: radius.pill, cursor: "pointer", background: "transparent", color: on ? color.surface : color.inkSoft }}>
            {on && <motion.span layoutId="world-pill" transition={{ duration: m.fade, ease: m.ease }} style={{ position: "absolute", inset: 0, borderRadius: radius.pill, background: color.ink }} />}
            <span style={{ position: "relative" }}>{o.label}</span>
          </button>
        );
      })}
    </div>
  );
}
