import type { ReactNode } from "react";
import { color, font, layout } from "../theme/tokens";

/** A plot frame in a fixed coordinate space (scaled to the container's width), with axes and ticks. */
export type Scale = (v: number) => number;

export function linear(domain: [number, number], range: [number, number]): Scale {
  const [d0, d1] = domain;
  const [r0, r1] = range;
  const k = d1 === d0 ? 0 : (r1 - r0) / (d1 - d0);
  return (v) => r0 + (v - d0) * k;
}

export function extent(values: number[], padShare: number): [number, number] {
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const pad = (hi - lo || Math.abs(hi) || 1) * padShare;
  return [lo - pad, hi + pad];
}

export const PLOT_WIDTH = 520;

export function Frame({
  height,
  x,
  y,
  xTicks,
  yTicks,
  xLabel,
  yLabel,
  title,
  children,
}: {
  height: number;
  x: Scale;
  y: Scale;
  xTicks: { v: number; label: string }[];
  yTicks: { v: number; label: string }[];
  xLabel: string;
  yLabel: string;
  title: string;
  children: ReactNode;
}) {
  const pad = layout.axisPadding;
  return (
    <svg viewBox={`0 0 ${PLOT_WIDTH} ${height}`} width="100%" role="img" aria-label={title} style={{ overflow: "visible", fontFamily: font.family }}>
      <line x1={pad} x2={PLOT_WIDTH - pad / 2} y1={height - pad} y2={height - pad} stroke={color.ruleStrong} />
      <line x1={pad} x2={pad} y1={pad / 2} y2={height - pad} stroke={color.ruleStrong} />
      {xTicks.map((t) => (
        <text key={`x${t.v}`} x={x(t.v)} y={height - pad + font.size.xs + 4} fontSize={font.size.xs} fill={color.inkFaint} textAnchor="middle">
          {t.label}
        </text>
      ))}
      {yTicks.map((t) => (
        <g key={`y${t.v}`}>
          <line x1={pad} x2={PLOT_WIDTH - pad / 2} y1={y(t.v)} y2={y(t.v)} stroke={color.rule} strokeDasharray="2 4" />
          <text x={pad - 6} y={y(t.v) + 4} fontSize={font.size.xs} fill={color.inkFaint} textAnchor="end">
            {t.label}
          </text>
        </g>
      ))}
      <text x={PLOT_WIDTH - pad / 2} y={height - 4} fontSize={font.size.xs} fill={color.inkSoft} textAnchor="end">
        {xLabel}
      </text>
      <text x={pad} y={pad / 2 - 6} fontSize={font.size.xs} fill={color.inkSoft}>
        {yLabel}
      </text>
      {children}
    </svg>
  );
}

export function ticks(domain: [number, number], count: number, fmt: (v: number) => string) {
  const [a, b] = domain;
  return Array.from({ length: count }, (_, i) => {
    const v = a + ((b - a) * i) / (count - 1);
    return { v, label: fmt(v) };
  });
}
