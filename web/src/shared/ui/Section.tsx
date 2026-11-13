import type { ReactNode } from "react";

export function Section({ id, title, lede, children }: { id: string; title: string; lede?: ReactNode; children: ReactNode }) {
  return (
    <section className="section" aria-labelledby={id}>
      <div className="section-head">
        <h2 id={id}>{title}</h2>
        {lede && <p className="prose">{lede}</p>}
      </div>
      {children}
    </section>
  );
}
