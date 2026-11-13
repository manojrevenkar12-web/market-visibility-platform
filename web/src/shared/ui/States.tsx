import { motion } from "framer-motion";
import { color, layout, motion as m, radius, space } from "../theme/tokens";

const ROWS = 9;
const WIDTHS = [0.55, 0.42, 0.7, 0.38, 0.62, 0.48, 0.66, 0.4, 0.58]; // skeleton bars, roughly like the chart

export function LoadingSkeleton() {
  return (
    <div role="status" aria-live="polite" aria-label="Loading the report" className="page">
      <div style={{ ...bar(0.5, layout.stripRowHeight), marginBottom: space.md }} />
      <div style={{ ...bar(0.7, space.md), marginBottom: space.xl }} />
      {Array.from({ length: ROWS }, (_, i) => (
        <motion.div
          key={i}
          style={{ ...bar(WIDTHS[i], layout.dot * 3), marginBottom: space.md }}
          animate={{ opacity: [0.5, 1, 0.5] }}
          transition={{ duration: m.pulse, repeat: Infinity, delay: i * m.stagger }}
        />
      ))}
    </div>
  );
}

function bar(width: number, height: number): React.CSSProperties {
  return { width: `${width * 100}%`, height, background: color.rule, borderRadius: radius.sm };
}

export function ErrorState({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div role="alert" className="page">
      <h1 style={{ fontSize: "var(--font-xl)", marginBottom: space.sm }}>The report did not load</h1>
      <p className="prose" style={{ marginBottom: space.lg }}>
        {message} Generate it with <code>go run ./cmd/report</code>, or try again.
      </p>
      <button type="button" onClick={onRetry} style={buttonStyle}>
        Try again
      </button>
    </div>
  );
}

export function EmptyState() {
  return (
    <div className="page">
      <h1 style={{ fontSize: "var(--font-xl)", marginBottom: space.sm }}>No results yet</h1>
      <p className="prose">
        Run the study with <code>go run ./cmd/lab</code>, then <code>go run ./cmd/report</code> to write the dashboard data.
      </p>
    </div>
  );
}

const buttonStyle: React.CSSProperties = {
  font: "inherit",
  fontWeight: 600,
  padding: `${space.xs}px ${space.md}px`,
  borderRadius: radius.md,
  border: `1px solid ${color.ink}`,
  background: color.ink,
  color: color.surface,
  cursor: "pointer",
};
