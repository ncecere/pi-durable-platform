// TEST_TIME_SCALE stretches every test timeout (the unit tests' limit and findBy/waitFor wait, Playwright's test, expect, action and server
// limits) by one factor, for a machine slower than the runners the limits were set for, such as a shared cluster node. It is 1 by
// default, so CI is unchanged, and a limit, not a delay: a test that finishes in time is no slower, and a real hang still fails, only later.
// Only 1 to 10 is accepted; anything else (0.5, 0, a word) is ignored, so it can never shorten a limit.

/** The scale in a setting: 1 to 10, else 1. */
export function parseTimeScale(value: string | undefined): number {
  if (value === undefined || value.trim() === "") return 1;
  const n = Number(value);
  return Number.isFinite(n) && n >= 1 && n <= 10 ? n : 1;
}

/** A limit in milliseconds, times the scale in `value` (the environment's by default). */
export function scaled(ms: number, value: string | undefined = process.env.TEST_TIME_SCALE): number {
  return Math.round(ms * parseTimeScale(value));
}
