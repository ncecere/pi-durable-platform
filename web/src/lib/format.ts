/** Formatting helpers shared by pages. */

/** A medium date and short time in the user's locale, or "—" when missing. */
export function formatDate(value?: string | null) {
  if (!value) return "—";
  return new Date(value).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** "1 member", "12 members". */
export const plural = (n: number, one: string, many = one + "s") => `${n.toLocaleString()} ${n === 1 ? one : many}`;
