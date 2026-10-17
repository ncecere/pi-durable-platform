/* When a key expires, in words: shared by personal API keys and service account keys. */
import { calendarDayDiff, formatDate, formatRelativeTime, toDate } from "./bitop-format";

/** "in 7 days", "tomorrow", "in 5 hours", "3 days ago": whole days from a day away, never weeks or months. */
export function expiryText(value: string | null | undefined, now = new Date()): string {
  const date = toDate(value);
  if (!date) return "";
  const days = calendarDayDiff(date, now);
  if (Math.abs(date.getTime() - now.getTime()) < 86_400_000 / 2 || days === 0) return formatRelativeTime(date, { now });
  try {
    return new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(days, "day");
  } catch {
    return formatDate(date);
  }
}
