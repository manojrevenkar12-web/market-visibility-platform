/** Every colour, size and duration the dashboard uses. Nothing else carries a raw value. */

export const color = {
  paper: "#F3F4F6",
  surface: "#FFFFFF",
  ink: "#14161C",
  inkSoft: "#4B5161",
  inkFaint: "#7C8292",
  rule: "#D9DCE3",
  ruleStrong: "#B4B9C6",
  fr: "#3949AB", // French sellers
  de: "#B7791F", // German sellers
  truth: "#14161C",
  truthBand: "#E3E5EA",
  honest: "#2F6B4F", // a design whose interval holds the truth
  biased: "#B3261E", // a design that misses it
  focus: "#3949AB",
} as const;

export const font = {
  family: '"Public Sans", system-ui, sans-serif',
  size: { xs: 12.8, sm: 14, md: 16, lg: 20, xl: 25, xxl: 31.25, display: 48.8 }, // 1.25 scale on 16
  weight: { regular: 400, medium: 500, semibold: 600, bold: 700 },
  leading: { tight: 1.08, snug: 1.3, body: 1.55 },
  tracking: { display: "-0.025em" },
} as const;

export const space = { xxs: 4, xs: 8, sm: 12, md: 16, lg: 24, xl: 40, xxl: 64, section: 96 } as const;

export const radius = { sm: 4, md: 8, pill: 999 } as const;

export const layout = {
  maxWidth: 1120,
  measure: 66,
  stripLabelWidth: 280,
  stripStatWidth: 170,
  stripRowHeight: 56,
  dot: 5,
  plotHeight: 280,
  axisPadding: 40,
  barWidth: 14,
} as const;

export const motion = {
  reveal: 0.6,
  stagger: 0.025,
  fade: 0.25,
  pulse: 1.6,
  ease: [0.22, 1, 0.36, 1] as const,
} as const;

export function cssVariables(): string {
  const entries: [string, string][] = [
    ...Object.entries(color).map(([k, v]) => [`--color-${k}`, v] as [string, string]),
    ...Object.entries(space).map(([k, v]) => [`--space-${k}`, `${v}px`] as [string, string]),
    ...Object.entries(font.size).map(([k, v]) => [`--font-${k}`, `${v / 16}rem`] as [string, string]),
    ["--font-family", font.family],
    ["--max-width", `${layout.maxWidth}px`],
    ["--measure", `${layout.measure}ch`],
  ];
  return `:root{${entries.map(([k, v]) => `${k}:${v}`).join(";")}}`;
}
