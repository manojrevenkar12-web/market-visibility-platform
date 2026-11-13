import type { Effect } from "../../shared/data/types";
import { pct, pp, signedPct } from "../../shared/ui/format";
import { color, font, radius, space } from "../../shared/theme/tokens";

/** A full connection with and without the feed cap on cross-border items. */
export function CapCompare({ open, capped, home }: { open: Effect; capped: Effect; home: string }) {
  const rows: { label: string; e: Effect }[] = [
    { label: "Open, no cap", e: open },
    { label: "Open, at most 6 of 24 feed slots cross-border", e: capped },
  ];
  return (
    <div className="cards">
      {rows.map(({ label, e }) => (
        <div key={label} style={{ background: color.surface, border: `1px solid ${color.rule}`, borderRadius: radius.md, padding: space.lg }}>
          <p style={{ fontSize: font.size.sm, color: color.inkSoft, marginBottom: space.sm }}>{label}</p>
          <dl className="num" style={{ display: "grid", gridTemplateColumns: "1fr auto", rowGap: space.xs, margin: 0, fontSize: font.size.sm }}>
            <dt>GMV against closed</dt>
            <dd style={{ margin: 0, fontWeight: font.weight.semibold }}>{signedPct(e.gmv_lift.mean)}</dd>
            <dt>{home} sellers' sell-through</dt>
            <dd style={{ margin: 0, fontWeight: font.weight.semibold, color: color.de }}>{pp(e.str_change_pp[home].mean)}</dd>
            <dt>Cross-border share of GMV</dt>
            <dd style={{ margin: 0 }}>{pct(e.cross_border_share_of_gmv.mean, 1)}</dd>
          </dl>
        </div>
      ))}
    </div>
  );
}
