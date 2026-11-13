import { motion, useReducedMotion } from "framer-motion";
import type { Design, Interval } from "../../shared/data/types";
import { signedPct } from "../../shared/ui/format";
import { color, font, layout, motion as m, radius, space } from "../../shared/theme/tokens";

const PAD = 0.04;
const TICKS = 5;

/** Each experiment design's 16 estimates of a full connection's GMV effect, against the true effect. */
export function DesignStrip({ designs, truth }: { designs: Design[]; truth: Interval }) {
  const reduce = useReducedMotion();
  const truths = designs.flatMap((d) => d.estimates.map((e) => e.truth));
  const values = designs.flatMap((d) => d.estimates.map((e) => e.value)).concat(truths, [0]);
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const x = (v: number) => (PAD + ((v - lo) / (hi - lo || 1)) * (1 - 2 * PAD)) * 100;
  const tLo = Math.min(...truths);
  const tHi = Math.max(...truths);
  const ticks = Array.from({ length: TICKS }, (_, i) => lo + ((hi - lo) * i) / (TICKS - 1));
  const grid = `${layout.stripLabelWidth}px 1fr ${layout.stripStatWidth}px`;
  let n = 0;
  return (
    <figure style={{ margin: 0 }} aria-label="Each design's estimates of the GMV effect against the true effect">
      <div style={{ display: "grid", gridTemplateColumns: grid, columnGap: space.md, alignItems: "end", marginBottom: space.xs }}>
        <span />
        <div style={{ position: "relative", height: space.lg }}>
          <span style={{ position: "absolute", left: `${x(truth.mean)}%`, transform: "translateX(-50%)", fontSize: font.size.xs, fontWeight: font.weight.semibold, whiteSpace: "nowrap" }}>
            true effect {signedPct(truth.mean)}
          </span>
        </div>
        <span style={{ fontSize: font.size.xs, color: color.inkFaint, textAlign: "right" }}>interval holds the truth</span>
      </div>
      {designs.map((d) => {
        const covered = Math.round(d.coverage * d.estimates.length);
        const good = d.coverage >= 0.8;
        return (
          <div key={d.id} role="group" aria-label={`${d.label}: bias ${signedPct(d.bias)}, interval holds the truth in ${covered} of ${d.estimates.length}`}
            style={{ display: "grid", gridTemplateColumns: grid, columnGap: space.md, alignItems: "center", minHeight: layout.stripRowHeight, borderTop: `1px solid ${color.rule}` }}>
            <span style={{ fontSize: font.size.sm, lineHeight: font.leading.snug }}>{d.label}</span>
            <div style={{ position: "relative", height: layout.stripRowHeight }}>
              <div style={{ position: "absolute", left: `${x(tLo)}%`, width: `${Math.max(x(tHi) - x(tLo), 0.5)}%`, top: space.xs, bottom: space.xs, background: color.truthBand, borderRadius: radius.sm }} />
              <div style={{ position: "absolute", left: `${x(truth.mean)}%`, top: space.xxs, bottom: space.xxs, width: 2, marginLeft: -1, background: color.truth }} />
              <div style={{ position: "absolute", left: `${x(0)}%`, top: 0, bottom: 0, width: 1, background: color.ruleStrong }} />
              {d.estimates.map((e, i) => {
                const hit = e.lo <= e.truth && e.truth <= e.hi;
                const delay = reduce ? 0 : n++ * m.stagger;
                const jitter = ((i % 4) - 1.5) * (layout.dot + 1);
                return (
                  <motion.span
                    key={i}
                    title={`estimate ${signedPct(e.value)} (${signedPct(e.lo)} to ${signedPct(e.hi)}), truth ${signedPct(e.truth)}`}
                    initial={reduce ? false : { opacity: 0, scale: 0.4 }}
                    animate={{ opacity: 1, scale: 1 }}
                    transition={{ duration: m.fade, delay, ease: m.ease }}
                    style={{
                      position: "absolute", left: `${x(e.value)}%`, top: `calc(50% + ${jitter}px)`,
                      width: layout.dot * 2, height: layout.dot * 2, marginLeft: -layout.dot, marginTop: -layout.dot,
                      borderRadius: radius.pill, background: hit ? color.honest : color.biased, opacity: 0.85,
                    }}
                  />
                );
              })}
            </div>
            <span className="num" style={{ textAlign: "right", fontSize: font.size.sm }}>
              <strong style={{ color: good ? color.honest : color.biased, fontWeight: font.weight.semibold }}>
                {covered} of {d.estimates.length}
              </strong>
              <span style={{ display: "block", fontSize: font.size.xs, color: color.inkFaint }}>bias {signedPct(d.bias)}</span>
            </span>
          </div>
        );
      })}
      <div style={{ display: "grid", gridTemplateColumns: grid, columnGap: space.md, borderTop: `1px solid ${color.rule}` }}>
        <span />
        <div style={{ position: "relative", height: space.lg }}>
          {ticks.map((t) => (
            <span key={t} className="num" style={{ position: "absolute", left: `${x(t)}%`, transform: "translateX(-50%)", top: space.xxs, fontSize: font.size.xs, color: color.inkFaint }}>
              {signedPct(t, 0)}
            </span>
          ))}
        </div>
        <span />
      </div>
      <figcaption style={{ marginTop: space.md, fontSize: font.size.sm, color: color.inkSoft, maxWidth: "var(--measure)" }}>
        One dot per replication: the GMV effect each design would have reported. Green when its own 95% interval holds that
        replication's true effect, red when it does not. The grey band spans the true effects across replications.
      </figcaption>
    </figure>
  );
}
