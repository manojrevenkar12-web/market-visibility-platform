import type { Effect } from "../../shared/data/types";
import { Frame, extent, linear, PLOT_WIDTH } from "../../shared/ui/Plot";
import { exposure, pp, signedPct } from "../../shared/ui/format";
import { color, font, layout, space } from "../../shared/theme/tokens";

const Y_TICKS = 5;

/** GMV effect as exposure grows, with its interval, and each side's sell-through change under it. */
export function RampChart({ ramp, markets, recommended }: { ramp: Effect[]; markets: string[]; recommended: number }) {
  const h = layout.plotHeight;
  const pad = layout.axisPadding;
  const points = [{ exposure_bp: 0, gmv_lift: { mean: 0, lo: 0, hi: 0 } }, ...ramp];
  const xs = points.map((p) => p.exposure_bp);
  const x = linear([0, Math.max(...xs)], [pad, PLOT_WIDTH - pad]);
  const yd = extent(points.flatMap((p) => [p.gmv_lift.lo, p.gmv_lift.hi]), 0.15);
  const y = linear(yd, [h - pad, pad / 2]);
  const yTicks = Array.from({ length: Y_TICKS }, (_, i) => yd[0] + ((yd[1] - yd[0]) * i) / (Y_TICKS - 1)).map((v) => ({ v, label: signedPct(v, 1) }));
  const line = points.map((p, i) => `${i ? "L" : "M"}${x(p.exposure_bp)},${y(p.gmv_lift.mean)}`).join(" ");
  const band =
    points.map((p, i) => `${i ? "L" : "M"}${x(p.exposure_bp)},${y(p.gmv_lift.hi)}`).join(" ") +
    [...points].reverse().map((p) => `L${x(p.exposure_bp)},${y(p.gmv_lift.lo)}`).join(" ") + "Z";
  const marketColor = (m: string) => (m === "FR" ? color.fr : m === "DE" ? color.de : color.inkSoft);
  return (
    <div>
      <Frame height={h} x={x} y={y} xTicks={xs.map((v) => ({ v, label: exposure(v) }))} yTicks={yTicks} xLabel="share of each market's listings shown to the other" yLabel="GMV against staying closed" title="GMV effect by exposure">
        <line x1={pad} x2={PLOT_WIDTH - pad / 2} y1={y(0)} y2={y(0)} stroke={color.ruleStrong} />
        <path d={band} fill={color.truthBand} />
        <path d={line} fill="none" stroke={color.ink} strokeWidth={2.5} />
        {points.map((p) => (
          <circle key={p.exposure_bp} cx={x(p.exposure_bp)} cy={y(p.gmv_lift.mean)} r={layout.dot} fill={p.exposure_bp === recommended && recommended > 0 ? color.honest : color.ink}>
            <title>{`${exposure(p.exposure_bp)}: ${signedPct(p.gmv_lift.mean)}`}</title>
          </circle>
        ))}
      </Frame>
      <table className="num" style={{ width: "100%", borderCollapse: "collapse", fontSize: font.size.sm, marginTop: space.md }}>
        <caption style={{ textAlign: "left", fontSize: font.size.xs, color: color.inkSoft, marginBottom: space.xs }}>Sell-through change, by seller market</caption>
        <thead>
          <tr>
            <th style={th}>Exposure</th>
            {markets.map((m) => (
              <th key={m} style={{ ...th, textAlign: "right", color: marketColor(m) }}>{m} sellers</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {ramp.map((e) => (
            <tr key={e.exposure_bp}>
              <td style={td}>{exposure(e.exposure_bp)}</td>
              {markets.map((m) => (
                <td key={m} style={{ ...td, textAlign: "right" }}>{pp(e.str_change_pp[m].mean)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const th: React.CSSProperties = { textAlign: "left", fontWeight: font.weight.medium, fontSize: font.size.xs, color: color.inkSoft, padding: `${space.xxs}px 0`, borderBottom: `1px solid ${color.rule}` };
const td: React.CSSProperties = { padding: `${space.xxs}px 0`, borderBottom: `1px solid ${color.rule}` };
