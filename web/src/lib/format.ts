/** Formatting helpers shared by pages. */

/** A medium date and short time in the user's locale, or "—" when missing. */
export function formatDate(value?: string | null) {
  if (!value) return "—";
  return new Date(value).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** "1 member", "12 members". */
export const plural = (n: number, one: string, many = one + "s") => `${n.toLocaleString()} ${n === 1 ? one : many}`;

/** Token counts and context windows: "128K", "131K", "1M", "1.5M", "8,192" (compact from 10,000 up, exact below). */
export function tokens(n: number | null | undefined): string {
  if (n == null) return "—";
  if (n >= 1_000_000) return `${Number((n / 1_000_000).toFixed(1))}M`;
  if (n >= 10_000) return `${Math.round(n / 1_000)}K`;
  return n.toLocaleString();
}

const dollars = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** A cost in US dollars with cents: "$12.40", "$0.00", and "<$0.01" for a tiny non-zero amount. */
export function usd(n: number | null | undefined): string {
  if (n == null) return "—";
  if (n > 0 && n < 0.005) return "<$0.01";
  return dollars.format(n);
}
