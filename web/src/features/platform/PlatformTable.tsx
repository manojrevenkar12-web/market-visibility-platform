import type { Bench } from "../../shared/data/types";
import { thousands } from "../../shared/ui/format";
import { color, font, space } from "../../shared/theme/tokens";

function get(b: Bench, ...path: string[]): number {
  let cur: unknown = b;
  for (const p of path) cur = (cur as Record<string, unknown> | undefined)?.[p];
  return typeof cur === "number" ? cur : NaN;
}

/** Platform measurements from reports/bench.json. */
export function PlatformTable({ bench }: { bench: Bench }) {
  const rows: [string, string][] = [
    ["One visibility decision", `${get(bench, "visible_ns_per_call").toFixed(0)} ns`],
    ["Materialise 1,000,000 listings, 8 markets, 3 channels", `${get(bench, "materialise", "seconds").toFixed(1)} s`],
    [
      "Ramp a connection from 20% to 30% on that catalogue",
      `${get(bench, "ramp_step", "incremental_ms").toFixed(0)} ms, ${thousands(get(bench, "ramp_step", "index_writes"))} index writes (full recompute ${(get(bench, "ramp_step", "full_recompute_ms") / 1000).toFixed(1)} s)`,
    ],
    ["Visibility lag at 5,000 listing events a second", `p99 ${get(bench, "pipeline_steady", "lag_p99_ms").toFixed(2)} ms`],
    ["Policy activation on 200,000 listings", `${get(bench, "policy_activation", "ms_to_last_index_write").toFixed(0)} ms to the last index write`],
    ["Feed request, 200 candidates filtered and balanced", `p99 ${get(bench, "feed_http", "p99_us").toFixed(0)} µs`],
  ];
  const cell: React.CSSProperties = { padding: `${space.xs}px 0`, borderBottom: `1px solid ${color.rule}`, verticalAlign: "top" };
  return (
    <table className="num" style={{ width: "100%", borderCollapse: "collapse", fontSize: font.size.sm }}>
      <caption style={{ textAlign: "left", fontSize: font.size.xs, color: color.inkSoft, marginBottom: space.xs }}>
        In process on {String(bench.cpus)} CPUs; Vespa and Kafka run in CI for correctness, not load
      </caption>
      <tbody>
        {rows.map(([k, v]) => (
          <tr key={k}>
            <td style={{ ...cell, color: color.inkSoft, paddingRight: space.lg }}>{k}</td>
            <td style={{ ...cell, textAlign: "right", fontWeight: font.weight.semibold }}>{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
