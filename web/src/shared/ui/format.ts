export const signedPct = (x: number, digits = 1) => `${x >= 0 ? "+" : "−"}${Math.abs(x * 100).toFixed(digits)}%`;
export const pct = (x: number, digits = 0) => `${(x * 100).toFixed(digits)}%`;
export const pp = (x: number) => `${x >= 0 ? "+" : "−"}${Math.abs(x).toFixed(2)} pp`;
export const thousands = (x: number) => x.toLocaleString("en-GB");
export const exposure = (bp: number) => (bp === 0 ? "closed" : `${bp / 100}%`);
