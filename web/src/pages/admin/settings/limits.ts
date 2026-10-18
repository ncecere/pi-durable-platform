/* Personal workspace limits as edited: numbers as typed text, checked before saving (the server's ranges). */

/** The server's ranges: max agents 0–10,000, max running 0–1,000. */
export const LIMIT_MAX = { maxAgents: 10000, maxRunning: 1000 } as const;

const whole = /^\d+$/;

/** "Enter a whole number from 0 to 10,000." for text that isn't one. */
export function countProblem(v: string, max: number): string | undefined {
  const t = v.trim();
  return whole.test(t) && Number(t) <= max ? undefined : `Enter a whole number from 0 to ${max.toLocaleString()}.`;
}

/** An optional amount in dollars: empty, or zero or more. */
export function budgetProblem(v: string): string | undefined {
  const t = v.trim();
  if (!t) return undefined;
  const n = Number(t);
  return Number.isFinite(n) && n >= 0 && n <= 1e9 ? undefined : "Enter an amount in dollars, such as 50 or 12.50, or leave it empty.";
}

/** A typed amount for the API: null when empty. */
export const budgetOf = (v: string): number | null => (v.trim() ? Number(v.trim()) : null);

/** A stored number as text ("" for none). */
export const text = (n: number | null | undefined) => (n == null ? "" : String(n));

export const budgetNote = "Recorded; not enforced yet.";
